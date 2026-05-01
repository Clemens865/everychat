package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// SessionLifetime is how long a freshly minted session remains valid.
const SessionLifetime = 30 * 24 * time.Hour

// SessionCookieName is the cookie under which the session token is stored.
const SessionCookieName = "everychat_session"

// ErrNoSession is returned when no valid session is found on the request.
var ErrNoSession = errors.New("auth: no session")

// ctxKey is unexported so callers must go through SessionEmail.
type ctxKey struct{}

// Sessions manages cookie-based admin sessions persisted in SQLite.
type Sessions struct {
	db  *sql.DB
	now func() time.Time
}

// NewSessions wires Sessions to the given DB.
func NewSessions(db *sql.DB) *Sessions {
	return &Sessions{db: db, now: time.Now}
}

// SetClock overrides the clock for testing.
func (s *Sessions) SetClock(now func() time.Time) { s.now = now }

// Create issues a new session for the given email and returns the opaque
// token string the caller should set as an HttpOnly cookie. Only the sha256
// hash of the token is stored.
func (s *Sessions) Create(ctx context.Context, addr string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", fmt.Errorf("session create: token: %w", err)
	}
	expires := s.now().Add(SessionLifetime).UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (user_email, token_hash, expires_at)
		VALUES (?, ?, ?)
	`, addr, hashToken(token), expires); err != nil {
		return "", fmt.Errorf("session create: insert: %w", err)
	}
	return token, nil
}

// Lookup resolves a token (the cookie value) to its associated email or
// ErrNoSession if no live session exists.
func (s *Sessions) Lookup(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrNoSession
	}
	var (
		addr      string
		expiresAt time.Time
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT user_email, expires_at FROM sessions WHERE token_hash = ?
	`, hashToken(token)).Scan(&addr, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSession
	}
	if err != nil {
		return "", fmt.Errorf("session lookup: %w", err)
	}
	if !s.now().Before(expiresAt) {
		return "", ErrNoSession
	}
	return addr, nil
}

// SetCookie attaches the session cookie to the response. SameSite=Lax keeps
// the magic-link redirect flow working; Secure is set so cookies are only
// sent over HTTPS (Caddy terminates TLS in front of the binary).
func (s *Sessions) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  s.now().Add(SessionLifetime),
	})
}

// ClearCookie removes the session cookie on logout.
func (s *Sessions) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Require returns a middleware that gates the wrapped handler behind a valid
// session. Unauthenticated requests are redirected to /login.
func (s *Sessions) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookieName)
		if err != nil || c.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		addr, err := s.Lookup(r.Context(), c.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, addr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SessionEmail returns the email associated with the request's session, or
// "" if none.
func SessionEmail(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}
