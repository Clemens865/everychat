package corpus

import (
	"errors"
	"testing"

	"github.com/clemenshoenig/everychat/internal/eval"
)

func TestTaxonomy_Valid(t *testing.T) {
	cases := map[string]bool{
		"steuerberater":  true,
		"handwerk":       true,
		"saas-b2b":       true,
		"":               false,
		"  ":             false,
		"unknown":        false,
		"STEUERBERATER":  true, // Valid is case-insensitive
		"steuerberatung": false,
	}
	for in, want := range cases {
		if got := Valid(in); got != want {
			t.Errorf("Valid(%q): got %v, want %v", in, got, want)
		}
	}
}

func TestIndustries_StableOrder(t *testing.T) {
	all := Industries()
	if len(all) != 6 {
		t.Fatalf("expected 6 industries, got %d", len(all))
	}
	if all[0] != Steuerberater {
		t.Errorf("first industry must be Steuerberater (anchor for tests), got %q", all[0])
	}
}

func TestLoad_AllPlaceholdersReturnEmpty(t *testing.T) {
	for _, ind := range Industries() {
		bundle, err := Load(ind)
		if err != nil && !errors.Is(err, ErrEmpty) {
			t.Errorf("%s: unexpected error %v", ind, err)
			continue
		}
		// Empty placeholders are legitimate state for non-seeded
		// industries — Sprint 6 fills steuerberater + handwerk.
		if bundle == nil {
			t.Errorf("%s: nil bundle", ind)
			continue
		}
		if !bundle.IsEmpty() && (errors.Is(err, ErrEmpty)) {
			t.Errorf("%s: bundle reports IsEmpty but err is ErrEmpty (inconsistent)", ind)
		}
	}
}

func TestLoad_RejectsUnknownIndustry(t *testing.T) {
	if _, err := Load(Industry("nope")); !errors.Is(err, ErrUnknownIndustry) {
		t.Fatalf("expected ErrUnknownIndustry, got %v", err)
	}
}

// The hold-out-overlap defender. Sprint 1 ships placeholder content,
// so we synthesize the overlap scenario by exercising the helper directly.
func TestAssertNoHoldoutOverlap_DetectsCollision(t *testing.T) {
	q := []eval.Question{{ID: "q1", Ask: "?"}, {ID: "q2", Ask: "?"}}
	h := []eval.Question{{ID: "q3", Ask: "?"}, {ID: "q1", Ask: "?"}} // q1 overlaps
	if err := assertNoHoldoutOverlap(q, h); !errors.Is(err, ErrHoldoutOverlap) {
		t.Fatalf("expected ErrHoldoutOverlap, got %v", err)
	}
}

func TestAssertNoHoldoutOverlap_PassesOnDisjoint(t *testing.T) {
	q := []eval.Question{{ID: "q1", Ask: "?"}}
	h := []eval.Question{{ID: "q2", Ask: "?"}}
	if err := assertNoHoldoutOverlap(q, h); err != nil {
		t.Fatalf("disjoint sets should not error: %v", err)
	}
}
