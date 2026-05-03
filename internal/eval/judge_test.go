package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/llm"
)

// stubChat returns a canned response. Mirrors the pattern used in
// internal/widget/suggester_test.go so the test scaffolding stays
// recognisable across packages.
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

func TestJudge_PassVerdict(t *testing.T) {
	chat := &stubChat{answer: `{"pass": true, "reason": ""}`}
	j := NewJudgeScorer(chat)
	pass, reasons := j.Score(context.Background(),
		Question{ID: "q1", Ask: "Welche Leistungen?", MustContain: []string{"Steuerberatung"}},
		"Wir beraten Sie steuerlich umfassend.",
	)
	if !pass {
		t.Fatalf("expected pass, got reasons=%v", reasons)
	}
	if len(reasons) != 0 {
		t.Errorf("pass should have no reasons, got %v", reasons)
	}

	// The judge prompt must include both the user question and the
	// must-contain hints so the model has the context it needs.
	body := chat.saw.Messages[0].Content
	if !strings.Contains(body, "Welche Leistungen?") {
		t.Errorf("prompt missing question: %s", body)
	}
	if !strings.Contains(body, "Steuerberatung") {
		t.Errorf("prompt missing must_contain hint: %s", body)
	}
	// And targets the haiku alias so the LiteLLM router charges the
	// cheaper rate.
	if chat.saw.Model != JudgeModelAlias {
		t.Errorf("model: got %q, want %q", chat.saw.Model, JudgeModelAlias)
	}
}

func TestJudge_FailVerdictWithReason(t *testing.T) {
	chat := &stubChat{answer: `{"pass": false, "reason": "Antwort weicht zur Rechtsberatung aus"}`}
	j := NewJudgeScorer(chat)
	pass, reasons := j.Score(context.Background(),
		Question{ID: "q1", Ask: "Welche Leistungen?"},
		"Bitte wenden Sie sich an einen Anwalt.",
	)
	if pass {
		t.Fatal("expected fail")
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "Rechtsberatung") {
		t.Errorf("reasons: %v", reasons)
	}
}

func TestJudge_TolerantOfFencedJSON(t *testing.T) {
	chat := &stubChat{answer: "```json\n" + `{"pass": true, "reason": ""}` + "\n```"}
	j := NewJudgeScorer(chat)
	pass, _ := j.Score(context.Background(), Question{Ask: "?"}, "answer")
	if !pass {
		t.Fatal("expected pass through fenced block")
	}
}

func TestJudge_MalformedJSONFails(t *testing.T) {
	chat := &stubChat{answer: "no json here at all"}
	j := NewJudgeScorer(chat)
	pass, reasons := j.Score(context.Background(), Question{Ask: "?"}, "answer")
	if pass {
		t.Fatal("malformed response should not pass")
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "no JSON object") {
		t.Errorf("reasons: %v", reasons)
	}
}

func TestJudge_LLMErrorSurfacesAsFail(t *testing.T) {
	chat := &stubChat{err: errors.New("503")}
	j := NewJudgeScorer(chat)
	pass, reasons := j.Score(context.Background(), Question{Ask: "?"}, "answer")
	if pass {
		t.Fatal("LLM error should fail the question")
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "judge error") {
		t.Errorf("reasons: %v", reasons)
	}
}

func TestJudge_EmptyAnswerFails(t *testing.T) {
	chat := &stubChat{answer: `{"pass": true}`}
	j := NewJudgeScorer(chat)
	pass, reasons := j.Score(context.Background(), Question{Ask: "?"}, "")
	if pass {
		t.Fatal("empty answer should never pass — short-circuits before LLM")
	}
	if len(reasons) == 0 {
		t.Error("expected explicit empty-answer reason")
	}
	// And we never even called the LLM in that case.
	if chat.saw.Model != "" {
		t.Errorf("LLM should not have been called for empty answer; saw model=%q", chat.saw.Model)
	}
}

func TestRunner_DispatchesByEvalMode(t *testing.T) {
	// Two bots, same question file — keyword bot gets keyword scoring,
	// judge bot routes through the LLM. The test asserts dispatch by
	// observing whether the LLM was called.
	keywordChat := &stubChat{answer: ""} // chat for the bot's *answer*, not for the judge
	r := &Runner{
		keyword: KeywordScorer{},
		judge:   NewJudgeScorer(keywordChat),
	}

	if got := r.pickScorer("keyword"); got == nil {
		t.Error("pickScorer(keyword) returned nil")
	}
	if _, ok := r.pickScorer("keyword").(KeywordScorer); !ok {
		t.Error("pickScorer(keyword) should return KeywordScorer")
	}
	if _, ok := r.pickScorer("llm_judge").(*JudgeScorer); !ok {
		t.Error("pickScorer(llm_judge) should return *JudgeScorer")
	}
	// Empty + unknown both fall back to keyword (safe default).
	if _, ok := r.pickScorer("").(KeywordScorer); !ok {
		t.Error("empty mode should fall back to keyword")
	}
	if _, ok := r.pickScorer("nonsense").(KeywordScorer); !ok {
		t.Error("unknown mode should fall back to keyword")
	}
}
