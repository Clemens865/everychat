package adversary

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

// Status constants for adversary_runs.status. Mirrored from the
// migration comment so application code uses the same vocabulary.
const (
	StatusRunning        = "running"
	StatusCompleted      = "completed"
	StatusCostExhausted  = "cost_exhausted"
	StatusError          = "error"
)

// Role constants for adversary_turns.role.
const (
	RoleTester = "tester"
	RoleVictim = "victim"
)

// Default LLM call shape — kept conservative. Adversary chats are not
// retrieval-augmented in v1: the point is to probe the system prompt
// under pressure, and adding retrieval doubles the failure surface
// (embedder rate limits, retrieval-quality regressions). A future
// iteration may add an opt-in RAG mode.
const (
	defaultMaxTokens     = 512
	defaultTemperature   = 0.7  // tester benefits from variety
	victimTemperature    = 0.4  // matches eval.HoldoutRunner
)

// Runner drives one adversary run end-to-end. db is required (every
// turn is persisted); victim/tester are usually the same llm.Chat
// instance (LiteLLM proxies to Claude for both sides) but the
// interface lets tests swap one without the other.
type Runner struct {
	db     *sql.DB
	victim llm.Chat
	tester llm.Chat
	now    func() time.Time
}

// NewRunner constructs a Runner. Pass the same llm.Chat for both
// victim and tester unless you have a reason to split them (e.g. a
// red-team-tuned tester model behind a different alias).
func NewRunner(db *sql.DB, victim, tester llm.Chat) *Runner {
	return &Runner{db: db, victim: victim, tester: tester, now: time.Now}
}

// SetClock overrides time.Now for tests.
func (r *Runner) SetClock(now func() time.Time) { r.now = now }

// Options bound a single Run. TurnCap counts *victim* turns
// (i.e. how many times the bot speaks); the actual turn count
// in the transcript is roughly 2× because each victim turn is
// preceded by a tester turn. MaxCostCents <= 0 means "no cap" —
// only used by tests; CLI/HTTP must pass a real value.
type Options struct {
	TurnCap      int
	MaxCostCents int64
}

// Report is what Run returns to the caller. The transcript is also
// persisted to adversary_turns; callers that want to inspect it later
// should use storage.ListAdversaryTurns.
type Report struct {
	RunID          int64
	BotID          int64
	Persona        string
	Status         string
	Verdict        string
	TotalCostCents int64
	Turns          []storage.AdversaryTurn
	StartedAt      time.Time
	FinishedAt     time.Time
}

// Run executes one adversarial conversation. Errors propagate from
// storage failures only; LLM/judge failures are absorbed into a
// terminal status='error' run with a partial transcript so the
// caller can still inspect what happened.
func (r *Runner) Run(ctx context.Context, bot storage.Bot, persona Persona, opts Options) (*Report, error) {
	if opts.TurnCap <= 0 {
		return nil, errors.New("adversary.Run: TurnCap must be > 0")
	}

	startedAt := r.now().UTC()
	runID, err := storage.CreateAdversaryRun(ctx, r.db, bot.ID, persona.ID, opts.TurnCap, opts.MaxCostCents)
	if err != nil {
		return nil, err
	}

	gate := NewCostGate(opts.MaxCostCents)
	var transcript []storage.AdversaryTurn
	turnIdx := 0

	// Turn 0: tester opens with the persona's opening message. No LLM
	// cost — the message is checked-in YAML, not generated.
	openingTurn := storage.AdversaryTurn{
		RunID: runID, TurnIndex: turnIdx, Role: RoleTester,
		Content: persona.OpeningMessage,
	}
	if err := storage.AppendAdversaryTurn(ctx, r.db, runID, openingTurn); err != nil {
		return r.terminateError(ctx, runID, transcript, gate, startedAt, persona.ID, bot.ID, err)
	}
	transcript = append(transcript, openingTurn)
	turnIdx++

	// Drive the alternating loop. Each iteration = one victim turn.
	for victimTurnsTaken := 0; victimTurnsTaken < opts.TurnCap; victimTurnsTaken++ {
		// Victim responds to the latest tester message.
		victimMsg, victimUsage, err := r.callVictim(ctx, bot, transcript)
		if err != nil {
			return r.terminateError(ctx, runID, transcript, gate, startedAt, persona.ID, bot.ID,
				fmt.Errorf("victim turn %d: %w", turnIdx, err))
		}
		victimTurn := storage.AdversaryTurn{
			RunID: runID, TurnIndex: turnIdx, Role: RoleVictim,
			Content: victimMsg, InputTokens: victimUsage.InputTokens, OutputTokens: victimUsage.OutputTokens,
		}
		if err := storage.AppendAdversaryTurn(ctx, r.db, runID, victimTurn); err != nil {
			return nil, err
		}
		transcript = append(transcript, victimTurn)
		turnIdx++

		if _, exceeded := gate.Charge(victimUsage); exceeded {
			return r.terminate(ctx, runID, transcript, gate, startedAt, persona.ID, bot.ID, StatusCostExhausted, "")
		}

		// If we just took the final victim turn, stop before the tester
		// generates a follow-up no one will answer.
		if victimTurnsTaken+1 >= opts.TurnCap {
			break
		}

		// Tester generates the next attack.
		testerMsg, testerUsage, err := r.callTester(ctx, persona, transcript)
		if err != nil {
			return r.terminateError(ctx, runID, transcript, gate, startedAt, persona.ID, bot.ID,
				fmt.Errorf("tester turn %d: %w", turnIdx, err))
		}
		testerTurn := storage.AdversaryTurn{
			RunID: runID, TurnIndex: turnIdx, Role: RoleTester,
			Content: testerMsg, InputTokens: testerUsage.InputTokens, OutputTokens: testerUsage.OutputTokens,
		}
		if err := storage.AppendAdversaryTurn(ctx, r.db, runID, testerTurn); err != nil {
			return nil, err
		}
		transcript = append(transcript, testerTurn)
		turnIdx++

		if _, exceeded := gate.Charge(testerUsage); exceeded {
			return r.terminate(ctx, runID, transcript, gate, startedAt, persona.ID, bot.ID, StatusCostExhausted, "")
		}
	}

	verdict := r.judge(ctx, persona, transcript)
	return r.terminate(ctx, runID, transcript, gate, startedAt, persona.ID, bot.ID, StatusCompleted, verdict)
}

