// Package llm is the Everychat LLM gateway. All chat completion and
// embedding calls in the binary go through one of the interfaces in this
// file. The default implementation talks to a LiteLLM Python sidecar via
// its OpenAI-compatible HTTP API; future providers (Mistral, local Llama,
// etc.) can be added in litellm/config.yaml without touching Go.
package llm

import "context"

// Role is the speaker of a single chat turn. Mirrors OpenAI/Anthropic chat
// schemas — system / user / assistant / tool.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a chat. The system prompt is passed via
// ChatRequest.System rather than as a Message — keeps the LiteLLM payload
// simple and matches the way OpenAI clients usually hand things.
type Message struct {
	Role    Role
	Content string
}

// ChatRequest is the input to Chat.Stream. Model is the LiteLLM "model_name"
// (e.g. "claude-sonnet"); leave empty to use the client's default.
type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int
	Temperature float64
}

// ChatChunk is a single event off the stream.
//
//   - Delta is the next token slice (empty when Done is true)
//   - Done signals the stream completed cleanly. When Done is true, Usage
//     may be populated with the final token counts (provider-dependent).
//   - Err is non-nil only on transport / decode failures. The channel is
//     closed after the error is delivered.
type ChatChunk struct {
	Delta string
	Done  bool
	Usage *Usage
	Err   error
}

// Usage holds the token counts the provider returned at stream end.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Chat streams chat-completion responses.
type Chat interface {
	Stream(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
}

// Embedder turns text into vectors. Implementations must return one vector
// per input, in the same order. All vectors share the same dimensionality
// (locked to text-embedding-3-small's 1536 in Phase 2).
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}
