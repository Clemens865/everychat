// Package main is the entrypoint for the everychat-ops provisioning CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/clemenshoenig/everychat/internal/provisioner"
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
	}
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, l)
	}
}
