package adversary

import "github.com/clemenshoenig/everychat/internal/llm"

// PriceTable is the per-1k-token rate (in cents, EUR) used by the
// adversary CostGate. The numbers are conservative approximations of
// Claude Sonnet pricing, expressed in EUR-cents so the storage column
// (max_cost_cents) is the same unit. We round UP rather than down —
// the gate must never underestimate spend.
//
// If LiteLLM is configured to route the adversary chat to a different
// model, the cost gate will overcount; that's safer than undercounting.
// A future iteration can pull rates from litellm/config.yaml.
const (
	InputCentsPer1k  = 0.30 // ~$3/MTok input × 1.10 EUR/USD
	OutputCentsPer1k = 1.50 // ~$15/MTok output × 1.10 EUR/USD
)

// CostGate accumulates token usage in cents and reports when the
// configured cap has been reached. Cents are tracked as int64 to keep
// arithmetic exact at the storage boundary; conversion from float
// rounds up via ceilDivCents.
type CostGate struct {
	MaxCents    int64
	accumulated int64
}

// NewCostGate constructs a gate. MaxCents <= 0 means "no cap" — the
// gate never trips. Useful for tests; callers that pass a real value
// should validate it at the CLI/HTTP boundary.
func NewCostGate(maxCents int64) *CostGate {
	return &CostGate{MaxCents: maxCents}
}

// Charge adds usage to the running total and reports whether the cap
// has been exceeded. The first call that crosses the cap returns
// exceeded=true; subsequent calls also return true (idempotent).
func (g *CostGate) Charge(u llm.Usage) (cents int64, exceeded bool) {
	cents = costInCents(u)
	g.accumulated += cents
	if g.MaxCents > 0 && g.accumulated >= g.MaxCents {
		return cents, true
	}
	return cents, false
}

// AccumulatedCents returns the running total. Persisted to
// adversary_runs.total_cost_cents at run end.
func (g *CostGate) AccumulatedCents() int64 { return g.accumulated }

// costInCents converts a Usage struct to integer EUR-cents, rounding
// up so the gate is never optimistic about a half-cent.
func costInCents(u llm.Usage) int64 {
	in := float64(u.InputTokens) * InputCentsPer1k / 1000.0
	out := float64(u.OutputTokens) * OutputCentsPer1k / 1000.0
	total := in + out
	if total <= 0 {
		return 0
	}
	// Round up to the next whole cent.
	cents := int64(total)
	if total > float64(cents) {
		cents++
	}
	return cents
}
