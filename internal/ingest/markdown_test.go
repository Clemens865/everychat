package ingest

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdown_StripsChrome(t *testing.T) {
	html := `<html><body>
		<nav><a href="/">home</a></nav>
		<h1>Leistungen</h1>
		<p>Steuerberatung und <strong>Lohnbuchhaltung</strong>.</p>
		<footer>(c) 2026</footer>
		<script>alert(1)</script>
	</body></html>`
	md, err := HTMLToMarkdown(html)
	if err != nil {
		t.Fatalf("HTMLToMarkdown: %v", err)
	}
	if !strings.Contains(md, "Leistungen") {
		t.Errorf("missing heading: %q", md)
	}
	if !strings.Contains(md, "Lohnbuchhaltung") {
		t.Errorf("missing bold content: %q", md)
	}
	if strings.Contains(md, "alert(1)") {
		t.Errorf("script content leaked into markdown: %q", md)
	}
}

func TestSplitMarkdown_ChunksOnHeadings(t *testing.T) {
	md := `# Section A
This is the first section. It is short.

## Subsection A1
Some details.

# Section B
Second top-level section.`
	got := SplitMarkdown("https://example.de/x", md)
	if len(got) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for i, c := range got {
		if c.Source != "https://example.de/x" {
			t.Errorf("chunk %d wrong source: %q", i, c.Source)
		}
		if c.Index != i {
			t.Errorf("chunk %d wrong index: %d", i, c.Index)
		}
		if strings.TrimSpace(c.Content) == "" {
			t.Errorf("chunk %d empty content", i)
		}
	}
}

func TestSplitMarkdown_LargeSectionGetsSplit(t *testing.T) {
	// Build one section that exceeds MaxChunkChars by repeating paragraphs.
	var sb strings.Builder
	sb.WriteString("# Big\n\n")
	para := strings.Repeat("Dies ist ein längerer Absatz mit Inhalt. ", 30) + "\n\n"
	for sb.Len() < MaxChunkChars*3 {
		sb.WriteString(para)
	}
	got := SplitMarkdown("u", sb.String())
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks for oversized section, got %d", len(got))
	}
	for _, c := range got {
		if len(c.Content) > MaxChunkChars*2 {
			t.Errorf("chunk too large: %d chars", len(c.Content))
		}
	}
}

func TestSplitMarkdown_EmptyInput(t *testing.T) {
	if got := SplitMarkdown("u", ""); len(got) != 0 {
		t.Fatalf("expected no chunks for empty input, got %d", len(got))
	}
	if got := SplitMarkdown("u", "   \n\n  "); len(got) != 0 {
		t.Fatalf("expected no chunks for whitespace input, got %d", len(got))
	}
}
