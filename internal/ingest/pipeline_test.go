package ingest

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"testing"

	"github.com/clemenshoenig/everychat/internal/crawler"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// fakeEmbedder produces a deterministic 1536-dim vector seeded by the
// hash of each input string, so tests can assert "different chunks → different
// vectors" without calling a real embedding service.
type fakeEmbedder struct {
	calls int
	err   error
}

func (f *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		seed := int64(0)
		for _, c := range s {
			seed = seed*31 + int64(c)
		}
		r := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic test data
		v := make([]float32, storage.EmbedDims)
		for j := range v {
			v[j] = r.Float32()
		}
		out[i] = v
	}
	return out, nil
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedBot(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tenants (name, domain) VALUES (?, ?)`, "T", "t.test")
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO bots (tenant_id, name) VALUES (?, ?)`, tid, "ingest-test-bot")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func staticPages(pages ...crawler.Page) <-chan crawler.Page {
	ch := make(chan crawler.Page, len(pages))
	for _, p := range pages {
		ch <- p
	}
	close(ch)
	return ch
}

func TestPipeline_IngestPages_HappyPath(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	em := &fakeEmbedder{}
	p := New(db, em)

	pages := staticPages(
		crawler.Page{URL: "https://example.de/", HTML: `<html><body><h1>Home</h1><p>Wir machen Steuerberatung.</p></body></html>`},
		crawler.Page{URL: "https://example.de/leistungen", HTML: `<html><body><h1>Leistungen</h1><p>Lohnbuchhaltung.</p></body></html>`},
	)

	stats, err := p.IngestPages(context.Background(), botID, pages)
	if err != nil {
		t.Fatalf("IngestPages: %v", err)
	}
	if stats.PagesFetched != 2 {
		t.Errorf("PagesFetched: got %d, want 2", stats.PagesFetched)
	}
	if stats.ChunksProduced < 2 {
		t.Errorf("ChunksProduced: got %d, want >=2", stats.ChunksProduced)
	}

	n, err := storage.CountChunks(context.Background(), db, botID)
	if err != nil {
		t.Fatal(err)
	}
	if n != stats.ChunksProduced {
		t.Errorf("DB chunk count %d != stats.ChunksProduced %d", n, stats.ChunksProduced)
	}

	// Vector retrieval works end-to-end.
	q, _ := em.Embed(context.Background(), []string{"Steuerberatung"})
	hits, err := storage.SearchChunks(context.Background(), db, botID, q[0], 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for the embedded query")
	}
}

func TestPipeline_ReingestReplacesChunks(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	em := &fakeEmbedder{}
	p := New(db, em)

	first := staticPages(crawler.Page{URL: "https://example.de/", HTML: `<html><body><h1>One</h1><p>Erster Inhalt.</p></body></html>`})
	if _, err := p.IngestPages(context.Background(), botID, first); err != nil {
		t.Fatal(err)
	}
	beforeCount, _ := storage.CountChunks(context.Background(), db, botID)

	second := staticPages(crawler.Page{URL: "https://example.de/", HTML: `<html><body><h1>Two</h1><p>Anderer Inhalt.</p></body></html>`})
	if _, err := p.IngestPages(context.Background(), botID, second); err != nil {
		t.Fatal(err)
	}
	afterCount, _ := storage.CountChunks(context.Background(), db, botID)

	if afterCount > beforeCount {
		t.Fatalf("chunks accumulated across re-ingest: before=%d after=%d", beforeCount, afterCount)
	}
	// Content of the latest run should win.
	rows, err := db.Query(`SELECT content FROM kb_chunks WHERE bot_id = ?`, botID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var sawTwo bool
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		if contains(c, "Anderer Inhalt") {
			sawTwo = true
		}
		if contains(c, "Erster Inhalt") {
			t.Errorf("stale chunk survived re-ingest: %q", c)
		}
	}
	if !sawTwo {
		t.Errorf("new chunks not found after re-ingest")
	}
}

func TestPipeline_EmbedderErrorPropagates(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	p := New(db, &fakeEmbedder{err: errors.New("rate limited")})

	pages := staticPages(crawler.Page{URL: "https://example.de/", HTML: `<html><body><p>x</p></body></html>`})
	if _, err := p.IngestPages(context.Background(), botID, pages); err == nil {
		t.Fatal("expected embedder error to propagate")
	}
}

func TestPipeline_SkipsEmptyPages(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	em := &fakeEmbedder{}
	p := New(db, em)

	pages := staticPages(
		crawler.Page{URL: "https://example.de/empty", HTML: `<html><head></head><body></body></html>`},
	)
	stats, err := p.IngestPages(context.Background(), botID, pages)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChunksProduced != 0 {
		t.Fatalf("expected 0 chunks for empty page, got %d", stats.ChunksProduced)
	}
	if em.calls != 0 {
		t.Fatalf("embedder called for empty page: %d times", em.calls)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
