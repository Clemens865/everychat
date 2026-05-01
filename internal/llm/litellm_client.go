package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Default model names registered in internal/llm/litellm/config.yaml.
const (
	DefaultChatModel  = "claude-sonnet"
	DefaultEmbedModel = "embed"
)

// LiteLLMClient implements Chat and Embedder by talking to a LiteLLM proxy
// over its OpenAI-compatible HTTP API. The proxy is a sibling Docker
// service in compose; for local `make dev` runs, point at localhost.
type LiteLLMClient struct {
	baseURL    string
	httpClient *http.Client
	chatModel  string
	embedModel string
}

// LiteLLMOption configures a LiteLLMClient.
type LiteLLMOption func(*LiteLLMClient)

// WithHTTPClient overrides the default *http.Client (useful in tests).
func WithHTTPClient(c *http.Client) LiteLLMOption {
	return func(l *LiteLLMClient) { l.httpClient = c }
}

// WithChatModel overrides the registered chat model name.
func WithChatModel(name string) LiteLLMOption {
	return func(l *LiteLLMClient) { l.chatModel = name }
}

// WithEmbedModel overrides the registered embedding model name.
func WithEmbedModel(name string) LiteLLMOption {
	return func(l *LiteLLMClient) { l.embedModel = name }
}

// NewLiteLLMClient constructs a client targeting the given base URL
// (e.g. "http://litellm:4000" inside compose, "http://127.0.0.1:4000"
// for `make dev`).
func NewLiteLLMClient(baseURL string, opts ...LiteLLMOption) *LiteLLMClient {
	c := &LiteLLMClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 0, // streaming — let context drive cancellation
		},
		chatModel:  DefaultChatModel,
		embedModel: DefaultEmbedModel,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Stream implements Chat.Stream. The returned channel is closed after the
// final chunk (Done=true or Err!=nil) is delivered.
func (c *LiteLLMClient) Stream(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	model := req.Model
	if model == "" {
		model = c.chatModel
	}
	body, err := buildChatBody(model, req)
	if err != nil {
		return nil, fmt.Errorf("llm: build body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: do request: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return nil, fmt.Errorf("llm: chat status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	out := make(chan ChatChunk, 16)
	go parseChatStream(resp, out)
	return out, nil
}

// Embed implements Embedder.Embed in a single batched request.
func (c *LiteLLMClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	payload := map[string]any{
		"model": c.embedModel,
		"input": inputs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llm embed: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm embed: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm embed: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("llm embed: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llm embed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("llm embed: decode: %w", err)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("llm embed: got %d vectors for %d inputs", len(parsed.Data), len(inputs))
	}
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// buildChatBody marshals a ChatRequest into the OpenAI-compatible payload
// LiteLLM proxies to the underlying provider.
func buildChatBody(model string, req ChatRequest) ([]byte, error) {
	type apiMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	msgs := make([]apiMsg, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		msgs = append(msgs, apiMsg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, apiMsg{Role: string(m.Role), Content: m.Content})
	}
	payload := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	// Ask LiteLLM to include usage in the final stream chunk.
	payload["stream_options"] = map[string]any{"include_usage": true}
	return json.Marshal(payload)
}

// parseChatStream drains an SSE response body and pushes chunks. Closes the
// response body and the output channel before returning.
func parseChatStream(resp *http.Response, out chan<- ChatChunk) {
	defer close(out)
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			out <- ChatChunk{Done: true}
			return
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			out <- ChatChunk{Err: fmt.Errorf("llm stream decode: %w", err)}
			return
		}
		var usage *Usage
		if event.Usage != nil {
			usage = &Usage{
				InputTokens:  event.Usage.PromptTokens,
				OutputTokens: event.Usage.CompletionTokens,
			}
		}
		for _, ch := range event.Choices {
			if ch.Delta.Content != "" {
				out <- ChatChunk{Delta: ch.Delta.Content, Usage: usage}
			}
			if ch.FinishReason != nil {
				out <- ChatChunk{Done: true, Usage: usage}
				return
			}
		}
		if usage != nil && len(event.Choices) == 0 {
			// usage-only frame (LiteLLM can emit one before [DONE])
			out <- ChatChunk{Usage: usage}
		}
	}
	if err := scanner.Err(); err != nil {
		// context.Canceled bubbles up here as an HTTP body read error.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			out <- ChatChunk{Err: err}
			return
		}
		out <- ChatChunk{Err: fmt.Errorf("llm stream read: %w", err)}
		return
	}
	// Stream ended without an explicit [DONE] — treat as graceful done.
	out <- ChatChunk{Done: true}
}
