package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Chunk is one slice of crawled / ingested knowledge attached to a bot.
// It mirrors a row in kb_chunks plus, on search results, the cosine
// distance returned by vec0.
type Chunk struct {
	ID         int64
	BotID      int64
	Source     string
	ChunkIndex int
	Content    string
	Distance   float64 // populated by SearchChunks; ignored on insert
}

// InsertChunk persists a chunk and its embedding atomically. The kb_chunks
// row is created first; its rowid is reused as kb_vec.chunk_id so a JOIN
// over the two tables stays trivial.
func InsertChunk(ctx context.Context, db *sql.DB, c Chunk, embedding []float32) (int64, error) {
	blob, err := SerializeEmbedding(embedding)
	if err != nil {
		return 0, fmt.Errorf("InsertChunk: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("InsertChunk: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO kb_chunks (bot_id, source, chunk_index, content)
		VALUES (?, ?, ?, ?)
	`, c.BotID, c.Source, c.ChunkIndex, c.Content)
	if err != nil {
		return 0, fmt.Errorf("InsertChunk: kb_chunks: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("InsertChunk: last id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO kb_vec (chunk_id, bot_id, embedding) VALUES (?, ?, ?)
	`, id, c.BotID, blob); err != nil {
		return 0, fmt.Errorf("InsertChunk: kb_vec: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("InsertChunk: commit: %w", err)
	}
	committed = true
	return id, nil
}

// SearchChunks runs a top-k cosine-similarity KNN against `botID`'s chunks.
// Results are ordered by ascending distance (closest first). Distance is
// the raw value vec0 returns — for cosine, smaller means more similar.
func SearchChunks(ctx context.Context, db *sql.DB, botID int64, queryEmbedding []float32, k int) ([]Chunk, error) {
	if k <= 0 {
		return nil, nil
	}
	blob, err := SerializeEmbedding(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("SearchChunks: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			kb_chunks.id,
			kb_chunks.bot_id,
			kb_chunks.source,
			kb_chunks.chunk_index,
			kb_chunks.content,
			kb_vec.distance
		FROM kb_vec
		JOIN kb_chunks ON kb_chunks.id = kb_vec.chunk_id
		WHERE kb_vec.bot_id = ?
		  AND kb_vec.embedding MATCH ?
		  AND k = ?
		ORDER BY kb_vec.distance
	`, botID, blob, k)
	if err != nil {
		return nil, fmt.Errorf("SearchChunks: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Chunk, 0, k)
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.BotID, &c.Source, &c.ChunkIndex, &c.Content, &c.Distance); err != nil {
			return nil, fmt.Errorf("SearchChunks: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SearchChunks: iterate: %w", err)
	}
	return out, nil
}

// DeleteChunksForBot removes every kb_chunks row (and, via trigger, every
// kb_vec row) belonging to a bot. Used by re-crawl flows.
func DeleteChunksForBot(ctx context.Context, db *sql.DB, botID int64) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM kb_chunks WHERE bot_id = ?`, botID); err != nil {
		return fmt.Errorf("DeleteChunksForBot: %w", err)
	}
	return nil
}

// CountChunks returns the number of kb_chunks rows for a bot.
func CountChunks(ctx context.Context, db *sql.DB, botID int64) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM kb_chunks WHERE bot_id = ?`, botID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("CountChunks: %w", err)
	}
	return n, nil
}
