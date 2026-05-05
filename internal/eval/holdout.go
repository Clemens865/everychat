package eval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// HoldoutTopK is how many KB chunks the hold-out runner retrieves per
// question. Matches the visitor /api/v1/chat path so the eval reflects
// the deployed shape, not the bare system prompt.
const HoldoutTopK = 5

// HoldoutRunner is a parallel of Runner that injects RAG context into
// the system prompt before each question — same retrieval path the
// visitor surface uses, so what we measure here is what visitors
// actually experience.
//
// Why a separate type instead of a flag on Runner: the dependency
// shape is genuinely different (HoldoutRunner needs an Embedder; Runner
// doesn't), and the persistence target differs (last_holdout_score, not
// eval_runs). Keeping them apart keeps each Run loop scannable.
type HoldoutRunner struct {
	db       *sql.DB
	chat     llm.Chat
	embedder llm.Embedder
	keyword  Scorer
	judge    Scorer
	now      func() time.Time
}

// NewHoldoutRunner constructs a HoldoutRunner. Both scorers are
// constructed eagerly for the same reason eval.Runner does it.
func NewHoldoutRunner(db *sql.DB, chat llm.Chat, embedder llm.Embedder) *HoldoutRunner {
	return &HoldoutRunner{
		db:       db,
		chat:     chat,
		embedder: embedder,
		keyword:  KeywordScorer{},
		judge:    NewJudgeScorer(chat),
		now:      time.Now,
	}
}

// SetClock overrides time.Now for tests.
func (r *HoldoutRunner) SetClock(now func() time.Time) { r.now = now }

// Run scores the bot against every question in `holdout`, persists
// last_holdout_score, and returns the Report. Errors propagate from
// LLM/retrieval failures only; per-question failures (LLM stream
// errors, judge errors) are recorded as fail items so the run can
// still produce a coherent score.
func (r *HoldoutRunner) Run(ctx context.Context, bot Bot, holdout []Question) (*Report, error) {
	if len(holdout) == 0 {
		return nil, errors.New("HoldoutRunner.Run: empty holdout set")
	}

	report := &Report{
		Bot:           bot.Name,
		QuestionsFile: "holdout",
		Total:         len(holdout),
		Threshold:     bot.Threshold,
		StartedAt:     r.now().UTC(),
		Items:         make([]Item, 0, len(holdout)),
	}

	scorer := r.keyword
	if bot.EvalMode == "llm_judge" && r.judge != nil {
		scorer = r.judge
	}

	for _, q := range holdout {
		item := r.runOne(ctx, bot, q, scorer)
		if item.Pass {
			report.Passed++
		}
		report.Items = append(report.Items, item)
	}
	report.FinishedAt = r.now().UTC()
	if report.Total > 0 {
		report.Score = float64(report.Passed) / float64(report.Total)
	}

	// Persist last_holdout_score on the bot so the dashboard badge picks
	// it up. Failure here is logged-only — the report is still useful
	// to the caller as a return value.
	if err := storage.SetLastHoldoutScore(ctx, r.db, bot.ID, report.Score); err != nil {
		return report, fmt.Errorf("HoldoutRunner: persist score: %w", err)
	}
	return report, nil
}

// runOne runs one question through the visitor pipeline:
// embed → SearchChunks → system prompt + RAG context → LLM → score.
func (r *HoldoutRunner) runOne(ctx context.Context, bot Bot, q Question, scorer Scorer) Item {
	item := Item{
		ID:        q.ID,
		Question:  q.Ask,
		StartedAt: r.now().UTC(),
	}

	// 1. Embed the question.
	vecs, err := r.embedder.Embed(ctx, []string{q.Ask})
	if err != nil || len(vecs) != 1 {
		item.FinishedAt = r.now().UTC()
		item.Pass = false
		item.Reasons = []string{fmt.Sprintf("embed error: %v", err)}
		return item
	}

	// 2. RAG retrieval.
	hits, err := storage.SearchChunks(ctx, r.db, bot.ID, vecs[0], HoldoutTopK)
	if err != nil {
		item.FinishedAt = r.now().UTC()
		item.Pass = false
		item.Reasons = []string{fmt.Sprintf("retrieval error: %v", err)}
		return item
	}

	// 3. System prompt with RAG context — same shape as the visitor
	// chat handler in internal/api/chat.go.
	sys := bot.SystemPrompt
	if len(hits) > 0 {
		var ctxBlock strings.Builder
		ctxBlock.WriteString("\n\nRelevante Auszüge aus der Wissensbasis:\n")
		for i, h := range hits {
			fmt.Fprintf(&ctxBlock, "[%d] %s\n%s\n\n", i+1, h.Source, truncateChunk(h.Content, 800))
		}
		sys += ctxBlock.String()
	}

	maxTok := q.MaxTokens
	if maxTok == 0 {
		maxTok = 600
	}

	ch, err := r.chat.Stream(ctx, llm.ChatRequest{
		System:      sys,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: q.Ask}},
		MaxTokens:   maxTok,
		Temperature: 0.4, // matches the visitor sandbox temperature
	})
	if err != nil {
		item.FinishedAt = r.now().UTC()
		item.Pass = false
		item.Reasons = []string{fmt.Sprintf("llm error: %v", err)}
		return item
	}

	var b strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			item.FinishedAt = r.now().UTC()
			item.Pass = false
			item.Reasons = []string{fmt.Sprintf("stream error: %v", chunk.Err)}
			return item
		}
		b.WriteString(chunk.Delta)
		if chunk.Usage != nil {
			item.InputTokens = chunk.Usage.InputTokens
			item.OutputTokens = chunk.Usage.OutputTokens
		}
	}

	item.Answer = b.String()
	item.Pass, item.Reasons = scorer.Score(ctx, q, item.Answer)
	item.FinishedAt = r.now().UTC()
	return item
}

// truncateChunk caps a chunk's content for inclusion in the system
// prompt — same 800-char shape internal/api/chat.go uses.
func truncateChunk(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
