// Package provisioner abstracts the act of standing up a new customer
// instance. Phase 1 ships a stub that prints a plan; Phase 5 wires the real
// hcloud client (the import is kept intentionally to lock the version in
// go.mod and to keep the LiveProvisioner skeleton compileable).
package provisioner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	_ "github.com/hetznercloud/hcloud-go/v2/hcloud" // imported for go.mod pinning; activated in Phase 5
)

// Options describes the customer instance to provision.
type Options struct {
	Domain     string
	OwnerEmail string
	ServerType string // e.g. ccx13
	Location   string // e.g. eu-central
	BinVersion string // e.g. v0.1.0
	DryRun     bool
}

// Instance is a thin description of a provisioned VM.
type Instance struct {
	ID         string
	Domain     string
	IPv4       string
	IPv6       string
	ServerType string
	Location   string
	CreatedAt  time.Time
}

// Provisioner stands up customer instances. Implementations must be safe for
// concurrent use.
type Provisioner interface {
	Provision(ctx context.Context, opts Options) (*Instance, error)
	List(ctx context.Context) ([]Instance, error)
}

// ErrLiveDisabled is returned by LiveProvisioner until Phase 5 enables real
// API calls.
var ErrLiveDisabled = errors.New("provisioner: live provisioning disabled until Phase 5")

// StubProvisioner records intent without making any external calls. Output
// goes to a configurable io.Writer (defaulting to os.Stdout in callers).
type StubProvisioner struct {
	out io.Writer
}

// NewStubProvisioner constructs a StubProvisioner writing to w.
func NewStubProvisioner(w io.Writer) *StubProvisioner {
	return &StubProvisioner{out: w}
}

// Provision implements Provisioner. It prints a plan; nothing is created.
func (s *StubProvisioner) Provision(_ context.Context, opts Options) (*Instance, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	st := opts.ServerType
	if st == "" {
		st = "ccx13"
	}
	loc := opts.Location
	if loc == "" {
		loc = "eu-central"
	}
	bin := opts.BinVersion
	if bin == "" {
		bin = "v0.1.0"
	}

	plan := []string{
		fmt.Sprintf("• Create %s instance in %s", st, loc),
		fmt.Sprintf("• Install everychat-bin %s", bin),
		fmt.Sprintf("• Configure Caddy for %s", opts.Domain),
		fmt.Sprintf("• Send magic-link to %s", opts.OwnerEmail),
	}
	_, _ = fmt.Fprintf(s.out, "Provisioning plan for %s:\n", opts.Domain)
	for _, line := range plan {
		_, _ = fmt.Fprintln(s.out, "  "+line)
	}
	if opts.DryRun {
		_, _ = fmt.Fprintln(s.out, "\n[DRY-RUN] No changes attempted.")
	} else {
		_, _ = fmt.Fprintln(s.out, "\n[STUB] No real API calls made — set EVERYCHAT_HETZNER_LIVE=1 in Phase 5 to enable.")
	}

	return &Instance{
		ID:         "stub-" + strings.ReplaceAll(opts.Domain, ".", "-"),
		Domain:     opts.Domain,
		IPv4:       "203.0.113.10", // RFC5737 documentation address — never real
		IPv6:       "2001:db8::1",
		ServerType: st,
		Location:   loc,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// List implements Provisioner. Stub always reports zero instances.
func (s *StubProvisioner) List(_ context.Context) ([]Instance, error) {
	_, _ = fmt.Fprintln(s.out, "No instances yet (stubbed)")
	return nil, nil
}

// LiveProvisioner is a placeholder. It returns ErrLiveDisabled for every
// call until Phase 5 wires the real hcloud client.
type LiveProvisioner struct {
	APIToken string
}

// NewLiveProvisioner constructs the placeholder. Currently a no-op.
func NewLiveProvisioner(token string) *LiveProvisioner { return &LiveProvisioner{APIToken: token} }

// Provision implements Provisioner.
func (l *LiveProvisioner) Provision(_ context.Context, _ Options) (*Instance, error) {
	return nil, ErrLiveDisabled
}

// List implements Provisioner.
func (l *LiveProvisioner) List(_ context.Context) ([]Instance, error) {
	return nil, ErrLiveDisabled
}

func (o Options) validate() error {
	if strings.TrimSpace(o.Domain) == "" {
		return errors.New("provisioner: --domain is required")
	}
	if strings.TrimSpace(o.OwnerEmail) == "" {
		return errors.New("provisioner: --owner-email is required")
	}
	return nil
}
