// Package auth implements magic-link login and signed sessions for the
// Everychat admin UI. Tokens are 256-bit, stored as sha256 hashes at rest,
// and have short expirations (15 min for magic links, 30 d for sessions).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/email"
)

// Errors returned by the magic-link verifier.
var (
	ErrInvalidToken = errors.New("auth: invalid or unknown token")
	ErrExpiredToken = errors.New("auth: token expired")
	ErrUsedToken    = errors.New("auth: token already used")
	ErrInvalidEmail = errors.New("auth: invalid email")
)

// MagicLinkLifetime is how long a freshly issued magic link remains valid.
const MagicLinkLifetime = 15 * time.Minute

// MagicLinks issues and verifies one-shot login tokens.
type MagicLinks struct {
	db      *sql.DB
	sender  email.Sender
	baseURL string // e.g. https://everychat.local
	now     func() time.Time
}

// NewMagicLinks wires a MagicLinks helper to the given DB, email Sender, and
// public-facing base URL. baseURL is used to build the link the user clicks.
func NewMagicLinks(db *sql.DB, sender email.Sender, baseURL string) *MagicLinks {
	return &MagicLinks{
		db:      db,
		sender:  sender,
		baseURL: strings.TrimRight(baseURL, "/"),
		now:     time.Now,
	}
}

// SetClock overrides the clock for testing.
func (m *MagicLinks) SetClock(now func() time.Time) { m.now = now }

// RequestLink generates a fresh token for the given email, persists its
// hash, and dispatches the magic-link email via the configured Sender.
func (m *MagicLinks) RequestLink(ctx context.Context, addr string) error {
	addr = strings.TrimSpace(addr)
	if !looksLikeEmail(addr) {
		return ErrInvalidEmail
	}
	token, err := newToken()
	if err != nil {
		return fmt.Errorf("magic link: generate token: %w", err)
	}
	hash := hashToken(token)
	expires := m.now().Add(MagicLinkLifetime).UTC()

	_, err = m.db.ExecContext(ctx, `
		INSERT INTO magic_links (email, token_hash, expires_at)
		VALUES (?, ?, ?)
	`, addr, hash, expires)
	if err != nil {
		return fmt.Errorf("magic link: store: %w", err)
	}

	link := m.baseURL + "/auth/verify?t=" + token
	subject := "Your Everychat sign-in link"
	body := "Hi,\n\nClick the link below to sign in. It is valid for 15 minutes and can be used only once.\n\n" +
		link +
		"\n\nIf you did not request this, you can ignore this email.\n"
	if err := m.sender.Send(ctx, addr, subject, body); err != nil {
		return fmt.Errorf("magic link: send: %w", err)
	}
	return nil
}

// VerifyLink checks the supplied token, marks it as used, and returns the
// associated email on success.
func (m *MagicLinks) VerifyLink(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrInvalidToken
	}
	hash := hashToken(token)

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("magic link verify: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		id        int64
		addr      string
		expiresAt time.Time
		usedAt    sql.NullTime
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, email, expires_at, used_at
		FROM magic_links
		WHERE token_hash = ?
	`, hash).Scan(&id, &addr, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", fmt.Errorf("magic link verify: lookup: %w", err)
	}
	if usedAt.Valid {
		return "", ErrUsedToken
	}
	if !m.now().Before(expiresAt) {
		return "", ErrExpiredToken
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE magic_links SET used_at = ? WHERE id = ?
	`, m.now().UTC(), id); err != nil {
		return "", fmt.Errorf("magic link verify: mark used: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("magic link verify: commit: %w", err)
	}
	committed = true
	return addr, nil
}

// newToken returns a 256-bit URL-safe hex string.
func newToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// hashToken returns the hex-encoded sha256 of the token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// looksLikeEmail does the cheapest possible sanity check. We deliberately
// avoid net/mail.ParseAddress here so we can keep this dependency-free.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	dot := strings.LastIndexByte(s, '.')
	return dot > at+1 && dot < len(s)-1
}
