package prompt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// fakeChat captures the request it receives and returns a canned answer.
type fakeChat struct {
	reqSeen llm.ChatRequest
	answer  string
	err     error
}

func (f *fakeChat) Stream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	f.reqSeen = req
	if f.err != nil {
		return nil, f.err
	}
	out := make(chan llm.ChatChunk, 2)
	out <- llm.ChatChunk{Delta: f.answer}
	out <- llm.ChatChunk{Done: true}
	close(out)
	return out, nil
}

// fakeEmbedder returns a fixed vector regardless of input.
type fakeEmbedder struct{ err error }

func (f *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, storage.EmbedDims)
		for j := range v {
			v[j] = 0.001 // deterministic, non-zero
		}
		out[i] = v
	}
	return out, nil
}

func TestDraft_HappyPath(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Seed a bot + a few KB chunks.
	res, err := db.Exec(`INSERT INTO tenants (name, domain) VALUES (?, ?)`, "Demo", "demo.de")
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO bots (tenant_id, name) VALUES (?, ?)`, tid, "demo-bot")
	if err != nil {
		t.Fatal(err)
	}
	bid, _ := res.LastInsertId()

	em := &fakeEmbedder{}
	for i, body := range []string{
		"Wir bieten Steuerberatung für KMU.",
		"Unser Team kümmert sich um Lohnbuchhaltung mit DATEV.",
		"Jahresabschlüsse nach HGB sind Standardleistung.",
	} {
		vecs, _ := em.Embed(context.Background(), []string{body})
		if _, err := storage.InsertChunk(context.Background(), db, storage.Chunk{
			BotID: bid, Source: "demo.de/about", ChunkIndex: i, Content: body,
		}, vecs[0]); err != nil {
			t.Fatal(err)
		}
	}

	chat := &fakeChat{answer: "Du bist der digitale Assistent der Demo-Kanzlei. …"}
	g, err := New(db, chat, em)
	if err != nil {
		t.Fatal(err)
	}

	got, err := g.Draft(context.Background(), Bot{ID: bid, Name: "Demo-Kanzlei", Tone: "formal", Industry: "Steuerberatung"})
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if !strings.Contains(got, "Du bist") {
		t.Errorf("got: %q", got)
	}

	// The meta-prompt must include the seeded company name + a chunk excerpt.
	body := chat.reqSeen.Messages[0].Content
	if !strings.Contains(body, "Demo-Kanzlei") {
		t.Errorf("meta-prompt missing CompanyName: %s", body)
	}
	if !strings.Contains(body, "Steuerberatung") {
		t.Errorf("meta-prompt missing Industry/seeded chunk: %s", body)
	}
}

func TestDraft_NoChunksFallsBackToGenericInstruction(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	res, err := db.Exec(`INSERT INTO tenants (name, domain) VALUES (?, ?)`, "Demo", "demo2.de")
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO bots (tenant_id, name) VALUES (?, ?)`, tid, "empty-bot")
	if err != nil {
		t.Fatal(err)
	}
	bid, _ := res.LastInsertId()

	chat := &fakeChat{answer: "Generischer Prompt."}
	g, err := New(db, chat, &fakeEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Draft(context.Background(), Bot{ID: bid, Name: "X"}); err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if !strings.Contains(chat.reqSeen.Messages[0].Content, "Noch keine Inhalte gecrawlt") {
		t.Error("expected generic-instruction fallback in meta-prompt")
	}
}

func TestDraft_EmbedError(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	g, err := New(db, &fakeChat{}, &fakeEmbedder{err: errors.New("rate limited")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Draft(context.Background(), Bot{ID: 1}); err == nil {
		t.Fatal("expected embed error to propagate")
	}
}

func TestDraft_LLMError(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	g, err := New(db, &fakeChat{err: errors.New("503")}, &fakeEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Draft(context.Background(), Bot{ID: 1, Name: "X"}); err == nil {
		t.Fatal("expected LLM error to propagate")
	}
}

func TestDraft_RejectsZeroBotID(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	g, err := New(db, &fakeChat{}, &fakeEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Draft(context.Background(), Bot{}); err == nil {
		t.Fatal("expected error for bot.ID == 0")
	}
}
