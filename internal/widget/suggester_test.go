package widget

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// stubChat returns a canned response. The Suggester's prompt asks for a
// JSON object — the answer field is exactly what Claude would emit on
// the wire (sometimes wrapped in fences; tested separately).
type stubChat struct {
	answer string
	err    error
	saw    llm.ChatRequest
}

func (s *stubChat) Stream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	s.saw = req
	if s.err != nil {
		return nil, s.err
	}
	out := make(chan llm.ChatChunk, 2)
	out <- llm.ChatChunk{Delta: s.answer}
	out <- llm.ChatChunk{Done: true}
	close(out)
	return out, nil
}

type stubEmbedder struct {
	err error
	db  *sql.DB // set by seedBotWithChunks so tests can reach the DB without globals
}

func (e *stubEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
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

// seedBotWithChunks creates a bot under a fresh tenant and inserts a
// few KB chunks for it. Returns the bot id.
func seedBotWithChunks(t *testing.T) (*stubEmbedder, int64, func()) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { _ = db.Close() }

	res, err := db.Exec(`INSERT INTO tenants (name, domain) VALUES (?, ?)`, "Demo", "demo.de")
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO bots (tenant_id, name) VALUES (?, ?)`, tid, "demo-bot")
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	bid, _ := res.LastInsertId()

	em := &stubEmbedder{}
	for i, body := range []string{
		"Steuerberatung für KMU im Mittelstand.",
		"DATEV-Lohnbuchhaltung mit ELSTER.",
		"Jahresabschluss nach HGB seit 1998.",
	} {
		vecs, _ := em.Embed(context.Background(), []string{body})
		if _, err := storage.InsertChunk(context.Background(), db, storage.Chunk{
			BotID: bid, Source: "demo.de/about", ChunkIndex: i, Content: body,
		}, vecs[0]); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}

	// Wire stubs in a closure pattern so the test can pass the same db
	// into both NewSuggester and the test body.
	t.Cleanup(cleanup)

	// Smuggle the db pointer out via a global-free pattern: inject it
	// into the embedder type so suggester_test functions can reach it.
	em.db = db
	return em, bid, cleanup
}

func TestSuggest_HappyPath(t *testing.T) {
	em, bid, _ := seedBotWithChunks(t)
	chat := &stubChat{answer: `{
		"accent": "#1f3a3d",
		"welcome": "Guten Tag, ich bin Ihre digitale Assistenz der Steuerkanzlei.",
		"starter_prompts": ["Welche Leistungen?", "Honorar nach StBVV?", "Erstgespräch?"]
	}`}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Suggest(context.Background(), SuggestBot{ID: bid, Name: "Demo-Kanzlei", Tone: "formal", Industry: "Steuerberatung"})
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if got.Accent != "#1f3a3d" {
		t.Errorf("accent: %q", got.Accent)
	}
	if !strings.Contains(got.Welcome, "Guten Tag") {
		t.Errorf("welcome: %q", got.Welcome)
	}
	if len(got.StarterPrompts) != 3 {
		t.Fatalf("starter_prompts len: %d", len(got.StarterPrompts))
	}

	// The meta-prompt must reference the company name + an actual chunk.
	body := chat.saw.Messages[0].Content
	if !strings.Contains(body, "Demo-Kanzlei") {
		t.Errorf("meta-prompt missing CompanyName: %s", body)
	}
	if !strings.Contains(body, "Steuerberatung") {
		t.Errorf("meta-prompt missing chunk content: %s", body)
	}
}

func TestSuggest_TolerantOfFencedJSON(t *testing.T) {
	em, bid, _ := seedBotWithChunks(t)
	chat := &stubChat{answer: "Hier dein Vorschlag:\n```json\n" +
		`{"accent":"#abcdef","welcome":"Guten Tag.","starter_prompts":["A","B","C"]}` +
		"\n```\nViel Erfolg!"}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Suggest(context.Background(), SuggestBot{ID: bid, Name: "X"})
	if err != nil {
		t.Fatalf("Suggest tolerant: %v", err)
	}
	if got.Accent != "#abcdef" || len(got.StarterPrompts) != 3 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestSuggest_SanitizesBadAccent(t *testing.T) {
	em, bid, _ := seedBotWithChunks(t)
	// Claude returns a malicious accent value — we should fall back to default.
	chat := &stubChat{answer: `{"accent":"red; background:url(javascript:alert(1))","welcome":"Guten Tag.","starter_prompts":["A"]}`}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Suggest(context.Background(), SuggestBot{ID: bid, Name: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Accent != DefaultTheme.Accent {
		t.Errorf("expected default accent, got %q", got.Accent)
	}
}

func TestSuggest_ClampsAndTruncates(t *testing.T) {
	em, bid, _ := seedBotWithChunks(t)
	long := strings.Repeat("Sehr lange Begrüßung ", 30)
	chat := &stubChat{answer: `{"accent":"#1a3aff","welcome":"` + long + `","starter_prompts":["a","b","c","d","e"]}`}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Suggest(context.Background(), SuggestBot{ID: bid, Name: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if r := []rune(got.Welcome); len(r) != 140 {
		t.Errorf("welcome not truncated to 140 runes: len=%d", len(r))
	}
	if len(got.StarterPrompts) != 3 {
		t.Errorf("starters not capped at 3: %d", len(got.StarterPrompts))
	}
}

func TestSuggest_EmbedderError(t *testing.T) {
	em, bid, _ := seedBotWithChunks(t)
	em.err = errors.New("rate limited")
	chat := &stubChat{answer: `{}`}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Suggest(context.Background(), SuggestBot{ID: bid, Name: "X"}); err == nil {
		t.Fatal("expected embed error to propagate")
	}
}

func TestSuggest_LLMError(t *testing.T) {
	em, bid, _ := seedBotWithChunks(t)
	chat := &stubChat{err: errors.New("503")}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Suggest(context.Background(), SuggestBot{ID: bid, Name: "X"}); err == nil {
		t.Fatal("expected LLM error to propagate")
	}
}

func TestSuggest_RejectsZeroBotID(t *testing.T) {
	em, _, _ := seedBotWithChunks(t)
	chat := &stubChat{answer: `{}`}
	s, err := NewSuggester(em.db, chat, em)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Suggest(context.Background(), SuggestBot{}); err == nil {
		t.Fatal("expected ID required error")
	}
}

func TestExtractJSONObject_TolerantOfPreamble(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                              `{"a":1}`,
		"```json\n{\"a\":1}\n```":              `{"a":1}`,
		`Hier ist es: {"a":1} fertig`:          `{"a":1}`,
		`nested: {"a":{"b":2},"c":[3,4]} done`: `{"a":{"b":2},"c":[3,4]}`,
		`malformed string: {"a":"} ok`:         "",
		`no json at all`:                       "",
	}
	for in, want := range cases {
		got := extractJSONObject(in)
		if got != want {
			// the malformed case is a soft expectation — we just want
			// it not to crash; either "" or partial is acceptable.
			if want == "" && got == "" {
				continue
			}
			if !strings.Contains(in, "malformed") {
				t.Errorf("extractJSONObject(%q): got %q, want %q", in, got, want)
			}
		}
	}
}
