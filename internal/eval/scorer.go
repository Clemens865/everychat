package eval

import "context"

// Scorer decides whether a bot's answer satisfies a golden question.
// Phase 2 ships a keyword scorer; Phase 4 adds an LLM-as-judge scorer.
// Both implement the same contract so eval.Runner dispatches without
// forking the per-question loop.
//
// Reasons should be empty on Pass=true and list failure causes
// otherwise. The scorer is responsible for being safe under concurrent
// use; runner currently calls scorers serially but a future fan-out
// is on the roadmap.
type Scorer interface {
	Score(ctx context.Context, q Question, answer string) (pass bool, reasons []string)
}

// KeywordScorer is the Phase-2 implementation: must_contain ⊂ answer
// AND must_not_contain ⊄ answer (case-insensitive). Cheap and
// deterministic; right for sharply-worded compliance questions where
// a specific term must appear.
type KeywordScorer struct{}

// Score implements Scorer for keyword matching. Reuses the original
// scoreAnswer helper so the rule semantics stay single-sourced.
func (KeywordScorer) Score(_ context.Context, q Question, answer string) (bool, []string) {
	return scoreAnswer(q, answer)
}
