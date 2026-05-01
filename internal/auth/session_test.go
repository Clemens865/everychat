package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clemenshoenig/everychat/internal/storage"
)

func TestSessions_CreateAndLookup(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := NewSessions(db)
	token, err := s.Create(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	addr, err := s.Lookup(context.Background(), token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if addr != "owner@example.com" {
		t.Fatalf("got %q", addr)
	}
}

func TestSessions_Expired(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := NewSessions(db)
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return frozen })

	token, err := s.Create(context.Background(), "x@y.de")
	if err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return frozen.Add(SessionLifetime + time.Minute) })

	if _, err := s.Lookup(context.Background(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired: got %v, want ErrNoSession", err)
	}
}

func TestSessions_RequireMiddleware(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewSessions(db)

	gotEmail := ""
	handler := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEmail = SessionEmail(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// no cookie -> redirect to /login
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no cookie: status %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("redirect target %q", loc)
	}

	// valid cookie -> handler invoked, email injected
	token, err := s.Create(context.Background(), "admin@example.de")
	if err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("valid cookie: status %d", rec2.Code)
	}
	if gotEmail != "admin@example.de" {
		t.Fatalf("got email %q", gotEmail)
	}

	// bogus cookie -> redirect
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req3.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "garbage"})
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusSeeOther {
		t.Fatalf("bad cookie: status %d", rec3.Code)
	}
}
