package widget

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// metaFS holds the theme-suggestion meta-prompt. Separate embed.FS from
// templateFS in widget.go because that one only matches `templates/*.html`.
//
//go:embed theme_de.tmpl
var metaFS embed.FS

// metaProbe is the question we embed and search the KB with to surface
// representative chunks for theme inspiration. Same probe shape as
// internal/prompt/generator.go uses — different meta-prompt, same
// retrieval contract.
const metaProbe = "Wofür ist dieses Unternehmen bekannt, welche Marke und Tonalität strahlt die Website aus?"

// metaTopK is how many chunks the suggester feeds Claude. Six is
// enough for a coherent brand read without inflating token cost.
const metaTopK = 6

// Suggester proposes Theme defaults from a bot's KB. Distinct from
// prompt.Generator (which drafts the system prompt) — same retrieval
// path, different LLM prompt, different output shape.
type Suggester struct {
	db       *sql.DB
	chat     llm.Chat
	embedder llm.Embedder
	tmpl     *template.Template
}

// SuggestBot is the slice of bot state Suggest needs. Defined here so
// callers can pass synthetic bots in tests without touching storage.
type SuggestBot struct {
	ID       int64
	Name     string
	Tone     string
	Industry string
}

// NewSuggester wires a Suggester to its dependencies.
func NewSuggester(db *sql.DB, chat llm.Chat, embedder llm.Embedder) (*Suggester, error) {
	t, err := template.ParseFS(metaFS, "theme_de.tmpl")
	if err != nil {
		return nil, fmt.Errorf("widget.Suggester: parse meta template: %w", err)
	}
	return &Suggester{db: db, chat: chat, embedder: embedder, tmpl: t}, nil
}

// Suggest asks Claude for a Theme grounded in the bot's KB and returns
// the parsed result. Errors propagate from embedding, retrieval,
// template execution, the LLM stream, or JSON parsing — the caller
// decides whether to persist or surface the error to the founder.
func (s *Suggester) Suggest(ctx context.Context, bot SuggestBot) (Theme, error) {
	if bot.ID == 0 {
		return Theme{}, errors.New("widget.Suggest: bot.ID is required")
	}

	// 1. Embed the probe.
	probeVecs, err := s.embedder.Embed(ctx, []string{metaProbe})
	if err != nil {
		return Theme{}, fmt.Errorf("widget.Suggest: embed probe: %w", err)
	}
	if len(probeVecs) != 1 {
		return Theme{}, fmt.Errorf("widget.Suggest: expected 1 probe vector, got %d", len(probeVecs))
	}

	// 2. Pull top-K chunks for the bot.
	hits, err := storage.SearchChunks(ctx, s.db, bot.ID, probeVecs[0], metaTopK)
	if err != nil {
		return Theme{}, fmt.Errorf("widget.Suggest: search chunks: %w", err)
	}

	// 3. Render the meta-prompt.
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
		companyName = "das Unternehmen"
	}

	var rendered strings.Builder
	if err := s.tmpl.Execute(&rendered, map[string]any{
		"CompanyName":   companyName,
		"Industry":      industry,
		"Tone":          tone,
		"DomainSummary": joinChunks(hits),
	}); err != nil {
		return Theme{}, fmt.Errorf("widget.Suggest: render meta-template: %w", err)
	}

	// 4. Ask Claude.
	ch, err := s.chat.Stream(ctx, llm.ChatRequest{
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: rendered.String()}},
		MaxTokens:   600,
		Temperature: 0.7, // a touch of variability so re-clicking returns alternatives
	})
	if err != nil {
		return Theme{}, fmt.Errorf("widget.Suggest: stream: %w", err)
	}

	var raw strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return Theme{}, fmt.Errorf("widget.Suggest: stream chunk: %w", chunk.Err)
		}
		raw.WriteString(chunk.Delta)
	}

	// 5. Extract + parse the JSON object. Even with the strict prompt,
	// Claude occasionally wraps the response in ```json … ```; strip
	// fenced blocks defensively.
	jsonText := extractJSONObject(raw.String())
	if jsonText == "" {
		return Theme{}, fmt.Errorf("widget.Suggest: no JSON object in response: %q", raw.String())
	}

	var parsed struct {
		Accent         string   `json:"accent"`
		Welcome        string   `json:"welcome"`
		StarterPrompts []string `json:"starter_prompts"`
	}
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return Theme{}, fmt.Errorf("widget.Suggest: parse JSON: %w (got %q)", err, jsonText)
	}

	// 6. Build Theme. sanitizeColor inside CSSVars is the rendering-
	// time gate; we also normalize at decode time so persisted JSON
	// doesn't carry malicious values that pass JSON but fail CSS.
	t := Theme{
		Accent:         sanitizeColor(parsed.Accent),
		Welcome:        truncateRunes(strings.TrimSpace(parsed.Welcome), 140),
		StarterPrompts: clampStarters(parsed.StarterPrompts, 3),
		Locale:         "de",
		Radius:         DefaultTheme.Radius,
	}
	if t.Welcome == "" {
		t.Welcome = DefaultTheme.Welcome
	}
	return t, nil
}

// joinChunks renders chunks as a numbered context block, capped at 1000
// chars per chunk to stay under the meta-prompt's token budget.
func joinChunks(chunks []storage.Chunk) string {
	if len(chunks) == 0 {
		return "(Noch keine Inhalte verfügbar — schlage neutrale, branchen-typische Werte vor.)"
	}
	var b strings.Builder
	for i, c := range chunks {
		body := c.Content
		if len(body) > 1000 {
			body = body[:1000] + "…"
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, c.Source, body)
	}
	return strings.TrimRight(b.String(), "\n")
}

// extractJSONObject scans for the first balanced `{...}` block in the
// LLM's output. Tolerates ```json fences, prose preamble, and trailing
// chatter — though the prompt asks for none of those.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// truncateRunes safely cuts a string at a rune boundary.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// clampStarters drops empties + caps the slice length.
func clampStarters(in []string, max int) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if r := []rune(p); len(r) > 60 {
			p = string(r[:60])
		}
		out = append(out, p)
		if len(out) == max {
			break
		}
	}
	return out
}
