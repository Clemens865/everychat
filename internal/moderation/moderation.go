// Package moderation is the interface boundary for input + output
// safety checks on visitor chat traffic.
//
// Phase 3 ships this as a contract with a Noop default impl that
// always allows. The PRD calls for Llama Guard 3 8B (CPU-quant GGUF
// via LiteLLM) — the real impl needs the GGUF model checked in
// alongside the LiteLLM config, which is a Phase-5 ops concern. The
// boundary is honest now so that swap is a one-line wiring change in
// cmd/everychat/main.go and zero changes elsewhere.
package moderation

import "context"

// Role is the speaker whose content is being checked. Llama Guard
// scores user / assistant messages differently — its prompt template
// expects to know which side it's evaluating.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Verdict is what a Moderator returns for one piece of content.
type Verdict struct {
	Allowed bool
	// Category is the moderation label (e.g. "S1: violent_crimes",
	// "S5: defamation"). Empty when Allowed=true. The category is
	// surfaced to admins via the audit_log; visitors only see a
	// generic refusal.
	Category string
	// Reason is the short human-readable explanation suitable for the
	// audit_log payload column. Phase 3 keeps it terse.
	Reason string
}

// Moderator decides whether a single piece of content is safe to
// process / display.
type Moderator interface {
	Check(ctx context.Context, role Role, content string) (Verdict, error)
}

// NoopModerator allows every input. It's the Phase 3 default; switch
// to LlamaGuard in Phase 5.
type NoopModerator struct{}

// Check implements Moderator.
func (NoopModerator) Check(_ context.Context, _ Role, _ string) (Verdict, error) {
	return Verdict{Allowed: true}, nil
}

// New returns the Phase-3 default implementation. The selection is
// centralized here so callers don't need to know about specific
// implementations — Phase 5's LlamaGuardModerator drops in by
// changing this single line.
func New() Moderator { return NoopModerator{} }
