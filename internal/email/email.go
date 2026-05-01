// Package email defines the Sender interface used to deliver transactional
// mail (magic-link logins, lead notifications, etc.). Phase 1 ships a
// stdout-printing implementation; Phase 5 will add a Postmark client.
package email

import "context"

// Sender delivers a single transactional email. Implementations must be safe
// for concurrent use.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}
