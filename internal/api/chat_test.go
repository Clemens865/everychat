package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// stubLiteLLM mocks just enough of the LiteLLM HTTP API for chat + embed.
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
			_, _ = w.Write([]byte("data: " + `{"choices":[{"delta":{"content":"Hallo Welt"}}]}` + "\n\n"))
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

func newServerWithBot(t *testing.T, devMode bool, originCSV string) (*Server, string, *httptest.Server) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bots, err := storage.ListBots(context.Background(), db)
	if err != nil || len(bots) == 0 {
		t.Fatalf("seeded bot missing: %v", err)
	}
	id := bots[0].ID

	if originCSV != "" {
		if err := storage.SetEmbedOriginAllow(context.Background(), db, id, originCSV); err != nil {
			t.Fatal(err)
		}
	}
	token, err := auth.IssueEmbedToken(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}

	upstream := stubLiteLLM(t)
	t.Cleanup(upstream.Close)
	llmClient := llm.NewLiteLLMClient(upstream.URL)

	return New(db, llmClient, devMode), token, upstream
}

func TestChat_HappyPath(t *testing.T) {
	srv, token, _ := newServerWithBot(t, true, "")

	body := `{"bot_token":"` + token + `","message":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(body))
	req.Header.Set("Origin", "https://anywhere.test")
	rec := httptest.NewRecorder()
	srv.chat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: token") {
		t.Errorf("expected SSE token events; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Errorf("expected SSE done event; got %s", rec.Body.String())
	}
}

func TestChat_BadToken(t *testing.T) {
	srv, _, _ := newServerWithBot(t, true, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(`{"bot_token":"deadbeef","message":"hi"}`))
	req.Header.Set("Origin", "https://anywhere.test")
	rec := httptest.NewRecorder()
	srv.chat(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestChat_OriginRejected(t *testing.T) {
	srv, token, _ := newServerWithBot(t, false, "https://allowed.test")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(`{"bot_token":"`+token+`","message":"hi"}`))
	req.Header.Set("Origin", "https://evil.test")
	rec := httptest.NewRecorder()
	srv.chat(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestChat_OriginAllowedListed(t *testing.T) {
	srv, token, _ := newServerWithBot(t, false, "https://allowed.test, https://other.test")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(`{"bot_token":"`+token+`","message":"hi"}`))
	req.Header.Set("Origin", "https://other.test")
	rec := httptest.NewRecorder()
	srv.chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestChat_RateLimit(t *testing.T) {
	srv, token, _ := newServerWithBot(t, true, "")
	srv.rate = newRateLimiter(2, 60)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
			strings.NewReader(`{"bot_token":"`+token+`","message":"hi"}`))
		req.Header.Set("Origin", "https://anywhere.test")
		rec := httptest.NewRecorder()
		srv.chat(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("warm-up %d: status %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(`{"bot_token":"`+token+`","message":"hi"}`))
	req.Header.Set("Origin", "https://anywhere.test")
	rec := httptest.NewRecorder()
	srv.chat(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestWidgetConfig_DoesNotLeakSystemPrompt(t *testing.T) {
	srv, token, _ := newServerWithBot(t, true, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/widget/"+token+"/config", nil)
	req.Header.Set("Origin", "https://anywhere.test")
	rec := httptest.NewRecorder()
	srv.widgetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "system_prompt") || strings.Contains(strings.ToLower(body), "draft_prompt") {
		t.Fatalf("widget config leaked prompt fields: %s", body)
	}
	if !strings.Contains(body, `"bot_token"`) || !strings.Contains(body, `"template"`) {
		t.Errorf("widget config missing public fields: %s", body)
	}
}

func TestWidgetConfig_BadTokenReturns404(t *testing.T) {
	srv, _, _ := newServerWithBot(t, true, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/widget/deadbeef/config", nil)
	req.Header.Set("Origin", "https://anywhere.test")
	rec := httptest.NewRecorder()
	srv.widgetConfig(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for bad token, got %d", rec.Code)
	}
}

// TestEmbedJS — moved to internal/widget where the loader now lives.
