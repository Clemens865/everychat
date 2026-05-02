package integrations

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignBody_Deterministic(t *testing.T) {
	a := SignBody("k", []byte("hello"))
	b := SignBody("k", []byte("hello"))
	if a != b {
		t.Fatal("HMAC not deterministic")
	}
	if SignBody("k", []byte("hello")) == SignBody("k2", []byte("hello")) {
		t.Fatal("different keys must yield different sigs")
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	sig := "sha256=" + SignBody("topsecret", body)
	if !VerifySignature("topsecret", body, sig) {
		t.Fatal("valid sig rejected")
	}
	// Bare hex without prefix also accepted.
	if !VerifySignature("topsecret", body, SignBody("topsecret", body)) {
		t.Fatal("bare hex sig rejected")
	}
	if VerifySignature("topsecret", body, "sha256=00") {
		t.Fatal("bad sig accepted")
	}
	if VerifySignature("wrong-key", body, sig) {
		t.Fatal("sig from wrong key accepted")
	}
}

func TestDeliver_HappyPath(t *testing.T) {
	var seenSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSig = r.Header.Get("X-Everychat-Signature")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	// httptest.NewServer binds to 127.0.0.1 — devMode=true allows it.
	c := New(true)
	body := []byte(`{"a":1}`)
	resp, err := c.Deliver(context.Background(), srv.URL, "secret", body)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if string(resp) != "ok" {
		t.Errorf("body: %q", string(resp))
	}
	if !strings.HasPrefix(seenSig, "sha256=") {
		t.Errorf("missing signature header: %q", seenSig)
	}
	if !VerifySignature("secret", body, seenSig) {
		t.Error("signature did not verify")
	}
}

func TestDeliver_RejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(true)
	if _, err := c.Deliver(context.Background(), srv.URL, "k", []byte(`{}`)); !errors.Is(err, ErrNon2xxResponse) {
		t.Fatalf("got %v, want ErrNon2xxResponse", err)
	}
}

func TestDeliver_RejectsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Manual 302 — set Location header + status; httptest's mux
		// doesn't break this open like http.Redirect would for a
		// synthetic Request.
		w.Header().Set("Location", "http://example.test/elsewhere")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c := New(true)
	_, err := c.Deliver(context.Background(), srv.URL, "k", []byte(`{}`))
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("got %v, want ErrRedirectBlocked", err)
	}
}

func TestDeliver_RejectsBadScheme(t *testing.T) {
	c := New(true)
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/", "javascript:alert(1)"} {
		if _, err := c.Deliver(context.Background(), u, "k", []byte(`{}`)); !errors.Is(err, ErrSchemeNotAllowed) && !errors.Is(err, ErrInvalidWebhookURL) {
			t.Errorf("scheme %s: got %v", u, err)
		}
	}
}

func TestDeliver_RejectsInvalidURL(t *testing.T) {
	c := New(true)
	if _, err := c.Deliver(context.Background(), "not a url", "k", []byte(`{}`)); err == nil {
		t.Fatal("expected error for malformed URL")
	}
}

// SSRF guard tests — production mode (devMode=false). Each must REJECT.
func TestSSRF_RejectsPrivateRanges(t *testing.T) {
	c := New(false)
	cases := []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
		"http://169.254.169.254/", // AWS/GCP IMDS
		"http://[::1]/",
		"http://[fe80::1]/",
		"http://[fc00::1]/",
		"http://0.0.0.0/",
		"http://224.0.0.1/", // multicast
		"http://192.0.2.1/", // documentation
		"http://198.51.100.1/",
		"http://203.0.113.1/",
		"http://100.64.0.1/", // CGNAT
	}
	for _, u := range cases {
		_, err := c.Deliver(context.Background(), u, "k", []byte(`{}`))
		if !errors.Is(err, ErrPrivateAddressBlocked) {
			t.Errorf("%s: got %v, want ErrPrivateAddressBlocked", u, err)
		}
	}
}

func TestSSRF_DevModeAllowsLocalhost(t *testing.T) {
	c := New(true)
	if c.ipBlocked(net.ParseIP("127.0.0.1")) {
		t.Error("devMode should not block 127.0.0.1")
	}
	if c.ipBlocked(net.ParseIP("10.0.0.1")) {
		t.Error("devMode should not block RFC1918")
	}
	// Even in devMode, link-local + multicast remain blocked because
	// they're never legitimate webhook targets.
	if !c.ipBlocked(net.ParseIP("169.254.169.254")) {
		t.Error("devMode must still block IMDS")
	}
}

func TestSSRF_AllowsPublicIP(t *testing.T) {
	c := New(false)
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if c.ipBlocked(net.ParseIP(ip)) {
			t.Errorf("public IP %s falsely blocked", ip)
		}
	}
}
