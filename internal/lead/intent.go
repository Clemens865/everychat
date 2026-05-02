// Package lead is the bridge from "visitor expressed interest" to
// "founder gets a contact form submission". Three concerns:
//
//   - intent.Detector — decides whether a chat turn is a lead-handoff
//     candidate. Phase 3 ships a keyword-based detector; Phase 5 swaps
//     to Claude tool-use without touching callers.
//   - dispatcher — persists leads + signed-webhook delivery via the
//     internal/integrations package, with retry-queue semantics.
//   - summarizer — generates a 3–5 line summary via the LLM.
package lead

import (
	"strings"
	"unicode"
)

// Detector inspects a chat turn (user message + draft assistant reply)
// and decides whether the visitor wants a human handoff. Implementations
// must be safe for concurrent use.
type Detector interface {
	Detect(userMessage, assistantAnswer string) Match
}

// Match is the verdict from a Detector. When Triggered=true, the
// dispatcher renders a lead-capture artifact. Reason is a short label
// that goes into the audit log.
type Match struct {
	Triggered bool
	Reason    string
}

// KeywordDetector is the Phase 3 implementation: matches a curated DE
// keyword list against the user message + assistant answer. False
// positives are bounded by the quality of the keywords; false negatives
// are recoverable since the visitor can re-ask. Phase 5 promotes this
// to Claude tool-use which captures intent across paraphrases.
type KeywordDetector struct {
	terms []string // pre-lowercased
}

// DefaultKeywordTerms is the v1 list. Tuned for DACH SMB / Mittelstand
// Steuerkanzlei / Handwerksbetrieb / Anwaltskanzlei contexts. Add terms
// over time based on lead-capture-rate measurements.
var DefaultKeywordTerms = []string{
	// Direct contact intent
	"termin", "rückruf", "rueckruf", "anrufen", "kontakt aufnehmen",
	"kontaktieren", "erreichen", "sprechen sie mich",
	"zurückrufen", "zurueckrufen", "rufen sie mich",
	// Meeting / appointment phrasing
	"erstgespräch", "erstgesprach", "beratungstermin", "kennenlerngespräch",
	"persönlich treffen", "persoenlich treffen",
	// Quote / pricing escalation
	"angebot zusenden", "angebot anfordern", "kostenvoranschlag",
	"individuelle anfrage", "verbindliches angebot",
	// Direct asks
	"wie erreiche ich sie", "kann ich sie sprechen",
	"hinterlasse ich meine nummer", "mailen sie mir",
}

// NewKeywordDetector returns a Detector backed by `terms` (or
// DefaultKeywordTerms when nil/empty). Terms are lowercased once.
func NewKeywordDetector(terms []string) *KeywordDetector {
	if len(terms) == 0 {
		terms = DefaultKeywordTerms
	}
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = strings.ToLower(t)
	}
	return &KeywordDetector{terms: out}
}

// Detect implements Detector.
//
// Strategy: lowercase + de-accent + check substring match against any
// configured term. Anchoring on the user message OR the assistant reply
// catches both "I'd like to book a meeting" (user) and "let me arrange
// a callback for you" (assistant suggesting handoff).
func (d *KeywordDetector) Detect(userMessage, assistantAnswer string) Match {
	hay := strings.ToLower(deaccent(userMessage + " " + assistantAnswer))
	for _, t := range d.terms {
		if strings.Contains(hay, deaccent(t)) {
			return Match{Triggered: true, Reason: "keyword:" + t}
		}
	}
	return Match{}
}

// deaccent strips German umlauts to their ASCII equivalents so the
// detector matches "rückruf" against terms written as "rueckruf"
// without doubling the vocabulary.
func deaccent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case 'ä', 'Ä':
			b.WriteString("ae")
		case 'ö', 'Ö':
			b.WriteString("oe")
		case 'ü', 'Ü':
			b.WriteString("ue")
		case 'ß':
			b.WriteString("ss")
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
