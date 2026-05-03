package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/llm"
)

// Bot is the slice of bots-table state the runner needs. Defined here so
// the storage package doesn't need to depend on eval and vice versa.
type Bot struct {
	ID           int64
	Name         string
	SystemPrompt string
	Threshold    float64
	EvalMode     string // "keyword" | "llm_judge" — defaults to keyword when ""
}

// Runner executes a golden-questions file against one bot.
type Runner struct {
	db      *sql.DB
	chat    llm.Chat
	keyword Scorer // used when bot.EvalMode == "keyword" (or empty)
	judge   Scorer // used when bot.EvalMode == "llm_judge"; may be nil if no chat for judge
	now     func() time.Time
}

// NewRunner wires a Runner to the DB and chat client. Both scorers
// are constructed eagerly: keyword is free, judge reuses the same
// llm.Chat (LiteLLM routes to the claude-haiku alias by model param).
func NewRunner(db *sql.DB, chat llm.Chat) *Runner {
	return &Runner{
		db:      db,
		chat:    chat,
		keyword: KeywordScorer{},
		judge:   NewJudgeScorer(chat),
		now:     time.Now,
	}
}

// pickScorer returns the configured scorer for `mode`. Empty/unknown
// falls through to keyword — the safe default that always works
// regardless of LiteLLM availability.
func (r *Runner) pickScorer(mode string) Scorer {
	if mode == "llm_judge" && r.judge != nil {
		return r.judge
	}
	return r.keyword
}

// SetClock overrides the wall clock for tests.
func (r *Runner) SetClock(now func() time.Time) { r.now = now }

// Run loads `questionsPath`, asks each question against `bot.SystemPrompt`,
// scores the answers, and persists an `eval_runs` row. Returns the full
// Report regardless of whether the threshold was met.
func (r *Runner) Run(ctx context.Context, bot Bot, questionsPath string) (*Report, error) {
	raw, err := os.ReadFile(questionsPath) //nolint:gosec // operator-supplied path is intentional
	if err != nil {
		return nil, fmt.Errorf("eval run: read %s: %w", questionsPath, err)
	}
	file, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	report := &Report{
		Bot:           bot.Name,
		QuestionsFile: questionsPath,
		Total:         len(file.Questions),
		Threshold:     bot.Threshold,
		StartedAt:     r.now().UTC(),
		Items:         make([]Item, 0, len(file.Questions)),
	}

	for _, q := range file.Questions {
		item := r.runOne(ctx, bot, q)
		if item.Pass {
			report.Passed++
		}
		report.Items = append(report.Items, item)
	}
	report.FinishedAt = r.now().UTC()
	if report.Total > 0 {
		report.Score = float64(report.Passed) / float64(report.Total)
	}

	if err := r.persist(ctx, bot.ID, report); err != nil {
		return report, fmt.Errorf("eval run: persist: %w", err)
	}
	return report, nil
}

// runOne asks a single question and scores the answer.
func (r *Runner) runOne(ctx context.Context, bot Bot, q Question) Item {
	item := Item{
		ID:        q.ID,
		Question:  q.Ask,
		StartedAt: r.now().UTC(),
	}

	maxTok := q.MaxTokens
	if maxTok == 0 {
		maxTok = 400
	}

	ch, err := r.chat.Stream(ctx, llm.ChatRequest{
		System:    bot.SystemPrompt,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: q.Ask}},
		MaxTokens: maxTok,
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
	item.Pass, item.Reasons = r.pickScorer(bot.EvalMode).Score(ctx, q, item.Answer)
	item.FinishedAt = r.now().UTC()
	return item
}

// persist writes the eval_runs row. Errors propagate but the in-memory
// report stays valid for the caller to render.
func (r *Runner) persist(ctx context.Context, botID int64, rep *Report) error {
	js, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO eval_runs
			(bot_id, questions_file, total, passed, score, started_at, finished_at, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, botID, rep.QuestionsFile, rep.Total, rep.Passed, rep.Score,
		rep.StartedAt, rep.FinishedAt, string(js))
	return err
}

// LoadBot fetches the runner's required slice of bot state by ID.
func LoadBot(ctx context.Context, db *sql.DB, id int64) (Bot, error) {
	var b Bot
	err := db.QueryRowContext(ctx,
		`SELECT id, name, system_prompt, eval_threshold, COALESCE(eval_mode, 'keyword') FROM bots WHERE id = ?`, id,
	).Scan(&b.ID, &b.Name, &b.SystemPrompt, &b.Threshold, &b.EvalMode)
	if err != nil {
		return Bot{}, fmt.Errorf("load bot %d: %w", id, err)
	}
	return b, nil
}

// LookupBotByName resolves a bot by its `name` column. Useful for the CLI
// where operators type human names rather than IDs.
func LookupBotByName(ctx context.Context, db *sql.DB, name string) (Bot, error) {
	var b Bot
	err := db.QueryRowContext(ctx,
		`SELECT id, name, system_prompt, eval_threshold, COALESCE(eval_mode, 'keyword') FROM bots WHERE name = ?`, name,
	).Scan(&b.ID, &b.Name, &b.SystemPrompt, &b.Threshold, &b.EvalMode)
	if err != nil {
		return Bot{}, fmt.Errorf("load bot %q: %w", name, err)
	}
	return b, nil
}

// LatestRun returns the most recently finished eval_runs row for a bot,
// or sql.ErrNoRows if none exists. Used by the publish-gate middleware
// to decide whether the bot is over its threshold.
func LatestRun(ctx context.Context, db *sql.DB, botID int64) (Report, error) {
	row := db.QueryRowContext(ctx, `
		SELECT bot_id, questions_file, total, passed, score, started_at, finished_at
		FROM eval_runs
		WHERE bot_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, botID)
	var rep Report
	var botIDOut int64
	err := row.Scan(&botIDOut, &rep.QuestionsFile, &rep.Total, &rep.Passed, &rep.Score, &rep.StartedAt, &rep.FinishedAt)
	return rep, err
}
