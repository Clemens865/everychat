// Package integrations holds outbound delivery mechanisms (webhook
// today; HubSpot Forms in Phase 5). Two security boundaries live here:
// SSRF defense before any HTTP call, and HMAC signing on the request
// itself so receivers can verify provenance without sharing TLS certs.
package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Errors surfaced by webhook delivery. Tested explicitly; the dispatcher
// uses errors.Is to decide retry vs permanent-fail.
var (
	ErrInvalidWebhookURL     = errors.New("webhook: invalid URL")
	ErrSchemeNotAllowed      = errors.New("webhook: only http/https schemes allowed")
	ErrPrivateAddressBlocked = errors.New("webhook: target resolves to a private/reserved address")
	ErrNoSuchHost            = errors.New("webhook: target host does not resolve")
	ErrNon2xxResponse        = errors.New("webhook: non-2xx response")
	ErrRedirectBlocked       = errors.New("webhook: redirects are not followed")
)

// WebhookClient delivers signed POSTs to operator-configured URLs.
// Constructed once; safe for concurrent use.
type WebhookClient struct {
	httpClient *http.Client
	resolver   *net.Resolver
	// devMode allows localhost / RFC1918 targets when EVERYCHAT_DEV_MODE=1.
	// Production: false; SSRF guard is full-strength.
	devMode bool
}

// New constructs a WebhookClient with sane timeouts and a redirect-
// blocking transport.
func New(devMode bool) *WebhookClient {
	return &WebhookClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 5 * time.Second,
				}).DialContext,
				MaxIdleConns:          16,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 8 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		resolver: net.DefaultResolver,
		devMode:  devMode,
	}
}

// Deliver POSTs `body` (already-marshalled JSON) to `target` with the
// signature header `X-Everychat-Signature: sha256=<hex>` computed over
// `secret + body`. Returns the response body for the caller to log.
//
// Security boundaries enforced before the call:
//  1. URL parses cleanly
//  2. Scheme is http or https
//  3. Resolved IP is not in any private / reserved / metadata range
//     (in production; localhost allowed in devMode for fixture tests)
//  4. Redirects are not followed (a 30x triggers ErrRedirectBlocked)
//  5. Connect / response timeouts capped at 5s / 8s respectively
func (c *WebhookClient) Deliver(ctx context.Context, target, secret string, body []byte) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWebhookURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrSchemeNotAllowed
	}

	// SSRF guard: resolve the host and verify EVERY returned address is
	// publicly routable. Even one private IP in the answer set rejects
	// the request — defends against DNS rebinding (resolve to public
	// here, private later).
	if err := c.checkHost(ctx, u.Hostname()); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "EverychatWebhook/0.1")
	req.Header.Set("X-Everychat-Signature", "sha256="+SignBody(secret, body))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webhook: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return respBody, ErrRedirectBlocked
	}
	if resp.StatusCode/100 != 2 {
		return respBody, fmt.Errorf("%w: %d", ErrNon2xxResponse, resp.StatusCode)
	}
	return respBody, nil
}

// SignBody returns the lowercase-hex SHA-256 HMAC of `body` keyed by
// `secret`. Exported so dispatcher tests can compute expected sigs.
func SignBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature returns true if `sig` (a `sha256=...` header value or
// a bare hex digest) matches body+secret. Constant-time comparison.
// Useful for receivers verifying webhooks Everychat sent.
func VerifySignature(secret string, body []byte, sig string) bool {
	sig = strings.TrimPrefix(sig, "sha256=")
	expected := SignBody(secret, body)
	return hmac.Equal([]byte(sig), []byte(expected))
}

// checkHost rejects hosts that resolve to any private / reserved /
// metadata address. In devMode (EVERYCHAT_DEV_MODE=1) localhost +
// RFC1918 are allowed so fixture tests can post to a local server.
func (c *WebhookClient) checkHost(ctx context.Context, host string) error {
	if host == "" {
		return ErrInvalidWebhookURL
	}
	// Direct IP literal — check it without DNS.
	if ip := net.ParseIP(host); ip != nil {
		if c.ipBlocked(ip) {
			return ErrPrivateAddressBlocked
		}
		return nil
	}

	addrs, err := c.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoSuchHost, err)
	}
	if len(addrs) == 0 {
		return ErrNoSuchHost
	}
	for _, a := range addrs {
		if c.ipBlocked(a.IP) {
			return ErrPrivateAddressBlocked
		}
	}
	return nil
}

// ipBlocked is the SSRF allow-list inverter: returns true for any IP
// that should not be reachable from a customer-configurable webhook URL.
//
// Blocks (RFC 6890 reserved space + cloud metadata):
//   - Loopback (127/8, ::1)
//   - Link-local (169.254/16, fe80::/10) — covers IMDS for AWS/GCP/Azure
//   - RFC1918 private (10/8, 172.16/12, 192.168/16)
//   - RFC4193 unique-local IPv6 (fc00::/7)
//   - Multicast (224/4, ff00::/8)
//   - Unspecified (0/8, ::)
//   - Documentation/test (192.0.2/24, 198.51.100/24, 203.0.113/24, 2001:db8::/32)
//   - 100.64/10 carrier-grade NAT (cgnat) — sometimes used internally
func (c *WebhookClient) ipBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if c.devMode && (ip.IsLoopback() || ip.IsPrivate()) {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	// CGNAT 100.64.0.0/10 — not covered by IsPrivate.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	// Documentation ranges.
	for _, cidr := range []string{
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"2001:db8::/32",
	} {
		_, n, _ := net.ParseCIDR(cidr)
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}
