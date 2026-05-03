package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/clemenshoenig/everychat/internal/llm"
)

// JudgeModelAlias is the LiteLLM model name used for LLM-as-judge.
// Constant so the LiteLLM config and the Go code stay in sync.
const JudgeModelAlias = "claude-haiku"

// JudgeScorer asks an LLM whether the bot's answer satisfies the
// golden question. Picks up where keyword scoring stops — corpus
// questions with `must_contain: ["Steuerberatung"]` shouldn't fail
// when the bot answers with "wir beraten Sie steuerlich" even though
// the literal string is missing.
//
// The judge prompt is structured to return ONLY a JSON object with
// pass + reason. Robustness (fenced blocks, preamble) is handled by
// the same balanced-brace scanner widget.Suggester uses.
type JudgeScorer struct {
	chat llm.Chat
}

// NewJudgeScorer wires a JudgeScorer to its LLM client.
func NewJudgeScorer(chat llm.Chat) *JudgeScorer { return &JudgeScorer{chat: chat} }

// Score implements Scorer. Returns pass + reasons. On LLM error,
// returns fail with the error in reasons — same shape the keyword
// scorer uses for missing-keyword cases so the runner doesn't have
// to special-case errors here.
func (j *JudgeScorer) Score(ctx context.Context, q Question, answer string) (bool, []string) {
	if strings.TrimSpace(answer) == "" {
		return false, []string{"empty answer"}
	}

	prompt := buildJudgePrompt(q, answer)
	ch, err := j.chat.Stream(ctx, llm.ChatRequest{
		Model:       JudgeModelAlias,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		MaxTokens:   200,
		Temperature: 0.0, // deterministic: same Q+A should always yield the same verdict
	})
	if err != nil {
		return false, []string{"judge error: " + err.Error()}
	}

	var raw strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return false, []string{"judge stream error: " + chunk.Err.Error()}
		}
		raw.WriteString(chunk.Delta)
	}

	jsonText := extractJSONObject(raw.String())
	if jsonText == "" {
		return false, []string{"judge: no JSON object in response: " + truncForError(raw.String())}
	}
	var parsed struct {
		Pass   bool   `json:"pass"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return false, []string{"judge: parse JSON: " + err.Error()}
	}

	if parsed.Pass {
		return true, nil
	}
	reason := strings.TrimSpace(parsed.Reason)
	if reason == "" {
		reason = "judge: failed without reason"
	}
	return false, []string{reason}
}

// buildJudgePrompt renders the structured prompt the judge LLM evaluates.
// Deliberately short + concrete to keep the haiku-rate cost down.
func buildJudgePrompt(q Question, answer string) string {
	var b strings.Builder
	b.WriteString("Du bist ein strenger aber fairer Prüfer für deutsche Kundenservice-Antworten. ")
	b.WriteString("Beantworte AUSSCHLIESSLICH mit einem JSON-Objekt der Form: ")
	b.WriteString(`{"pass": true|false, "reason": "<≤120 Zeichen>"}`)
	b.WriteString("\n\n")
	b.WriteString("Frage des Besuchers:\n")
	b.WriteString(q.Ask)
	b.WriteString("\n\n")
	if len(q.MustContain) > 0 {
		b.WriteString("Erwartete Themen / Fakten (Hinweis, kein wörtlicher Match nötig):\n- ")
		b.WriteString(strings.Join(q.MustContain, "\n- "))
		b.WriteString("\n\n")
	}
	if len(q.MustNotContain) > 0 {
		b.WriteString("Was NICHT vorkommen darf:\n- ")
		b.WriteString(strings.Join(q.MustNotContain, "\n- "))
		b.WriteString("\n\n")
	}
	b.WriteString("Antwort des Bots:\n")
	b.WriteString(answer)
	b.WriteString("\n\n")
	b.WriteString("Bewertung: pass=true wenn die Antwort die Frage inhaltlich befriedigend beantwortet ")
	b.WriteString("(Paraphrasen erlaubt, alle Verbote eingehalten). pass=false sonst — `reason` knapp + konkret.\n")
	b.WriteString("Gib nur das JSON zurück.")
	return b.String()
}

// extractJSONObject is the same balanced-brace scanner widget.Suggester
// uses. Duplicated here to keep eval free of widget imports.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// truncForError caps the length of an LLM response when surfacing it
// in an error message — the audit log shouldn't carry the full payload
// if the judge went off-script.
func truncForError(s string) string {
	if len(s) <= 200 {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q…", s[:200])
}
