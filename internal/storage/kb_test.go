package storage

import (
	"context"
	"database/sql"
	"math/rand"
	"testing"
)

// seedBot inserts a tenant + bot and returns the bot's id. Tests don't go
// through the bots package because the kb layer must work in isolation.
func seedBot(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tenants (name, domain) VALUES (?, ?)`, "T", "t.test")
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO bots (tenant_id, name) VALUES (?, ?)`, tid, "kb-test-bot")
	if err != nil {
		t.Fatal(err)
	}
	bid, _ := res.LastInsertId()
	return bid
}

// unitVec returns a deterministic length-EmbedDims vector with the first
// `n` slots set to non-zero values. This gives us repeatable distances
// without having to call a real embedder in tests.
func unitVec(seed int) []float32 {
	v := make([]float32, EmbedDims)
	r := rand.New(rand.NewSource(int64(seed))) //nolint:gosec // deterministic test data
	for i := range v {
		v[i] = r.Float32()
	}
	return v
}

func TestInsertChunk_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)

	id, err := InsertChunk(context.Background(), db, Chunk{
		BotID:      botID,
		Source:     "https://example.de/about",
		ChunkIndex: 0,
		Content:    "We help SMEs.",
	}, unitVec(1))
	if err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero chunk id")
	}

	// kb_chunks row exists
	var content string
	if err := db.QueryRow(`SELECT content FROM kb_chunks WHERE id = ?`, id).Scan(&content); err != nil {
		t.Fatalf("kb_chunks read: %v", err)
	}
	if content != "We help SMEs." {
		t.Fatalf("content: %q", content)
	}

	// kb_vec row exists for the same chunk_id
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM kb_vec WHERE chunk_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("kb_vec count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 kb_vec row, got %d", n)
	}
}

func TestInsertChunk_DimMismatch(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	short := []float32{1, 2, 3}
	if _, err := InsertChunk(context.Background(), db, Chunk{BotID: botID, Source: "x", Content: "x"}, short); err == nil {
		t.Fatal("expected dim-mismatch error")
	}
}

func TestSearchChunks_KNNOrdering(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)

	// Insert three chunks with deterministic embeddings; the query is
	// identical to one of them, so it should rank closest.
	v1 := unitVec(1)
	v2 := unitVec(2)
	v3 := unitVec(3)
	for i, v := range [][]float32{v1, v2, v3} {
		if _, err := InsertChunk(context.Background(), db, Chunk{
			BotID:      botID,
			Source:     "src",
			ChunkIndex: i,
			Content:    "chunk-" + itoa(i+1),
		}, v); err != nil {
			t.Fatal(err)
		}
	}

	got, err := SearchChunks(context.Background(), db, botID, v2, 2)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].Content != "chunk-2" {
		t.Errorf("nearest: got %q, want chunk-2", got[0].Content)
	}
	if !(got[0].Distance <= got[1].Distance) {
		t.Errorf("results not sorted by distance: %v vs %v", got[0].Distance, got[1].Distance)
	}
}

func TestSearchChunks_BotIsolation(t *testing.T) {
	db := openTestDB(t)
	botA := seedBot(t, db)
	// second bot
	res, err := db.Exec(`INSERT INTO bots (tenant_id, name) VALUES (1, 'bot-b')`)
	if err != nil {
		t.Fatal(err)
	}
	botB, _ := res.LastInsertId()

	if _, err := InsertChunk(context.Background(), db, Chunk{BotID: botA, Source: "a", Content: "a"}, unitVec(7)); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertChunk(context.Background(), db, Chunk{BotID: botB, Source: "b", Content: "b"}, unitVec(7)); err != nil {
		t.Fatal(err)
	}

	got, err := SearchChunks(context.Background(), db, botA, unitVec(7), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BotID != botA {
		t.Fatalf("expected only bot A's chunk, got %+v", got)
	}
}

func TestDeleteChunksForBot_CascadesToVec(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	for i := 0; i < 3; i++ {
		if _, err := InsertChunk(context.Background(), db, Chunk{BotID: botID, Source: "s", ChunkIndex: i, Content: "c"}, unitVec(i+10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteChunksForBot(context.Background(), db, botID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM kb_chunks WHERE bot_id = ?`, botID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("kb_chunks count: %d, err: %v", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM kb_vec WHERE bot_id = ?`, botID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("kb_vec count after delete (trigger should have cascaded): %d, err: %v", n, err)
	}
}

func TestCountChunks(t *testing.T) {
	db := openTestDB(t)
	botID := seedBot(t, db)
	for i := 0; i < 5; i++ {
		if _, err := InsertChunk(context.Background(), db, Chunk{BotID: botID, Source: "s", ChunkIndex: i, Content: "c"}, unitVec(i+100)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CountChunks(context.Background(), db, botID)
	if err != nil || n != 5 {
		t.Fatalf("CountChunks: %d, err: %v", n, err)
	}
}
