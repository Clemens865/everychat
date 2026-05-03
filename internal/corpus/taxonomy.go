// Package corpus owns the DACH-Goldfragen-Korpus: industry-tagged
// YAML bundles (questions + exemplars + holdout) that seed bot
// authoring during the new-bot wizard. The corpus content itself
// lives at the repo root under corpus/de/<industry>/, embedded into
// the binary via go:embed so a single binary deploy carries the moat.
//
// The taxonomy is locked: adding an industry is a corpus-curation PR
// (new directory + content), not a runtime config flag. This keeps
// the moat inspectable + versioned.
package corpus

import "strings"

// Industry is the taxonomy enum. Values are stable identifiers used
// in directory paths (corpus/de/<industry>/) and in bots.industry.
type Industry string

const (
	Steuerberater  Industry = "steuerberater"
	Handwerk       Industry = "handwerk"
	Anwaltskanzlei Industry = "anwaltskanzlei"
	Maschinenbau   Industry = "maschinenbau"
	Versicherung   Industry = "versicherung"
	SaaSB2B        Industry = "saas-b2b"
)

// Industries returns every industry in display order. Used by the
// wizard's <select> and any future iteration helpers.
func Industries() []Industry {
	return []Industry{
		Steuerberater,
		Handwerk,
		Anwaltskanzlei,
		Maschinenbau,
		Versicherung,
		SaaSB2B,
	}
}

// DisplayName returns a human-readable label for the wizard dropdown.
// Stays in DE to match the rest of the admin UI.
func (i Industry) DisplayName() string {
	switch i {
	case Steuerberater:
		return "Steuerberatung"
	case Handwerk:
		return "Handwerk"
	case Anwaltskanzlei:
		return "Anwaltskanzlei"
	case Maschinenbau:
		return "Maschinenbau"
	case Versicherung:
		return "Versicherung"
	case SaaSB2B:
		return "SaaS / B2B"
	default:
		return string(i)
	}
}

// Valid reports whether s names a known industry. Used at every layer
// boundary that accepts an industry from outside (form posts, CLI args)
// to guarantee the storage column never holds a junk value.
func Valid(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	for _, ind := range Industries() {
		if string(ind) == s {
			return true
		}
	}
	return false
}
