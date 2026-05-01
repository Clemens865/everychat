package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStream_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		assertChatPayload(t, r, "claude-sonnet")
		writeSSE(w,
			`{"choices":[{"delta":{"content":"Hallo"}}]}`,
			`{"choices":[{"delta":{"content":" Welt"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
			"[DONE]",
		)
	}))
	defer srv.Close()

	c := NewLiteLLMClient(srv.URL)
	ch, err := c.Stream(context.Background(), ChatRequest{
		System:   "Be helpful.",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got, usage := drain(ch)
	if got != "Hallo Welt" {
		t.Errorf("got %q, want %q", got, "Hallo Welt")
	}
	if usage == nil || usage.InputTokens != 12 || usage.OutputTokens != 3 {
		t.Errorf("usage: %+v", usage)
	}
}

func TestStream_NonStreamingDone(t *testing.T) {
	// Some providers may close the stream without an explicit [DONE]. We
	// should still report Done=true so callers don't hang.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"x"}}]}`)
	}))
	defer srv.Close()

	c := NewLiteLLMClient(srv.URL)
	ch, err := c.Stream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := drain(ch)
	if got != "x" {
		t.Errorf("got %q", got)
	}
}

func TestStream_NonOKReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	c := NewLiteLLMClient(srv.URL)
	_, err := c.Stream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

func TestStream_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// First chunk, then hold the connection open.
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"start"}}]}`+"\n\n")
		flusher.Flush()
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewLiteLLMClient(srv.URL)
	ch, err := c.Stream(ctx, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	sawErr := false
	for chunk := range ch {
		if chunk.Err != nil {
			if !errors.Is(chunk.Err, context.Canceled) && !strings.Contains(chunk.Err.Error(), "context canceled") {
				t.Fatalf("expected context canceled, got %v", chunk.Err)
			}
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected a context.Canceled error chunk")
	}
}

func TestEmbed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Model != "embed" {
			t.Errorf("model %q", body.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		// Echo a tiny vector per input.
		out := `{"data":[`
		for i := range body.Input {
			if i > 0 {
				out += ","
			}
			out += fmt.Sprintf(`{"embedding":[%d.0,%d.1,%d.2]}`, i, i, i)
		}
		out += `]}`
		_, _ = io.WriteString(w, out)
	}))
	defer srv.Close()

	c := NewLiteLLMClient(srv.URL)
	got, err := c.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 || len(got[0]) != 3 || got[1][0] != 1.0 {
		t.Fatalf("unexpected vectors: %#v", got)
	}
}

func TestEmbed_EmptyInput(t *testing.T) {
	c := NewLiteLLMClient("http://unused")
	got, err := c.Embed(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", got, err)
	}
}

func TestEmbed_MismatchedCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1]}]}`)
	}))
	defer srv.Close()
	c := NewLiteLLMClient(srv.URL)
	_, err := c.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 2 inputs") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

// helpers ----

func writeSSE(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, l := range lines {
		_, _ = io.WriteString(w, "data: "+l+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func drain(ch <-chan ChatChunk) (string, *Usage) {
	var b strings.Builder
	var usage *Usage
	for c := range ch {
		if c.Err != nil {
			return b.String(), usage
		}
		b.WriteString(c.Delta)
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	return b.String(), usage
}

func assertChatPayload(t *testing.T, r *http.Request, wantModel string) {
	t.Helper()
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Model != wantModel {
		t.Errorf("model: got %q want %q", body.Model, wantModel)
	}
	if !body.Stream {
		t.Error("stream should be true")
	}
	if len(body.Messages) == 0 {
		t.Error("expected at least one message")
	}
}
