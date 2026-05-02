package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/clemenshoenig/everychat/internal/storage"
)

// EmbedTokenLen is the byte length of the random token before hex encoding.
// 32 bytes → 64 hex chars, matching the magic-link / session token shape.
const EmbedTokenLen = 32

// IssueEmbedToken generates a fresh public bot-token for the widget,
// stores its sha256 on the bot row, and returns the cleartext token. The
// cleartext is shown ONCE to the founder in the admin UI; subsequent
// regenerations invalidate the previous token.
func IssueEmbedToken(ctx context.Context, db *sql.DB, botID int64) (string, error) {
	var buf [EmbedTokenLen]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("IssueEmbedToken: rand: %w", err)
	}
	token := hex.EncodeToString(buf[:])
	if err := storage.SetEmbedTokenHash(ctx, db, botID, HashEmbedToken(token)); err != nil {
		return "", err
	}
	return token, nil
}

// HashEmbedToken returns the sha256 hex digest of the cleartext token.
// Exposed so the visitor-API layer can hash incoming tokens once and look
// them up in storage without repeating the digest computation.
func HashEmbedToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
