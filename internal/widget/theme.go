package widget

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Theme is the typed view of bots.widget_theme_json. Single source of
// truth for both visitor-rendered surfaces (bubble + inline) and admin
// "Verhalten" rail edits.
//
// JSON tags lock the on-disk shape; the field set is append-only so
// older rows continue to deserialize as new fields are added (zero
// value → DefaultTheme fallback).
type Theme struct {
	Name           string   `json:"name,omitempty"`            // visitor-facing display name override
	Welcome        string   `json:"welcome,omitempty"`         // first bot bubble in the thread
	StarterPrompts []string `json:"starter_prompts,omitempty"` // chips above composer until first send
	Accent         string   `json:"accent,omitempty"`          // CSS color, e.g. "#2f4cff"
	Radius         string   `json:"radius,omitempty"`          // CSS length, e.g. "14px"
	Locale         string   `json:"locale,omitempty"`          // BCP-47, defaults to "de"
}

// DefaultTheme is the fallback applied when a bot row has no theme JSON
// or fields are blank. The values match the visual spec in DESIGN-phase-2.md.
var DefaultTheme = Theme{
	Welcome: "Guten Tag — wie kann ich helfen?",
	Accent:  "#2f4cff",
	Radius:  "14px",
	Locale:  "de",
}

// DecodeTheme parses a stored JSON blob into a Theme, falling back to
// DefaultTheme values for any blank fields. Unknown fields are ignored
// so future-version rows don't break older binaries.
func DecodeTheme(raw string) Theme {
	t := Theme{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &t)
	}
	if strings.TrimSpace(t.Welcome) == "" {
		t.Welcome = DefaultTheme.Welcome
	}
	if strings.TrimSpace(t.Accent) == "" {
		t.Accent = DefaultTheme.Accent
	}
	if strings.TrimSpace(t.Radius) == "" {
		t.Radius = DefaultTheme.Radius
	}
	if strings.TrimSpace(t.Locale) == "" {
		t.Locale = DefaultTheme.Locale
	}
	return t
}

// EncodeTheme serializes a Theme back to its on-disk JSON form. Empty
// fields are omitted so rows stay small and explicit.
func EncodeTheme(t Theme) (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("EncodeTheme: %w", err)
	}
	return string(b), nil
}

// CSSVars renders the theme's tokenized fields as a `:root { ... }` CSS
// block. Both bubble.html and inline.html consume this — single source
// of theming truth, so visual drift between templates is impossible.
//
// Only sanitized fields go into the CSS. Accent is validated client of
// this method (color picker → hex); the Radius field is a CSS length.
// We don't allow arbitrary string injection here — the template's data
// passes only Theme.Accent / Theme.Radius which are color/length-shaped.
func (t Theme) CSSVars() string {
	var b strings.Builder
	b.WriteString(":root{")
	fmt.Fprintf(&b, "--w-accent:%s;", sanitizeColor(t.Accent))
	fmt.Fprintf(&b, "--w-radius:%s;", sanitizeLength(t.Radius))
	b.WriteString("}")
	return b.String()
}

// sanitizeColor returns the input only if it looks like a hex color
// (`#rgb` or `#rrggbb`). Anything else falls back to the default. This
// is the security boundary against CSS injection: even if a malicious
// admin sets the accent to `red; background:url(javascript:…)`, we
// drop it.
func sanitizeColor(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 4 && len(s) != 7 {
		return DefaultTheme.Accent
	}
	if s[0] != '#' {
		return DefaultTheme.Accent
	}
	for _, c := range s[1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return DefaultTheme.Accent
		}
	}
	return s
}

// sanitizeLength accepts only digits + a unit suffix (px, rem, em).
// Anything else falls back.
func sanitizeLength(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultTheme.Radius
	}
	cut := -1
	for i, c := range s {
		if c < '0' || c > '9' {
			cut = i
			break
		}
	}
	if cut <= 0 {
		return DefaultTheme.Radius
	}
	num, unit := s[:cut], s[cut:]
	switch unit {
	case "px", "rem", "em":
		return num + unit
	default:
		return DefaultTheme.Radius
	}
}
