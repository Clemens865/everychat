// Package main is the entrypoint for the everychat-ops provisioning CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/clemenshoenig/everychat/internal/crawler"
	"github.com/clemenshoenig/everychat/internal/eval"
	"github.com/clemenshoenig/everychat/internal/ingest"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/provisioner"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// Version is the binary version. Overridable via -ldflags "-X main.Version=...".
var Version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "version", "-v", "--version":
		fmt.Printf("everychat-ops %s\n", Version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "provision":
		if err := runProvision(args); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := runList(args); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "eval":
		if err := runEval(args); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "crawl":
		if err := runCrawl(args); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func runProvision(args []string) error {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	domain := fs.String("domain", "", "customer domain (required)")
	owner := fs.String("owner-email", "", "owner email for magic-link (required)")
	dryRun := fs.Bool("dry-run", false, "preview the plan; never call any APIs")
	if err := fs.Parse(args); err != nil {
		return err
	}

	prov := pickProvisioner()
	_, err := prov.Provision(context.Background(), provisioner.Options{
		Domain:     *domain,
		OwnerEmail: *owner,
		DryRun:     *dryRun,
	})
	return err
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, err := pickProvisioner().List(context.Background())
	return err
}

func runEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	botID := fs.Int64("bot-id", 0, "bot id (mutually exclusive with --bot-name)")
	botName := fs.String("bot-name", "", "bot name (e.g. steuerkanzlei-demo)")
	questions := fs.String("questions", "", "path to golden-questions YAML (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *questions == "" {
		return errors.New("--questions is required")
	}
	if *botID == 0 && *botName == "" {
		return errors.New("either --bot-id or --bot-name is required")
	}

	dbPath := os.Getenv("EVERYCHAT_DB_PATH")
	if dbPath == "" {
		dbPath = storage.DefaultDSN
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
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
		return err
	}

	litellmURL := os.Getenv("EVERYCHAT_LITELLM_URL")
	if litellmURL == "" {
		litellmURL = "http://127.0.0.1:4000"
	}
	chat := llm.NewLiteLLMClient(litellmURL)

	report, err := eval.NewRunner(db, chat).Run(ctx, bot, *questions)
	if err != nil {
		return err
	}
	eval.PrintScorecard(os.Stdout, report)
	if !report.MeetsThreshold() {
		os.Exit(2) // distinct from arg-parse exit code 1
	}
	return nil
}

func runCrawl(args []string) error {
	fs := flag.NewFlagSet("crawl", flag.ContinueOnError)
	domain := fs.String("domain", "", "seed URL to crawl (e.g. https://kanzlei-mustermann.de)")
	botID := fs.Int64("bot-id", 0, "bot id to ingest into (mutually exclusive with --bot-name)")
	botName := fs.String("bot-name", "", "bot name to ingest into")
	maxPages := fs.Int("max-pages", 0, "max pages to fetch (default 200)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *domain == "" {
		return errors.New("--domain is required")
	}
	if *botID == 0 && *botName == "" {
		return errors.New("either --bot-id or --bot-name is required")
	}

	dbPath := os.Getenv("EVERYCHAT_DB_PATH")
	if dbPath == "" {
		dbPath = storage.DefaultDSN
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
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
		return err
	}

	litellmURL := os.Getenv("EVERYCHAT_LITELLM_URL")
	if litellmURL == "" {
		litellmURL = "http://127.0.0.1:4000"
	}
	embedder := llm.NewLiteLLMClient(litellmURL)

	pipe := ingest.New(db, embedder)
	pipe.Logger = os.Stdout

	stats, err := pipe.Ingest(ctx, bot.ID, *domain, crawler.Options{MaxPages: *maxPages})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "\n✔ Ingested %d pages → %d chunks (%d embedding calls)\n",
		stats.PagesFetched, stats.ChunksProduced, stats.EmbeddingsCalls)
	return nil
}

// pickProvisioner returns a Live or Stub Provisioner depending on
// EVERYCHAT_HETZNER_LIVE. Phase 1 always returns the stub; Live still
// returns ErrLiveDisabled until Phase 5.
func pickProvisioner() provisioner.Provisioner {
	if os.Getenv("EVERYCHAT_HETZNER_LIVE") == "1" {
		return provisioner.NewLiveProvisioner(os.Getenv("HCLOUD_TOKEN"))
	}
	return provisioner.NewStubProvisioner(os.Stdout)
}

func usage(w *os.File) {
	lines := []string{
		"everychat-ops — provisioning CLI for Everychat",
		"",
		"usage: everychat-ops <subcommand> [flags]",
		"",
		"subcommands:",
		"  version    print the binary version",
		"  help       show this help",
		"  provision  --domain <d> --owner-email <e> [--dry-run]",
		"  list       list customer instances (stubbed in Phase 1)",
		"  eval       --bot-name=<n> --questions=<path>  run golden-questions and print a scorecard",
		"  crawl      --bot-name=<n> --domain=<url>      crawl a site and fill the bot's KB (re-run replaces chunks)",
	}
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, l)
	}
}
