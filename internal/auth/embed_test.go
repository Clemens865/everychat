package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/clemenshoenig/everychat/internal/storage"
)

func TestIssueEmbedToken_RoundTrip(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	bots, err := storage.ListBots(context.Background(), db)
	if err != nil || len(bots) == 0 {
		t.Fatalf("seeded bot missing: %v", err)
	}
	id := bots[0].ID

	token, err := IssueEmbedToken(context.Background(), db, id)
	if err != nil {
		t.Fatalf("IssueEmbedToken: %v", err)
	}
	if len(token) != EmbedTokenLen*2 {
		t.Fatalf("token length: got %d, want %d", len(token), EmbedTokenLen*2)
	}

	// Round-trip via the hash.
	got, err := storage.LookupBotByEmbedTokenHash(context.Background(), db, HashEmbedToken(token))
	if err != nil {
		t.Fatalf("LookupBotByEmbedTokenHash: %v", err)
	}
	if got.ID != id {
		t.Fatalf("got bot id %d, want %d", got.ID, id)
	}
}

func TestIssueEmbedToken_RegenerateInvalidatesPrior(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	bots, _ := storage.ListBots(context.Background(), db)
	id := bots[0].ID

	first, _ := IssueEmbedToken(context.Background(), db, id)
	second, _ := IssueEmbedToken(context.Background(), db, id)
	if first == second {
		t.Fatal("expected different tokens across calls")
	}
	if _, err := storage.LookupBotByEmbedTokenHash(context.Background(), db, HashEmbedToken(first)); !errors.Is(err, storage.ErrBotNotFound) {
		t.Fatalf("first token should not resolve any bot, got %v", err)
	}
	if _, err := storage.LookupBotByEmbedTokenHash(context.Background(), db, HashEmbedToken(second)); err != nil {
		t.Fatalf("second token should resolve: %v", err)
	}
}

func TestHashEmbedToken_StableAndDeterministic(t *testing.T) {
	a := HashEmbedToken("abc")
	b := HashEmbedToken("abc")
	if a != b {
		t.Fatal("hash not deterministic")
	}
	if HashEmbedToken("abc") == HashEmbedToken("abd") {
		t.Fatal("hash collision on tiny input — sha256 broken?")
	}
}
