package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/clemenshoenig/everychat/internal/email"
	"github.com/clemenshoenig/everychat/internal/storage"
)

func TestMagicLink_RequestAndVerifyHappyPath(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	buf := &bytes.Buffer{}
	sender := email.NewStdoutSenderTo(buf)
	m := NewMagicLinks(db, sender, "https://everychat.local")

	ctx := context.Background()
	if err := m.RequestLink(ctx, "owner@example.com"); err != nil {
		t.Fatalf("RequestLink: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "owner@example.com") {
		t.Fatalf("email body did not address recipient: %q", out)
	}
	token := extractToken(t, out)

	addr, err := m.VerifyLink(ctx, token)
	if err != nil {
		t.Fatalf("VerifyLink: %v", err)
	}
	if addr != "owner@example.com" {
		t.Fatalf("got addr %q, want owner@example.com", addr)
	}
}

func TestMagicLink_ReplayRejected(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	buf := &bytes.Buffer{}
	m := NewMagicLinks(db, email.NewStdoutSenderTo(buf), "https://everychat.local")

	if err := m.RequestLink(context.Background(), "x@y.de"); err != nil {
		t.Fatal(err)
	}
	token := extractToken(t, buf.String())

	if _, err := m.VerifyLink(context.Background(), token); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := m.VerifyLink(context.Background(), token); !errors.Is(err, ErrUsedToken) {
		t.Fatalf("replay: got %v, want ErrUsedToken", err)
	}
}

func TestMagicLink_ExpiredRejected(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	buf := &bytes.Buffer{}
	m := NewMagicLinks(db, email.NewStdoutSenderTo(buf), "https://everychat.local")

	frozen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return frozen })

	if err := m.RequestLink(context.Background(), "x@y.de"); err != nil {
		t.Fatal(err)
	}
	token := extractToken(t, buf.String())

	// jump past the 15-minute lifetime
	m.SetClock(func() time.Time { return frozen.Add(MagicLinkLifetime + time.Minute) })

	if _, err := m.VerifyLink(context.Background(), token); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expired: got %v, want ErrExpiredToken", err)
	}
}

func TestMagicLink_UnknownToken(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	m := NewMagicLinks(db, email.NewStdoutSenderTo(&bytes.Buffer{}), "https://everychat.local")
	if _, err := m.VerifyLink(context.Background(), "deadbeef"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown: got %v, want ErrInvalidToken", err)
	}
}

func TestMagicLink_RejectsBadEmail(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m := NewMagicLinks(db, email.NewStdoutSenderTo(&bytes.Buffer{}), "https://everychat.local")
	if err := m.RequestLink(context.Background(), "not-an-email"); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("bad email: got %v, want ErrInvalidEmail", err)
	}
}

// extractToken pulls the magic-link token out of an emitted email body.
func extractToken(t *testing.T, body string) string {
	t.Helper()
	const marker = "/auth/verify?t="
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no token in body: %q", body)
	}
	rest := body[i+len(marker):]
	end := strings.IndexAny(rest, " \r\n")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}
