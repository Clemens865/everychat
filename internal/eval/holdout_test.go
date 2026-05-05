package eval

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// holdoutEmbedder returns a constant 1536-dim vector. Sufficient for
// SearchChunks to find any chunks the test seeds, since we don't care
// about retrieval ordering — the runner's job is to call the path,
// not to produce semantically meaningful retrieval.
type holdoutEmbedder struct{ err error }

func (e holdoutEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, storage.EmbedDims)
		for j := range v {
			v[j] = 0.001
		}
		out[i] = v
	}
	return out, nil
}

// holdoutChat answers every question with a fixed reply that contains
// "Steuerberatung" — used to drive deterministic keyword scoring.
type holdoutChat struct {
	answer  string
	err     error
	saw     []llm.ChatRequest
	answers map[string]string // optional per-question override keyed by user message
}

func (c *holdoutChat) Stream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	c.saw = append(c.saw, req)
	if c.err != nil {
		return nil, c.err
	}
	answer := c.answer
	if c.answers != nil && len(req.Messages) > 0 {
		if a, ok := c.answers[req.Messages[0].Content]; ok {
			answer = a
		}
	}
	out := make(chan llm.ChatChunk, 2)
	out <- llm.ChatChunk{Delta: answer}
	out <- llm.ChatChunk{Done: true, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}}
	close(out)
	return out, nil
}

// seedBotForHoldout creates a bot + a few chunks so the runner has a
// realistic SearchChunks path to exercise.
func seedBotForHoldout(t *testing.T, em llm.Embedder) (*sql.DB, int64) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bots, _ := storage.ListBots(context.Background(), db)
	id := bots[0].ID

	for i, body := range []string{
		"Wir bieten Steuerberatung für KMU.",
		"DATEV-Lohnbuchhaltung mit ELSTER.",
	} {
		v, _ := em.Embed(context.Background(), []string{body})
		if _, err := storage.InsertChunk(context.Background(), db,
			storage.Chunk{BotID: id, Source: "demo.de/about", ChunkIndex: i, Content: body},
			v[0]); err != nil {
			t.Fatal(err)
		}
	}
	return db, id
}

func TestHoldout_HappyPath_KeywordScoring(t *testing.T) {
	em := holdoutEmbedder{}
	db, _ := seedBotForHoldout(t, em)

	chat := &holdoutChat{answer: "Wir bieten umfangreiche Steuerberatung für KMU."}
	r := NewHoldoutRunner(db, chat, em)

	bot, err := LookupBotByName(context.Background(), db, "steuerkanzlei-demo")
	if err != nil {
		t.Fatal(err)
	}
	bot.EvalMode = "keyword"

	holdout := []Question{
		{ID: "h1", Ask: "Was bietet ihr an?", MustContain: []string{"Steuerberatung"}},
		{ID: "h2", Ask: "Auch Lohnabrechnung?", MustContain: []string{"Steuerberatung"}}, // same bot answer matches
	}

	rep, err := r.Run(context.Background(), bot, holdout)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Total != 2 || rep.Passed != 2 {
		t.Fatalf("expected 2/2, got %d/%d", rep.Passed, rep.Total)
	}

	// Each item exercised the embed → search → chat path.
	if len(chat.saw) != 2 {
		t.Errorf("expected 2 chat calls, got %d", len(chat.saw))
	}
	// And the system prompt for each call had the RAG context appended.
	if !strings.Contains(chat.saw[0].System, "Relevante Auszüge") {
		t.Errorf("RAG context missing from system prompt: %q", chat.saw[0].System)
	}

	// last_holdout_score persisted.
	persisted, _ := storage.LoadBot(context.Background(), db, bot.ID)
	if !persisted.LastHoldoutScore.Valid || persisted.LastHoldoutScore.Float64 != 1.0 {
		t.Errorf("last_holdout_score: %+v", persisted.LastHoldoutScore)
	}
}

func TestHoldout_EmbedErrorBecomesPerQuestionFail(t *testing.T) {
	em := holdoutEmbedder{err: errors.New("rate limited")}
	db, _ := seedBotForHoldout(t, holdoutEmbedder{}) // seed with working embedder, then swap

	r := NewHoldoutRunner(db, &holdoutChat{}, em)
	bot, _ := LookupBotByName(context.Background(), db, "steuerkanzlei-demo")
	rep, err := r.Run(context.Background(), bot, []Question{{ID: "h1", Ask: "?"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 0 {
		t.Errorf("expected 0 passes when embedder errors, got %d", rep.Passed)
	}
	if !strings.Contains(rep.Items[0].Reasons[0], "embed error") {
		t.Errorf("expected embed error reason, got %v", rep.Items[0].Reasons)
	}
}

func TestHoldout_RejectsEmptyHoldoutSet(t *testing.T) {
	em := holdoutEmbedder{}
	db, _ := seedBotForHoldout(t, em)

	r := NewHoldoutRunner(db, &holdoutChat{}, em)
	bot, _ := LookupBotByName(context.Background(), db, "steuerkanzlei-demo")
	if _, err := r.Run(context.Background(), bot, nil); err == nil {
		t.Fatal("expected error for empty hold-out set")
	}
}
