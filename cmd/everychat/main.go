// Package main is the entrypoint for the Everychat HTTP server.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/email"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
	"github.com/clemenshoenig/everychat/internal/web"
)

const (
	defaultAddr       = ":8080"
	defaultBaseURL    = "https://everychat.local"
	defaultLiteLLMURL = "http://127.0.0.1:4000"
)

func main() {
	addr := getenv("EVERYCHAT_ADDR", defaultAddr)
	dbPath := getenv("EVERYCHAT_DB_PATH", storage.DefaultDSN)
	baseURL := getenv("EVERYCHAT_BASE_URL", defaultBaseURL)
	litellmURL := getenv("EVERYCHAT_LITELLM_URL", defaultLiteLLMURL)

	db, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("storage open: %v", err)
	}
	defer func() { _ = db.Close() }()
	log.Printf("storage ready at %s", dbPath)

	sender := email.NewStdoutSender()
	links := auth.NewMagicLinks(db, sender, baseURL)
	sessions := auth.NewSessions(db)

	// LLM gateway — used by Phase 2 sprints (eval, prompt drafting, sandbox).
	// Phase 1 routes don't call it, but constructing it here surfaces config
	// errors at boot rather than on first request.
	llmClient := llm.NewLiteLLMClient(litellmURL)
	_ = llmClient // wired into Phase 2 handlers in Sprints 5-6

	srv, err := web.New(links, sessions)
	if err != nil {
		log.Fatalf("web init: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown error: %v", err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("everychat listening on %s (base url %s, litellm %s)", addr, baseURL, litellmURL)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
	<-idleConnsClosed
	log.Println("everychat stopped")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
