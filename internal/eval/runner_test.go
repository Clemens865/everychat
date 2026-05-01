package eval

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// fakeChat is a deterministic Chat implementation keyed by the user
// message. Each entry is the assistant content streamed back in one chunk.
type fakeChat struct {
	answers map[string]string
	err     error
}

func (f *fakeChat) Stream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(chan llm.ChatChunk, 4)
	last := ""
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1].Content
	}
	answer, ok := f.answers[last]
	if !ok {
		answer = "(no fixture)"
	}
	out <- llm.ChatChunk{Delta: answer, Usage: &llm.Usage{InputTokens: 12, OutputTokens: 8}}
	out <- llm.ChatChunk{Done: true, Usage: &llm.Usage{InputTokens: 12, OutputTokens: 8}}
	close(out)
	return out, nil
}

func TestParse_Valid(t *testing.T) {
	f, err := Parse([]byte(`bot: x
questions:
  - id: q1
    ask: hi
    must_contain: ["a"]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Bot != "x" || len(f.Questions) != 1 || f.Questions[0].ID != "q1" {
		t.Fatalf("unexpected: %#v", f)
	}
}

func TestParse_DuplicateID(t *testing.T) {
	_, err := Parse([]byte(`bot: x
questions:
  - id: q1
    ask: hi
  - id: q1
    ask: bye
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-id error, got %v", err)
	}
}

func TestParse_MissingBot(t *testing.T) {
	_, err := Parse([]byte(`questions:
  - id: q1
    ask: hi
`))
	if err == nil || !strings.Contains(err.Error(), "bot") {
		t.Fatalf("expected missing-bot error, got %v", err)
	}
}

func TestRun_AllPass(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "qs.yaml")
	mustWrite(t, yamlPath, `bot: demo
questions:
  - id: q1
    ask: greet
    must_contain: ["hallo"]
  - id: q2
    ask: thanks
    must_contain: ["bitte"]
`)

	chat := &fakeChat{answers: map[string]string{
		"greet":  "Hallo! Wie kann ich helfen?",
		"thanks": "Sehr gerne, bitte schön.",
	}}
	r, db := newRunner(t, chat)

	rep, err := r.Run(context.Background(), Bot{
		ID: 1, Name: "demo", SystemPrompt: "be helpful", Threshold: 0.85,
	}, yamlPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Passed != 2 || rep.Total != 2 || rep.Score != 1.0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if !rep.MeetsThreshold() {
		t.Fatal("expected to meet threshold")
	}

	// Verify scorecard rendering.
	buf := &bytes.Buffer{}
	PrintScorecard(buf, rep)
	out := buf.String()
	if !strings.Contains(out, "PASS  q1") || !strings.Contains(out, "above threshold 0.85") {
		t.Fatalf("scorecard:\n%s", out)
	}
	// And persistence.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM eval_runs WHERE bot_id=1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 eval_runs row, got %d", n)
	}
}

func TestRun_PartialPass(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "qs.yaml")
	mustWrite(t, yamlPath, `bot: demo
questions:
  - id: q-good
    ask: q1
    must_contain: ["yes"]
  - id: q-missing
    ask: q2
    must_contain: ["yes"]
  - id: q-forbidden
    ask: q3
    must_not_contain: ["secret"]
`)
	chat := &fakeChat{answers: map[string]string{
		"q1": "yes definitely",
		"q2": "no idea",
		"q3": "the secret answer",
	}}
	r, _ := newRunner(t, chat)

	rep, err := r.Run(context.Background(), Bot{ID: 1, Name: "demo", Threshold: 0.85}, yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 1 || rep.Total != 3 {
		t.Fatalf("unexpected: %+v", rep)
	}
	if rep.MeetsThreshold() {
		t.Fatal("expected to be below threshold")
	}
	// Specific failure reasons surfaced.
	for _, it := range rep.Items {
		switch it.ID {
		case "q-missing":
			if it.Pass || len(it.Reasons) != 1 || !strings.Contains(it.Reasons[0], "missing keyword") {
				t.Errorf("q-missing reasons: %v", it.Reasons)
			}
		case "q-forbidden":
			if it.Pass || !strings.Contains(it.Reasons[0], "forbidden keyword") {
				t.Errorf("q-forbidden reasons: %v", it.Reasons)
			}
		}
	}
}

func TestRun_LLMErrorMakesQuestionFail(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "qs.yaml")
	mustWrite(t, yamlPath, `bot: demo
questions:
  - id: q1
    ask: hi
    must_contain: ["x"]
`)
	chat := &fakeChat{err: errors.New("rate limited")}
	r, _ := newRunner(t, chat)

	rep, err := r.Run(context.Background(), Bot{ID: 1, Name: "demo", Threshold: 0.85}, yamlPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Passed != 0 || rep.Total != 1 {
		t.Fatalf("unexpected: %+v", rep)
	}
	if rep.Items[0].Pass || !strings.Contains(rep.Items[0].Reasons[0], "rate limited") {
		t.Fatalf("expected llm-error reason, got %v", rep.Items[0].Reasons)
	}
}

func TestRun_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "qs.yaml")
	mustWrite(t, yamlPath, "this is :: not yaml :::")
	r, _ := newRunner(t, &fakeChat{})

	if _, err := r.Run(context.Background(), Bot{ID: 1, Name: "demo"}, yamlPath); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRun_DemoBotSeededByMigration(t *testing.T) {
	// Sanity check: migration 002_phase2 should have seeded the demo bot.
	_, db := newRunner(t, &fakeChat{})
	bot, err := LookupBotByName(context.Background(), db, "steuerkanzlei-demo")
	if err != nil {
		t.Fatalf("LookupBotByName: %v", err)
	}
	if bot.Threshold != 0.85 {
		t.Errorf("expected threshold 0.85, got %v", bot.Threshold)
	}
	if !strings.Contains(strings.ToLower(bot.SystemPrompt), "steuerberatung") {
		t.Errorf("seeded prompt should mention steuerberatung")
	}
}

// helpers ----

func newRunner(t *testing.T, chat llm.Chat) (*Runner, *sql.DB) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewRunner(db, chat), db
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
