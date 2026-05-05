package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AdversaryRun is the typed view of an `adversary_runs` row. Phase 5
// Sprint 1; consumed by the dashboard polling endpoint and the CLI.
type AdversaryRun struct {
	ID             int64
	BotID          int64
	Persona        string
	TurnCap        int
	MaxCostCents   int64
	TotalCostCents int64
	Status         string // running | completed | cost_exhausted | error
	Verdict        sql.NullString
	StartedAt      time.Time
	FinishedAt     sql.NullTime
}

// AdversaryTurn is one persisted turn in the transcript.
type AdversaryTurn struct {
	ID           int64
	RunID        int64
	TurnIndex    int
	Role         string // tester | victim
	Content      string
	InputTokens  int
	OutputTokens int
	CreatedAt    time.Time
}

// CreateAdversaryRun inserts a new run with status='running'. The
// runner updates status + verdict + total_cost_cents on completion.
func CreateAdversaryRun(ctx context.Context, db *sql.DB, botID int64, persona string, turnCap int, maxCostCents int64) (int64, error) {
	res, err := db.ExecContext(ctx, `
		INSERT INTO adversary_runs (bot_id, persona, turn_cap, max_cost_cents)
		VALUES (?, ?, ?, ?)
	`, botID, persona, turnCap, maxCostCents)
	if err != nil {
		return 0, fmt.Errorf("CreateAdversaryRun: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("CreateAdversaryRun: %w", err)
	}
	return id, nil
}

// AppendAdversaryTurn writes a single turn to the transcript. Called
// after every LLM response so a crashed run still leaves an
// inspectable transcript.
func AppendAdversaryTurn(ctx context.Context, db *sql.DB, runID int64, turn AdversaryTurn) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO adversary_turns (run_id, turn_index, role, content, input_tokens, output_tokens)
		VALUES (?, ?, ?, ?, ?, ?)
	`, runID, turn.TurnIndex, turn.Role, turn.Content, turn.InputTokens, turn.OutputTokens)
	if err != nil {
		return fmt.Errorf("AppendAdversaryTurn: %w", err)
	}
	return nil
}

// FinishAdversaryRun closes out a run with terminal state. Status
// must be one of completed | cost_exhausted | error; the runner
// validates this at the call site.
func FinishAdversaryRun(ctx context.Context, db *sql.DB, runID int64, status, verdict string, totalCents int64) error {
	_, err := db.ExecContext(ctx, `
		UPDATE adversary_runs
		   SET status = ?, verdict = NULLIF(?, ''), total_cost_cents = ?, finished_at = CURRENT_TIMESTAMP
		 WHERE id = ?
	`, status, verdict, totalCents, runID)
	if err != nil {
		return fmt.Errorf("FinishAdversaryRun: %w", err)
	}
	return nil
}

// LoadAdversaryRun reads a single run by id. Used by the dashboard
// polling endpoint and the CLI's --resume flag (future).
func LoadAdversaryRun(ctx context.Context, db *sql.DB, id int64) (AdversaryRun, error) {
	var r AdversaryRun
	err := db.QueryRowContext(ctx, `
		SELECT id, bot_id, persona, turn_cap, max_cost_cents, total_cost_cents,
		       status, verdict, started_at, finished_at
		  FROM adversary_runs
		 WHERE id = ?
	`, id).Scan(
		&r.ID, &r.BotID, &r.Persona, &r.TurnCap, &r.MaxCostCents, &r.TotalCostCents,
		&r.Status, &r.Verdict, &r.StartedAt, &r.FinishedAt,
	)
	if err != nil {
		return AdversaryRun{}, err
	}
	return r, nil
}

// ListAdversaryTurns returns every turn for a run in turn_index order.
func ListAdversaryTurns(ctx context.Context, db *sql.DB, runID int64) ([]AdversaryTurn, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, run_id, turn_index, role, content, input_tokens, output_tokens, created_at
		  FROM adversary_turns
		 WHERE run_id = ?
		 ORDER BY turn_index ASC, id ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("ListAdversaryTurns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AdversaryTurn
	for rows.Next() {
		var t AdversaryTurn
		if err := rows.Scan(&t.ID, &t.RunID, &t.TurnIndex, &t.Role, &t.Content, &t.InputTokens, &t.OutputTokens, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// LatestAdversaryRunForBot returns the most-recent run for a bot, used
// by the dashboard card to render the verdict badge. Returns
// sql.ErrNoRows if the bot has never been adversary-tested.
func LatestAdversaryRunForBot(ctx context.Context, db *sql.DB, botID int64) (AdversaryRun, error) {
	var r AdversaryRun
	err := db.QueryRowContext(ctx, `
		SELECT id, bot_id, persona, turn_cap, max_cost_cents, total_cost_cents,
		       status, verdict, started_at, finished_at
		  FROM adversary_runs
		 WHERE bot_id = ?
		 ORDER BY started_at DESC, id DESC
		 LIMIT 1
	`, botID).Scan(
		&r.ID, &r.BotID, &r.Persona, &r.TurnCap, &r.MaxCostCents, &r.TotalCostCents,
		&r.Status, &r.Verdict, &r.StartedAt, &r.FinishedAt,
	)
	if err != nil {
		return AdversaryRun{}, err
	}
	return r, nil
}
