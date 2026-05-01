package email

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// StdoutSender writes every email to a configurable io.Writer (defaulting to
// os.Stdout). It is the development-mode Sender — production deployments
// swap in a Postmark-backed implementation in Phase 5.
type StdoutSender struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdoutSender constructs a StdoutSender writing to os.Stdout.
func NewStdoutSender() *StdoutSender {
	return &StdoutSender{w: os.Stdout}
}

// NewStdoutSenderTo lets tests inject their own writer.
func NewStdoutSenderTo(w io.Writer) *StdoutSender {
	return &StdoutSender{w: w}
}

// Send implements Sender.
func (s *StdoutSender) Send(_ context.Context, to, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(s.w,
		"\n──── EVERYCHAT EMAIL (dev / stdout sender) ────\n"+
			"At:      %s\n"+
			"To:      %s\n"+
			"Subject: %s\n"+
			"\n%s\n"+
			"───────────────────────────────────────────────\n\n",
		time.Now().Format(time.RFC3339), to, subject, body,
	)
	return err
}
