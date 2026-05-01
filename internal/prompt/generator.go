// Package prompt drafts a German system prompt for a bot from its
// crawled KB. Phase 2 ships one entry point: Generator.Draft pulls a
// handful of representative chunks via storage.SearchChunks, fills the
// embedded meta-prompt template, and asks Claude (via llm.Chat) to
// return a finished prompt.
//
// This is the LLM-author-of-prompts flow described in the PRD §Sprint 5.
package prompt

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

//go:embed template_de.tmpl
var tmplFS embed.FS

// metaProbe is the question we embed and search the KB with to surface
// representative chunks. Phrased as a generic "what does this company do?"
// so it ranks high against any About / Leistungen page.
const metaProbe = "Was macht dieses Unternehmen, welche Leistungen werden angeboten und an welche Zielgruppe?"

// metaTopK is how many chunks we feed into the meta-prompt. Six is enough
// to cover About/Leistungen/Preise/Kontakt without blowing the prompt budget.
const metaTopK = 6

// Bot is the slice of bot state Draft needs. Defined here so the prompt
// package doesn't pull a circular dep with eval.
type Bot struct {
	ID       int64
	Name     string
	Tone     string
	Industry string
}

// Generator drafts system prompts for bots.
type Generator struct {
	db       *sql.DB
	chat     llm.Chat
	embedder llm.Embedder
	tmpl     *template.Template
}

// New wires a Generator to its dependencies.
func New(db *sql.DB, chat llm.Chat, embedder llm.Embedder) (*Generator, error) {
	t, err := template.ParseFS(tmplFS, "template_de.tmpl")
	if err != nil {
		return nil, fmt.Errorf("prompt: parse meta-template: %w", err)
	}
	return &Generator{db: db, chat: chat, embedder: embedder, tmpl: t}, nil
}

// Draft asks the LLM for a German system prompt grounded in the bot's
// crawled KB. Returns the prompt text on success. Errors propagate from
// embedding, retrieval, template execution, or the LLM stream.
func (g *Generator) Draft(ctx context.Context, bot Bot) (string, error) {
	if bot.ID == 0 {
		return "", errors.New("prompt.Draft: bot.ID is required")
	}

	// 1. Embed the meta-probe and pull representative KB chunks.
	probeVecs, err := g.embedder.Embed(ctx, []string{metaProbe})
	if err != nil {
		return "", fmt.Errorf("prompt.Draft: embed probe: %w", err)
	}
	if len(probeVecs) != 1 {
		return "", fmt.Errorf("prompt.Draft: expected 1 probe vector, got %d", len(probeVecs))
	}
	hits, err := storage.SearchChunks(ctx, g.db, bot.ID, probeVecs[0], metaTopK)
	if err != nil {
		return "", fmt.Errorf("prompt.Draft: search chunks: %w", err)
	}

	// 2. Fill the meta-prompt template.
	tone := bot.Tone
	if strings.TrimSpace(tone) == "" {
		tone = "formal"
	}
	industry := bot.Industry
	if strings.TrimSpace(industry) == "" {
		industry = "(unbekannt — aus den Auszügen ableiten)"
	}
	companyName := bot.Name
	if strings.TrimSpace(companyName) == "" {
		companyName = "der Kanzlei"
	}

	var rendered strings.Builder
	if err := g.tmpl.Execute(&rendered, map[string]any{
		"CompanyName":   companyName,
		"Industry":      industry,
		"Tone":          tone,
		"DomainSummary": joinChunks(hits),
	}); err != nil {
		return "", fmt.Errorf("prompt.Draft: render meta-template: %w", err)
	}

	// 3. Ask Claude.
	ch, err := g.chat.Stream(ctx, llm.ChatRequest{
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: rendered.String()}},
		MaxTokens:   1200,
		Temperature: 0.4,
	})
	if err != nil {
		return "", fmt.Errorf("prompt.Draft: stream: %w", err)
	}

	var out strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return "", fmt.Errorf("prompt.Draft: stream chunk: %w", chunk.Err)
		}
		out.WriteString(chunk.Delta)
	}
	result := strings.TrimSpace(out.String())
	if result == "" {
		return "", errors.New("prompt.Draft: empty completion")
	}
	return result, nil
}

// joinChunks renders retrieved chunks as a numbered context block. Capped
// at 1200 chars per chunk to keep the meta-prompt under the model's input
// budget without truncating the entire pipeline.
func joinChunks(chunks []storage.Chunk) string {
	if len(chunks) == 0 {
		return "(Noch keine Inhalte gecrawlt — schreibe einen generischen Prompt für die Branche.)"
	}
	var b strings.Builder
	for i, c := range chunks {
		body := c.Content
		if len(body) > 1200 {
			body = body[:1200] + "…"
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, c.Source, body)
	}
	return strings.TrimRight(b.String(), "\n")
}
