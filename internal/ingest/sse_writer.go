package ingest

import (
	"bytes"
	"sync"
)

// SSELines is an io.Writer that delivers each newline-terminated line to
// a callback. Used by the wizard's crawl-progress endpoint to forward
// Pipeline.Logger output as Server-Sent Events.
//
// Concurrency: Write is safe for multiple goroutines, but the callback
// is invoked under the writer's lock; do not call Write from inside it.
type SSELines struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	OnLine func(line string)
}

// NewSSELines constructs an SSELines that calls fn for every full line
// observed in the byte stream.
func NewSSELines(fn func(string)) *SSELines {
	return &SSELines{OnLine: fn}
}

// Write implements io.Writer. Buffers partial lines until a newline
// arrives.
func (s *SSELines) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := s.buf.Write(p)
	for {
		raw, err := s.buf.ReadBytes('\n')
		if err != nil {
			// No newline yet — push the unread bytes back into the buffer.
			s.buf.Reset()
			s.buf.Write(raw)
			break
		}
		line := string(bytes.TrimRight(raw, "\r\n"))
		if s.OnLine != nil && line != "" {
			s.OnLine(line)
		}
	}
	return n, nil
}

// Flush emits any remaining buffered text as a final line.
func (s *SSELines) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	rest := s.buf.String()
	s.buf.Reset()
	if s.OnLine != nil && rest != "" {
		s.OnLine(rest)
	}
}
