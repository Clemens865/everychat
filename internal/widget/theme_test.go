package widget

import (
	"strings"
	"testing"
)

func TestDecodeTheme_BlanksFallToDefaults(t *testing.T) {
	got := DecodeTheme("")
	if got.Accent != DefaultTheme.Accent || got.Radius != DefaultTheme.Radius || got.Locale != DefaultTheme.Locale {
		t.Fatalf("blanks not defaulted: %+v", got)
	}
}

func TestDecodeTheme_RoundTrip(t *testing.T) {
	src := Theme{
		Name:           "Steuerkanzlei",
		Welcome:        "Hallo",
		StarterPrompts: []string{"Leistungen?", "Honorar?"},
		Accent:         "#1a3aff",
		Radius:         "10px",
		Locale:         "de",
	}
	enc, err := EncodeTheme(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enc, "starter_prompts") {
		t.Fatalf("encoded missing starter_prompts: %s", enc)
	}
	got := DecodeTheme(enc)
	if got.Name != src.Name || got.Accent != src.Accent || len(got.StarterPrompts) != 2 {
		t.Fatalf("round-trip changed value: in=%+v out=%+v", src, got)
	}
}

func TestDecodeTheme_UnknownFieldsIgnored(t *testing.T) {
	got := DecodeTheme(`{"accent":"#abcdef","mystery":"future-field","welcome":"Hi"}`)
	if got.Accent != "#abcdef" || got.Welcome != "Hi" {
		t.Fatalf("unknown fields broke decode: %+v", got)
	}
}

func TestSanitizeColor_AcceptsHex(t *testing.T) {
	cases := map[string]string{
		"#abc":                "#abc",
		"#ABCDEF":             "#ABCDEF",
		"#123456":             "#123456",
		"red":                 DefaultTheme.Accent,
		"#xyz":                DefaultTheme.Accent,
		"":                    DefaultTheme.Accent,
		"#ab":                 DefaultTheme.Accent,
		"javascript:alert(1)": DefaultTheme.Accent,
	}
	for in, want := range cases {
		if got := sanitizeColor(in); got != want {
			t.Errorf("sanitizeColor(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeLength_AcceptsKnownUnits(t *testing.T) {
	cases := map[string]string{
		"14px":            "14px",
		"1rem":            "1rem",
		"2em":             "2em",
		"100vh":           DefaultTheme.Radius,
		"; background:#f": DefaultTheme.Radius,
		"":                DefaultTheme.Radius,
	}
	for in, want := range cases {
		if got := sanitizeLength(in); got != want {
			t.Errorf("sanitizeLength(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestCSSVars_ContainsTokens(t *testing.T) {
	t.Helper()
	out := Theme{Accent: "#abc", Radius: "12px"}.CSSVars()
	if !strings.Contains(out, "--w-accent:#abc") || !strings.Contains(out, "--w-radius:12px") {
		t.Fatalf("CSSVars missing tokens: %s", out)
	}
}

func TestCSSVars_RejectsInjection(t *testing.T) {
	out := Theme{Accent: `red; background:url(javascript:alert(1))`, Radius: `10px;}*{display:none}`}.CSSVars()
	if strings.Contains(out, "javascript") || strings.Contains(out, "background") || strings.Contains(out, "display") {
		t.Fatalf("CSSVars leaked malicious payload: %s", out)
	}
	// Should fall back to defaults.
	if !strings.Contains(out, DefaultTheme.Accent) || !strings.Contains(out, DefaultTheme.Radius) {
		t.Fatalf("CSSVars didn't fall back to defaults: %s", out)
	}
}
