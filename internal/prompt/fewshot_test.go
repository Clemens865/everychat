package prompt

import (
	"strings"
	"testing"

	"github.com/clemenshoenig/everychat/internal/corpus"
)

func TestBlend_AppendsExemplars(t *testing.T) {
	base := "Du bist der Assistent."
	ex := []corpus.QAPair{
		{Q: "Was kostet Lohnbuchhaltung?", A: "Pro Mitarbeiter pauschal."},
		{Q: "Bietet ihr Erstgespräche?", A: "Ja, kostenlos."},
	}
	got := Blend(base, ex, DefaultMaxExemplars)
	if !strings.Contains(got, "# Beispiele") {
		t.Errorf("missing header: %s", got)
	}
	if !strings.Contains(got, "Frage: Was kostet Lohnbuchhaltung?") {
		t.Errorf("missing Q1: %s", got)
	}
	if !strings.Contains(got, "Antwort: Ja, kostenlos.") {
		t.Errorf("missing A2: %s", got)
	}
	if !strings.HasPrefix(got, base) {
		t.Errorf("base prompt not preserved at top")
	}
}

func TestBlend_RespectsMax(t *testing.T) {
	base := "system."
	ex := []corpus.QAPair{
		{Q: "Q1", A: "A1"},
		{Q: "Q2", A: "A2"},
		{Q: "Q3", A: "A3"},
		{Q: "Q4", A: "A4"},
		{Q: "Q5", A: "A5"},
	}
	got := Blend(base, ex, 2)
	if strings.Count(got, "Frage: ") != 2 {
		t.Errorf("expected 2 Frage entries, got\n%s", got)
	}
	if strings.Contains(got, "Q3") {
		t.Errorf("Q3 should be capped out: %s", got)
	}
}

func TestBlend_DefaultMaxAppliesWhenZero(t *testing.T) {
	base := "system."
	ex := make([]corpus.QAPair, 10)
	for i := range ex {
		ex[i] = corpus.QAPair{Q: "Q", A: "A"}
	}
	got := Blend(base, ex, 0)
	if strings.Count(got, "Frage: ") != DefaultMaxExemplars {
		t.Errorf("default max not applied: count=%d", strings.Count(got, "Frage: "))
	}
}

func TestBlend_NoOpWhenEmptyInput(t *testing.T) {
	if got := Blend("", []corpus.QAPair{{Q: "x", A: "y"}}, 4); got != "" {
		t.Errorf("empty system prompt should pass through, got %q", got)
	}
	if got := Blend("system.", nil, 4); got != "system." {
		t.Errorf("nil exemplars should pass through, got %q", got)
	}
	if got := Blend("system.", []corpus.QAPair{}, 4); got != "system." {
		t.Errorf("empty exemplars should pass through, got %q", got)
	}
}

func TestBlend_SkipsBlankPairs(t *testing.T) {
	got := Blend("system.", []corpus.QAPair{
		{Q: "", A: "lonely answer"},
		{Q: "lonely question", A: ""},
		{Q: "Q1", A: "A1"},
	}, 4)
	if strings.Count(got, "Frage: ") != 1 {
		t.Errorf("expected only one valid pair, got\n%s", got)
	}
}

func TestBlend_AllBlankReturnsOriginalUntouched(t *testing.T) {
	base := "system."
	got := Blend(base, []corpus.QAPair{{Q: "", A: ""}, {Q: " ", A: " "}}, 4)
	if got != base {
		t.Errorf("all-blank exemplars should leave prompt untouched, got %q", got)
	}
}

func TestBlend_TruncatesOverlongValues(t *testing.T) {
	longQ := strings.Repeat("ä", 500)
	longA := strings.Repeat("ö", 1200)
	got := Blend("system.", []corpus.QAPair{{Q: longQ, A: longA}}, 1)
	// The output must not contain the full long strings.
	if strings.Contains(got, longQ) {
		t.Error("question not truncated")
	}
	if strings.Contains(got, longA) {
		t.Error("answer not truncated")
	}
}
