package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/clemenshoenig/everychat/internal/adversary"
	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/prompt"
	"github.com/clemenshoenig/everychat/internal/storage"
	"github.com/clemenshoenig/everychat/internal/widget"
)

// stubLiteLLM mocks the chat + embed surface of LiteLLM. The
// returned chat reply contains "ABLEHNUNG" so the adversary judge
// gets a deterministic verdict to extract from.
func stubLiteLLM(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			vec := make([]float32, storage.EmbedDims)
			for i := range vec {
				vec[i] = 0.001
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": vec}},
			})
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"PASS — Bot hat ABLEHNUNG signalisiert."}}]}` + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
			_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
}

// newTestServer builds a fully-wired *Server with an in-memory DB and
// a stubbed LiteLLM, plus the seeded demo bot's id. Auth is *not*
// wired — tests call exported and unexported handlers directly so
// session middleware doesn't need to be stubbed.
func newTestServer(t *testing.T) (*Server, int64, func()) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	bots, err := storage.ListBots(context.Background(), db)
	if err != nil || len(bots) == 0 {
		_ = db.Close()
		t.Fatalf("seeded bot missing: %v", err)
	}
	botID := bots[0].ID

	upstream := stubLiteLLM(t)
	llmClient := llm.NewLiteLLMClient(upstream.URL)

	gen, err := prompt.New(db, llmClient, llmClient)
	if err != nil {
		_ = db.Close()
		upstream.Close()
		t.Fatal(err)
	}
	suggester, err := widget.NewSuggester(db, llmClient, llmClient)
	if err != nil {
		_ = db.Close()
		upstream.Close()
		t.Fatal(err)
	}

	srv, err := New(db, &auth.MagicLinks{}, &auth.Sessions{}, gen, suggester, llmClient)
	if err != nil {
		_ = db.Close()
		upstream.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		upstream.Close()
		_ = db.Close()
	}
	t.Cleanup(cleanup)
	return srv, botID, cleanup
}

func TestAdversaryModalPartial_ListsAllPersonas(t *testing.T) {
	srv, botID, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.adversaryModalPartial(rec, httptest.NewRequest(http.MethodGet, "/admin/bots/1/adversary", nil), botID)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%q", rec.Code, body)
	}
	for _, want := range []string{"datenexfiltration", "off-topic-drift", "jailbreak-klassiker"} {
		if !strings.Contains(body, want) {
			t.Errorf("modal missing persona %q\n%s", want, body)
		}
	}
	if !strings.Contains(body, "Adversary-Lauf starten") {
		t.Errorf("modal missing submit button")
	}
}

func TestAdversaryStart_KicksOffRun_PersistsRow(t *testing.T) {
	srv, botID, _ := newTestServer(t)

	form := url.Values{
		"persona":      []string{"datenexfiltration"},
		"turns":        []string{"2"},
		"max_cost_eur": []string{"0.10"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/bots/1/adversary", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.adversaryStart(rec, req, botID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Adversary-Lauf läuft") {
		t.Errorf("response missing polling stub:\n%s", body)
	}

	// The detached goroutine should land a row within ~3s.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := storage.LatestAdversaryRunForBot(context.Background(), srv.db, botID)
		if err == nil {
			if run.Persona != "datenexfiltration" {
				t.Errorf("persona: got %q, want datenexfiltration", run.Persona)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("adversary run row never appeared in the DB")
}

func TestAdversaryLatest_NoRunYet_RendersWaiting(t *testing.T) {
	srv, botID, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.adversaryLatest(rec, httptest.NewRequest(http.MethodGet, "/admin/bots/1/adversary/latest", nil), botID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wird vorbereitet") {
		t.Errorf("expected waiting state, got:\n%s", body)
	}
}

func TestTestPaneRun_ReturnsAnswerHTML(t *testing.T) {
	srv, botID, _ := newTestServer(t)

	form := url.Values{"message": []string{"Was bietet ihr an?"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/bots/1/test-pane/run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.testPaneRun(rec, req, botID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PASS — Bot hat ABLEHNUNG signalisiert.") {
		t.Errorf("answer missing from response:\n%s", body)
	}
	if !strings.Contains(body, "Was bietet ihr an?") {
		t.Errorf("question echo missing from response:\n%s", body)
	}
}

func TestTestPanePartial_ReturnsForm(t *testing.T) {
	srv, botID, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.testPanePartial(rec, httptest.NewRequest(http.MethodGet, "/admin/bots/1/test-pane", nil), botID)
	body := rec.Body.String()
	if !strings.Contains(body, `name="message"`) {
		t.Errorf("partial missing message textarea:\n%s", body)
	}
}

// silence unused import warnings if a future refactor drops uses.
var (
	_ = io.Discard
	_ = adversary.StatusRunning
)
