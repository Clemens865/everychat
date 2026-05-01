// Package ingest converts crawled HTML pages into Markdown chunks ready
// for embedding and KB storage. The Phase 2 pipeline is: html-to-markdown
// to strip chrome and produce clean text, then a heading-aware greedy
// chunker that targets ~3200 chars per chunk.
package ingest

import (
	"strings"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// MaxChunkChars is the soft size limit for a single chunk. ~3200 chars is
// roughly 800 tokens for OpenAI's tokenizer — leaves headroom under the
// embedding model's input cap and keeps similarity bands tight.
const MaxChunkChars = 3200

// MinChunkChars guards against single-line chunks. Anything under this
// gets merged with the next section before emission.
const MinChunkChars = 200

// Chunk is one slice of cleaned Markdown ready for embedding.
type Chunk struct {
	Source  string // page URL the chunk came from
	Index   int    // 0-based ordinal within the source page
	Content string
}

// HTMLToMarkdown strips chrome (nav / footer / script / style) and converts
// the remaining HTML to Markdown. Leaves heading and link structure intact
// so the chunker can use them as boundaries.
func HTMLToMarkdown(html string) (string, error) {
	md, err := htmltomd.ConvertString(html)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(md), nil
}

// SplitMarkdown splits Markdown into ~MaxChunkChars-sized pieces,
// preferring heading boundaries. Each emitted chunk has a non-empty
// Content; chunks shorter than MinChunkChars are merged into the next.
func SplitMarkdown(source, markdown string) []Chunk {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	sections := splitOnHeadings(markdown)

	var (
		out  []Chunk
		buf  strings.Builder
		idx  int
		emit = func() {
			s := strings.TrimSpace(buf.String())
			if s == "" {
				return
			}
			out = append(out, Chunk{Source: source, Index: idx, Content: s})
			idx++
			buf.Reset()
		}
	)

	for _, section := range sections {
		if buf.Len()+len(section) > MaxChunkChars && buf.Len() >= MinChunkChars {
			emit()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(section)
		// If a single section blows past MaxChunkChars on its own, break it
		// at paragraph boundaries to avoid a giant chunk.
		if buf.Len() >= MaxChunkChars {
			parts := splitOversized(buf.String())
			buf.Reset()
			for i, p := range parts {
				if i < len(parts)-1 {
					out = append(out, Chunk{Source: source, Index: idx, Content: strings.TrimSpace(p)})
					idx++
					continue
				}
				buf.WriteString(p)
			}
		}
	}
	emit()
	return out
}

// splitOnHeadings cuts Markdown at top-level (#) and second-level (##)
// heading boundaries. Headings stay attached to the section they introduce.
func splitOnHeadings(md string) []string {
	lines := strings.Split(md, "\n")
	var sections []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			sections = append(sections, s)
		}
		cur.Reset()
	}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		isHeading := strings.HasPrefix(trim, "# ") || strings.HasPrefix(trim, "## ")
		if isHeading && cur.Len() > 0 {
			flush()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	flush()
	return sections
}

// splitOversized breaks a chunk that's too large at paragraph boundaries.
// Returns at least one element.
func splitOversized(s string) []string {
	paragraphs := strings.Split(s, "\n\n")
	var out []string
	var cur strings.Builder
	for _, p := range paragraphs {
		if cur.Len()+len(p) > MaxChunkChars && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		out = []string{s}
	}
	return out
}
