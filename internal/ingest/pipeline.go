package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"

	"github.com/clemenshoenig/everychat/internal/crawler"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// EmbedBatchSize is the maximum number of strings sent to the embedder in
// one HTTP call. Keeps memory bounded and rides comfortably under the
// OpenAI 8192-token-per-input cap × 16 inputs default.
const EmbedBatchSize = 16

// Stats summarizes one Ingest run for the caller (CLI / wizard) to render.
type Stats struct {
	PagesFetched    int
	ChunksProduced  int
	EmbeddingsCalls int
}

// Pipeline orchestrates: crawl → HTML→Markdown → chunk → embed → persist.
// All parts are exchangeable for testing — the crawler is wrapped behind
// an internal interface so we can hand the pipeline a fixture stream
// without standing up a real HTTP server.
type Pipeline struct {
	DB       *sql.DB
	Embedder llm.Embedder
	Logger   io.Writer // optional progress sink; nil silences output
}

// New constructs a Pipeline with stdout progress logging.
func New(db *sql.DB, embedder llm.Embedder) *Pipeline {
	return &Pipeline{DB: db, Embedder: embedder}
}

// Ingest runs the full pipeline for one bot.
//
// Re-running for the same bot replaces the existing chunks (delete-then-
// insert) so the wizard's "Erneut crawlen" UX doesn't accumulate stale
// content.
func (p *Pipeline) Ingest(ctx context.Context, botID int64, seedURL string, opts crawler.Options) (Stats, error) {
	pages, err := crawler.Crawl(ctx, seedURL, opts)
	if err != nil {
		return Stats{}, fmt.Errorf("ingest: start crawl: %w", err)
	}
	return p.IngestPages(ctx, botID, pages)
}

// IngestPages runs the post-crawl half of the pipeline against an arbitrary
// page channel. Useful for tests that don't want to spin up a real crawler.
func (p *Pipeline) IngestPages(ctx context.Context, botID int64, pages <-chan crawler.Page) (Stats, error) {
	if err := storage.DeleteChunksForBot(ctx, p.DB, botID); err != nil {
		return Stats{}, fmt.Errorf("ingest: clear existing chunks: %w", err)
	}

	stats := Stats{}
	pending := make([]storage.Chunk, 0, EmbedBatchSize)

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		texts := make([]string, len(pending))
		for i, c := range pending {
			texts[i] = c.Content
		}
		vectors, err := p.Embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("ingest: embed batch: %w", err)
		}
		stats.EmbeddingsCalls++
		if len(vectors) != len(pending) {
			return fmt.Errorf("ingest: embedder returned %d vectors for %d inputs", len(vectors), len(pending))
		}
		for i, c := range pending {
			if _, err := storage.InsertChunk(ctx, p.DB, c, vectors[i]); err != nil {
				return fmt.Errorf("ingest: persist chunk: %w", err)
			}
		}
		pending = pending[:0]
		return nil
	}

	for page := range pages {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.PagesFetched++

		md, err := HTMLToMarkdown(page.HTML)
		if err != nil {
			p.logf("warn: %s: html→md failed: %v", page.URL, err)
			continue
		}
		chunks := SplitMarkdown(page.URL, md)
		if len(chunks) == 0 {
			p.logf("info: %s: no usable content", page.URL)
			continue
		}
		p.logf("✓ %s — %d chunks", page.URL, len(chunks))
		stats.ChunksProduced += len(chunks)

		for _, c := range chunks {
			pending = append(pending, storage.Chunk{
				BotID:      botID,
				Source:     c.Source,
				ChunkIndex: c.Index,
				Content:    c.Content,
			})
			if len(pending) >= EmbedBatchSize {
				if err := flush(); err != nil {
					return stats, err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return stats, err
	}
	return stats, nil
}

func (p *Pipeline) logf(format string, args ...any) {
	if p.Logger != nil {
		_, _ = fmt.Fprintf(p.Logger, format+"\n", args...)
		return
	}
	log.Printf(format, args...)
}
