package provisioner

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStub_ProvisionDryRunPrintsPlan(t *testing.T) {
	buf := &bytes.Buffer{}
	p := NewStubProvisioner(buf)

	inst, err := p.Provision(context.Background(), Options{
		Domain:     "bot.test.de",
		OwnerEmail: "me@me.de",
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if inst == nil || inst.Domain != "bot.test.de" {
		t.Fatalf("expected stub instance with domain bot.test.de, got %#v", inst)
	}
	out := buf.String()
	expectations := []string{
		"Provisioning plan for bot.test.de",
		"Create ccx13 instance in eu-central",
		"Install everychat-bin v0.1.0",
		"Configure Caddy for bot.test.de",
		"Send magic-link to me@me.de",
		"[DRY-RUN]",
	}
	for _, want := range expectations {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestStub_ProvisionWithoutDryRunPrintsStubNotice(t *testing.T) {
	buf := &bytes.Buffer{}
	p := NewStubProvisioner(buf)
	if _, err := p.Provision(context.Background(), Options{
		Domain:     "bot.example.de",
		OwnerEmail: "demo@example.de",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[STUB] No real API calls") {
		t.Fatalf("missing stub notice:\n%s", buf.String())
	}
}

func TestStub_ProvisionRejectsMissingFields(t *testing.T) {
	buf := &bytes.Buffer{}
	p := NewStubProvisioner(buf)
	if _, err := p.Provision(context.Background(), Options{Domain: "a.de"}); err == nil {
		t.Fatal("expected error for missing owner email")
	}
	if _, err := p.Provision(context.Background(), Options{OwnerEmail: "x@y.de"}); err == nil {
		t.Fatal("expected error for missing domain")
	}
}

func TestStub_List(t *testing.T) {
	buf := &bytes.Buffer{}
	p := NewStubProvisioner(buf)
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
	if !strings.Contains(buf.String(), "No instances yet") {
		t.Fatalf("expected 'No instances yet' notice:\n%s", buf.String())
	}
}

func TestLive_AlwaysReturnsDisabled(t *testing.T) {
	l := NewLiveProvisioner("dummy-token")
	if _, err := l.Provision(context.Background(), Options{Domain: "x.de", OwnerEmail: "x@y.de"}); !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("Provision: got %v, want ErrLiveDisabled", err)
	}
	if _, err := l.List(context.Background()); !errors.Is(err, ErrLiveDisabled) {
		t.Fatalf("List: got %v, want ErrLiveDisabled", err)
	}
}
