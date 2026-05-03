package prompt

import (
	"strings"

	"github.com/clemenshoenig/everychat/internal/corpus"
)

// DefaultMaxExemplars is the cap on how many corpus exemplars get
// blended into the system prompt. Four is the sweet spot from the
// few-shot literature: enough to anchor the model's tone + refusal
// behaviour, few enough to keep the prompt budget under control even
// with a 600-token cap on Draft completions.
const DefaultMaxExemplars = 4

// Blend appends a `# Beispiele` Q&A block to `systemPrompt`. Idempotent
// for empty inputs: returns the prompt unchanged if either argument is
// empty/nil. Respects `max` as a hard cap.
//
// The output format is deliberately plain text — no JSON, no tool-use
// schema — so it survives any downstream prompt transform a future
// model might apply. The `Frage:` / `Antwort:` markers come from the
// model's German training distribution and read naturally to Claude.
//
// Each exemplar's question + answer are rune-truncated at 320 / 800
// runes respectively to bound the worst-case prompt growth a curator
// can introduce by accident.
func Blend(systemPrompt string, exemplars []corpus.QAPair, max int) string {
	if strings.TrimSpace(systemPrompt) == "" || len(exemplars) == 0 {
		return systemPrompt
	}
	if max <= 0 {
		max = DefaultMaxExemplars
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(systemPrompt, "\n"))
	b.WriteString("\n\n# Beispiele\n\n")

	used := 0
	for _, ex := range exemplars {
		q := strings.TrimSpace(ex.Q)
		a := strings.TrimSpace(ex.A)
		if q == "" || a == "" {
			continue
		}
		b.WriteString("Frage: ")
		b.WriteString(truncRunes(q, 320))
		b.WriteString("\nAntwort: ")
		b.WriteString(truncRunes(a, 800))
		b.WriteString("\n\n")
		used++
		if used >= max {
			break
		}
	}
	if used == 0 {
		// All exemplars were blank — return the original prompt
		// untouched rather than emitting a header with no body.
		return systemPrompt
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncRunes is the same boundary-safe truncator the suggester uses;
// duplicated here to keep the prompt package free of widget imports.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
