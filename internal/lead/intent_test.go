package lead

import "testing"

func TestKeywordDetector_Triggers(t *testing.T) {
	d := NewKeywordDetector(nil)
	cases := []string{
		"Können wir einen Termin vereinbaren?",
		"Bitte rufen Sie mich zurück.",
		"Hätte gern einen Rückruf.",
		"Wir möchten ein Erstgespräch.",
		"Senden Sie mir bitte ein verbindliches Angebot.",
		"Ich brauche einen Beratungstermin.",
	}
	for _, c := range cases {
		if got := d.Detect(c, ""); !got.Triggered {
			t.Errorf("expected triggered for %q, got %+v", c, got)
		}
	}
}

func TestKeywordDetector_TriggersOnAssistantSide(t *testing.T) {
	d := NewKeywordDetector(nil)
	got := d.Detect("Hallo", "Gerne arrangiere ich einen Rückruf für Sie.")
	if !got.Triggered {
		t.Fatalf("expected trigger on assistant text: %+v", got)
	}
}

func TestKeywordDetector_DoesNotTriggerOnNeutral(t *testing.T) {
	d := NewKeywordDetector(nil)
	cases := []string{
		"Was kostet die Lohnbuchhaltung?",
		"Welche Leistungen bietet die Kanzlei?",
		"Erstellen Sie auch Jahresabschlüsse?",
	}
	for _, c := range cases {
		if got := d.Detect(c, ""); got.Triggered {
			t.Errorf("false positive on %q: %+v", c, got)
		}
	}
}

func TestKeywordDetector_AccentNormalization(t *testing.T) {
	d := NewKeywordDetector(nil)
	// "Rueckruf" without umlauts should still match the "rückruf" term.
	if got := d.Detect("Rueckruf bitte", ""); !got.Triggered {
		t.Fatalf("ASCII-folded variant should match: %+v", got)
	}
}

func TestKeywordDetector_CustomTerms(t *testing.T) {
	d := NewKeywordDetector([]string{"appointment"})
	if !d.Detect("I'd like an appointment", "").Triggered {
		t.Fatal("custom term not matched")
	}
	if d.Detect("rückruf bitte", "").Triggered {
		t.Fatal("custom-only detector should not fall back to defaults")
	}
}
