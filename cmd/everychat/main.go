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

	"github.com/clemenshoenig/everychat/internal/adversary"
	"github.com/clemenshoenig/everychat/internal/api"
	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/corpus"
	"github.com/clemenshoenig/everychat/internal/email"
	"github.com/clemenshoenig/everychat/internal/eval"
	"github.com/clemenshoenig/everychat/internal/integrations"
	"github.com/clemenshoenig/everychat/internal/lead"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/moderation"
	"github.com/clemenshoenig/everychat/internal/prompt"
	"github.com/clemenshoenig/everychat/internal/retention"
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
	if len(os.Args) > 1 && os.Args[1] == "eval-holdout" {
		os.Exit(runHoldoutCLI(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "check-corpus" {
		os.Exit(runCheckCorpusCLI(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "adversary" {
		os.Exit(runAdversaryCLI(os.Args[2:]))
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

	// Phase 3 Sprint 6 — DSGVO retention sweeper. Daily ticker; boot
	// catch-up runs once on startup. The sweeper writes _retention_runs
	// + audit_log per Art. 30 evidence requirements.
	retScheduler := retention.NewScheduler(db)
	retCtx, retCancel := context.WithCancel(context.Background())
	defer retCancel()
	retScheduler.Start(retCtx)

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

// runCheckCorpusCLI walks every industry in the taxonomy and validates
// the embedded corpus YAML through corpus.Load. Empty industries
// (placeholder content) are reported as ok-empty so the gate stays
// green for non-seeded verticals; any schema/holdout-overlap error
// fails the build. Wired to `make check-corpus`.
func runCheckCorpusCLI(_ []string) int {
	rc := 0
	for _, ind := range corpus.Industries() {
		bundle, err := corpus.Load(ind)
		switch {
		case errors.Is(err, corpus.ErrEmpty):
			fmt.Printf("  %-16s  empty (placeholder)\n", ind)
		case err != nil:
			fmt.Fprintf(os.Stderr, "  %-16s  FAIL  %v\n", ind, err)
			rc = 1
		default:
			fmt.Printf("  %-16s  ok    questions=%d exemplars=%d holdout=%d\n",
				ind, len(bundle.Questions), len(bundle.Exemplars), len(bundle.Holdout))
		}
	}
	if rc != 0 {
		fmt.Fprintln(os.Stderr, "check-corpus: at least one bundle failed validation")
	}
	return rc
}

// runHoldoutCLI is the `everychat eval-holdout` subcommand introduced
// in Phase 4 Sprint 5 — runs the bot against the industry's blind
// hold-out questions (loaded from internal/corpus/de/<industry>/holdout.yaml)
// through the visitor RAG pipeline. Persists last_holdout_score on
// the bot. Exits 2 if the score falls below threshold so CI can
// distinguish quality-gate failure from infrastructure failure (1).
func runHoldoutCLI(args []string) int {
	fs := flag.NewFlagSet("eval-holdout", flag.ContinueOnError)
	botName := fs.String("bot-name", "", "bot name (required)")
	industryFlag := fs.String("industry", "", "industry tag (required; one of corpus.Industries())")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *botName == "" || *industryFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: everychat eval-holdout --bot-name=<n> --industry=<tag>")
		return 2
	}
	if !corpus.Valid(*industryFlag) {
		fmt.Fprintf(os.Stderr, "unknown industry %q (try one of: ", *industryFlag)
		for i, ind := range corpus.Industries() {
			if i > 0 {
				fmt.Fprint(os.Stderr, ", ")
			}
			fmt.Fprint(os.Stderr, string(ind))
		}
		fmt.Fprintln(os.Stderr, ")")
		return 2
	}

	bundle, err := corpus.Load(corpus.Industry(*industryFlag))
	if err != nil && !errors.Is(err, corpus.ErrEmpty) {
		fmt.Fprintf(os.Stderr, "load corpus: %v\n", err)
		return 1
	}
	if bundle == nil || len(bundle.Holdout) == 0 {
		fmt.Fprintf(os.Stderr, "industry %q has no hold-out questions yet — Sprint 6 fills steuerberater + handwerk.\n", *industryFlag)
		return 1
	}

	dbPath := getenv("EVERYCHAT_DB_PATH", storage.DefaultDSN)
	db, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	bot, err := eval.LookupBotByName(ctx, db, *botName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load bot: %v\n", err)
		return 1
	}

	chat := llm.NewLiteLLMClient(getenv("EVERYCHAT_LITELLM_URL", defaultLiteLLMURL))
	runner := eval.NewHoldoutRunner(db, chat, chat) // same client doubles as embedder
	rep, err := runner.Run(ctx, bot, bundle.Holdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hold-out run: %v\n", err)
		return 1
	}
	eval.PrintScorecard(os.Stdout, rep)
	if !rep.MeetsThreshold() {
		return 2
	}
	return 0
}

// runAdversaryCLI is the `everychat adversary` subcommand introduced
// in Phase 5 Sprint 1. Runs a tester-LLM red-team against a bot for
// the given persona, persists transcript + verdict, and prints the
// run-id + final verdict. Exit codes: 0=PASS verdict, 2=FAIL or
// non-PASS verdict (cost_exhausted, error, JUDGE_ERROR), 1=infra.
func runAdversaryCLI(args []string) int {
	fs := flag.NewFlagSet("adversary", flag.ContinueOnError)
	botName := fs.String("bot-name", "", "bot name (required)")
	personaID := fs.String("persona", "", "persona id (required; one of adversary.Personas())")
	turns := fs.Int("turns", 8, "max victim turns")
	maxCostEUR := fs.Float64("max-cost-eur", 0.50, "hard cap on accumulated LLM spend in EUR")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *botName == "" || *personaID == "" {
		fmt.Fprintln(os.Stderr, "usage: everychat adversary --bot-name=<n> --persona=<id> [--turns=N] [--max-cost-eur=X.XX]")
		fmt.Fprintln(os.Stderr, "available personas:")
		if personas, err := adversary.Personas(); err == nil {
			for _, p := range personas {
				fmt.Fprintf(os.Stderr, "  %-22s %s\n", p.ID, p.Name)
			}
		}
		return 2
	}
	persona, err := adversary.LoadPersona(*personaID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	if *turns <= 0 {
		fmt.Fprintln(os.Stderr, "--turns must be > 0")
		return 2
	}
	if *maxCostEUR <= 0 {
		fmt.Fprintln(os.Stderr, "--max-cost-eur must be > 0")
		return 2
	}
	maxCostCents := int64(*maxCostEUR * 100)

	dbPath := getenv("EVERYCHAT_DB_PATH", storage.DefaultDSN)
	db, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	evalBot, err := eval.LookupBotByName(ctx, db, *botName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load bot: %v\n", err)
		return 1
	}
	storageBot, err := storage.LoadBot(ctx, db, evalBot.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load bot: %v\n", err)
		return 1
	}

	chat := llm.NewLiteLLMClient(getenv("EVERYCHAT_LITELLM_URL", defaultLiteLLMURL))
	runner := adversary.NewRunner(db, chat, chat)
	rep, err := runner.Run(ctx, storageBot, *persona, adversary.Options{
		TurnCap: *turns, MaxCostCents: maxCostCents,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		if rep != nil {
			fmt.Printf("partial run-id=%d status=%s\n", rep.RunID, rep.Status)
		}
		return 1
	}

	fmt.Printf("\n=== Adversary Run %d  (bot=%s, persona=%s) ===\n", rep.RunID, *botName, persona.ID)
	for _, t := range rep.Turns {
		fmt.Printf("\n[%s · turn %d]\n%s\n", t.Role, t.TurnIndex, t.Content)
	}
	fmt.Printf("\n--- status:  %s\n", rep.Status)
	fmt.Printf("--- verdict: %s\n", rep.Verdict)
	fmt.Printf("--- spend:   %d cents (cap %d)\n", rep.TotalCostCents, maxCostCents)

	if rep.Status == adversary.StatusCompleted && len(rep.Verdict) >= 4 && rep.Verdict[:4] == "PASS" {
		return 0
	}
	return 2
}
