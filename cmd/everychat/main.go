// Package main is the entrypoint for the Everychat HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clemenshoenig/everychat/internal/api"
	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/email"
	"github.com/clemenshoenig/everychat/internal/eval"
	"github.com/clemenshoenig/everychat/internal/integrations"
	"github.com/clemenshoenig/everychat/internal/lead"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/moderation"
	"github.com/clemenshoenig/everychat/internal/prompt"
	"github.com/clemenshoenig/everychat/internal/storage"
	"github.com/clemenshoenig/everychat/internal/web"
	"github.com/clemenshoenig/everychat/internal/widget"
)

const (
	defaultAddr       = ":8080"
	defaultBaseURL    = "https://everychat.local"
	defaultLiteLLMURL = "http://127.0.0.1:4000"
)

func main() {
	// Phase 6 surface: `everychat eval --bot-name=X --questions=Y` runs
	// the same code path as `everychat-ops eval`. Lets the public CLI
	// stay consolidated under a single binary.
	if len(os.Args) > 1 && os.Args[1] == "eval" {
		os.Exit(runEvalCLI(os.Args[2:]))
	}

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

	// LLM gateway — chat + embeddings.
	llmClient := llm.NewLiteLLMClient(litellmURL)

	// Prompt generator (Phase 2 Sprint 5). Hands the bot+KB to Claude
	// to draft a German system prompt.
	promptGen, err := prompt.New(db, llmClient, llmClient)
	if err != nil {
		log.Fatalf("prompt init: %v", err)
	}

	// Theme suggester (Phase 3 Sprint 4). Same retrieval path as
	// promptGen, different meta-prompt — yields a Theme JSON.
	themeSuggester, err := widget.NewSuggester(db, llmClient, llmClient)
	if err != nil {
		log.Fatalf("widget suggester init: %v", err)
	}

	srv, err := web.New(db, links, sessions, promptGen, themeSuggester, llmClient)
	if err != nil {
		log.Fatalf("web init: %v", err)
	}

	// Visitor-facing API surface. devMode=true means an empty
	// embed_origin_allow on a bot is treated as "any origin" (only safe
	// for local dev).
	devMode := getenv("EVERYCHAT_DEV_MODE", "0") == "1"

	// Phase 3 Sprint 5 — lead capture pipeline. Webhook-only delivery
	// (HubSpot deferred to Phase 5 per design memo). Moderator + intent
	// detector behind clean interfaces; default impls are noop / keyword.
	moderator := moderation.New()
	intentDet := lead.NewKeywordDetector(nil)
	webhookCli := integrations.New(devMode)
	leadDispatcher := lead.New(db, webhookCli)

	apiSrv := api.New(db, llmClient, moderator, intentDet, leadDispatcher, devMode)

	// Widget surface — embed.js loader + iframe shell + static assets.
	widgetSrv, err := widget.New(db)
	if err != nil {
		log.Fatalf("widget init: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(apiSrv, widgetSrv),
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

// runEvalCLI is the `everychat eval` subcommand — parity with
// everychat-ops eval but accessible from the consumer-facing binary.
// Returns the desired exit code.
func runEvalCLI(args []string) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	botID := fs.Int64("bot-id", 0, "bot id (mutually exclusive with --bot-name)")
	botName := fs.String("bot-name", "", "bot name (e.g. steuerkanzlei-demo)")
	questions := fs.String("questions", "", "path to golden-questions YAML (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *questions == "" || (*botID == 0 && *botName == "") {
		fmt.Fprintln(os.Stderr, "usage: everychat eval --bot-name=<n> --questions=<path>")
		return 2
	}

	dbPath := getenv("EVERYCHAT_DB_PATH", storage.DefaultDSN)
	db, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	var bot eval.Bot
	if *botID != 0 {
		bot, err = eval.LoadBot(ctx, db, *botID)
	} else {
		bot, err = eval.LookupBotByName(ctx, db, *botName)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "load bot: %v\n", err)
		return 1
	}

	chat := llm.NewLiteLLMClient(getenv("EVERYCHAT_LITELLM_URL", defaultLiteLLMURL))
	rep, err := eval.NewRunner(db, chat).Run(ctx, bot, *questions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: %v\n", err)
		return 1
	}
	eval.PrintScorecard(os.Stdout, rep)
	if !rep.MeetsThreshold() {
		return 2
	}
	return 0
}