// callVictim sends the conversation to the bot-under-test. Tester
// messages map to user; victim messages map to assistant. Skips RAG
// for v1 (see package comment).
func (r *Runner) callVictim(ctx context.Context, bot storage.Bot, transcript []storage.AdversaryTurn) (string, llm.Usage, error) {
	msgs := make([]llm.Message, 0, len(transcript))
	for _, t := range transcript {
		switch t.Role {
		case RoleTester:
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: t.Content})
		case RoleVictim:
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: t.Content})
		}
	}
	return streamToString(ctx, r.victim, llm.ChatRequest{
		System:      bot.SystemPrompt,
		Messages:    msgs,
		MaxTokens:   defaultMaxTokens,
		Temperature: victimTemperature,
	})
}

// callTester asks the tester-LLM for the next attack. The tester sees
// the conversation with roles flipped: its prior outputs are
// assistant, victim's responses are user.
func (r *Runner) callTester(ctx context.Context, persona Persona, transcript []storage.AdversaryTurn) (string, llm.Usage, error) {
	msgs := make([]llm.Message, 0, len(transcript))
	for _, t := range transcript {
		switch t.Role {
		case RoleTester:
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: t.Content})
		case RoleVictim:
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: t.Content})
		}
	}
	return streamToString(ctx, r.tester, llm.ChatRequest{
		System:      persona.TesterSystem,
		Messages:    msgs,
		MaxTokens:   defaultMaxTokens,
		Temperature: defaultTemperature,
	})
}

// judge runs the persona's rubric against the full transcript and
// returns a one-line verdict ("PASS — ..." or "FAIL — ..."). A judge
// failure (LLM error, parse error) becomes a "JUDGE_ERROR" verdict so
// the run still completes — the transcript itself is the source of
// truth even if the judge is flaky.
func (r *Runner) judge(ctx context.Context, persona Persona, transcript []storage.AdversaryTurn) string {
	if len(transcript) == 0 {
		return "JUDGE_ERROR — empty transcript"
	}
	var b strings.Builder
	b.WriteString("Hier das vollständige Transkript einer adversariellen Test-Session:\n\n")
	for _, t := range transcript {
		fmt.Fprintf(&b, "[%s] %s\n\n", t.Role, t.Content)
	}
	prompt, _, err := streamToString(ctx, r.tester, llm.ChatRequest{
		System:      persona.JudgeRubric,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: b.String()}},
		MaxTokens:   240,
		Temperature: 0.0,
	})
	if err != nil {
		return "JUDGE_ERROR — " + err.Error()
	}
	verdict := strings.TrimSpace(prompt)
	if verdict == "" {
		return "JUDGE_ERROR — empty response"
	}
	return verdict
}

// terminate writes the final state and assembles the Report.
func (r *Runner) terminate(ctx context.Context, runID int64, transcript []storage.AdversaryTurn, gate *CostGate, startedAt time.Time, persona string, botID int64, status, verdict string) (*Report, error) {
	if err := storage.FinishAdversaryRun(ctx, r.db, runID, status, verdict, gate.AccumulatedCents()); err != nil {
		return nil, err
	}
	return &Report{
		RunID:          runID,
		BotID:          botID,
		Persona:        persona,
		Status:         status,
		Verdict:        verdict,
		TotalCostCents: gate.AccumulatedCents(),
		Turns:          transcript,
		StartedAt:      startedAt,
		FinishedAt:     r.now().UTC(),
	}, nil
}

// terminateError marks the run as errored and surfaces the failure.
// Returns the partial Report alongside the error so callers can still
// render the transcript.
func (r *Runner) terminateError(ctx context.Context, runID int64, transcript []storage.AdversaryTurn, gate *CostGate, startedAt time.Time, persona string, botID int64, cause error) (*Report, error) {
	verdict := "ERROR — " + cause.Error()
	rep, _ := r.terminate(ctx, runID, transcript, gate, startedAt, persona, botID, StatusError, verdict)
	return rep, cause
}

// streamToString drains an llm.Chat stream into a single string +
// usage. Returns the first chunk error encountered; mid-stream errors
// abort but the partial content is discarded (the caller will record
// the error verdict).
func streamToString(ctx context.Context, chat llm.Chat, req llm.ChatRequest) (string, llm.Usage, error) {
	ch, err := chat.Stream(ctx, req)
	if err != nil {
		return "", llm.Usage{}, err
	}
	var b strings.Builder
	var usage llm.Usage
	for chunk := range ch {
		if chunk.Err != nil {
			return "", llm.Usage{}, chunk.Err
		}
		b.WriteString(chunk.Delta)
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
	}
	return b.String(), usage, nil
}
