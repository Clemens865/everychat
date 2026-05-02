// Package api hosts the visitor-facing endpoints (/api/v1/*, /widget/*,
// /embed.js). Separate from internal/web by design: no admin cookie ever
// reaches these handlers, authentication is bot-token + Origin allow-list,
// and the surface lives behind its own rate limiter so a misbehaving
// embed can't degrade the admin experience.
//
// Phase 3 sprint 1 ships:
//   - POST /api/v1/chat                       SSE chat (RAG + streaming)
//   - GET  /api/v1/widget/{token}/config      public theme + welcome + starters
//   - GET  /embed.js                          loader bundle (placeholder until S2)
package api

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/clemenshoenig/everychat/internal/llm"
)

// Server bundles dependencies needed by the visitor handlers.
type Server struct {
	db      *sql.DB
	llm     *llm.LiteLLMClient
	rate    *rateLimiter
	devMode bool // when true, empty embed_origin_allow is treated as "any origin"
}

// New constructs a visitor-API Server.
func New(db *sql.DB, llmClient *llm.LiteLLMClient, devMode bool) *Server {
	return &Server{
		db:      db,
		llm:     llmClient,
		rate:    newRateLimiter(30, 60), // 30 reqs / 60s per IP
		devMode: devMode,
	}
}

// Routes registers /api/v1/* and /embed.js under the given mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.Handle("/api/v1/chat", http.HandlerFunc(s.chat))
	mux.Handle("/api/v1/widget/", http.HandlerFunc(s.widgetConfig))
	mux.Handle("/embed.js", http.HandlerFunc(s.embedJS))
}

// originHeader returns the request's Origin (or Referer-derived fallback).
func originHeader(r *http.Request) string {
	if o := strings.TrimSpace(r.Header.Get("Origin")); o != "" {
		return o
	}
	// Some embeds drop Origin on same-origin POSTs; fall back to Referer's
	// scheme+host so we still have something to validate against.
	if ref := r.Header.Get("Referer"); ref != "" {
		if i := strings.Index(ref, "/"); i >= 0 {
			// crude: scheme://host/...
			tail := ref[i+1:]
			if j := strings.Index(tail, "/"); j >= 0 {
				return ref[:i+1+j]
			}
			return ref
		}
	}
	return ""
}

// originAllowed checks `origin` against the bot's CSV allow-list. Empty
// allow-list in devMode is permissive; in production it blocks everything.
func (s *Server) originAllowed(origin, csv string) bool {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return s.devMode
	}
	for _, e := range strings.Split(csv, ",") {
		if strings.EqualFold(strings.TrimSpace(e), origin) {
			return true
		}
	}
	return false
}

// clientIP best-efforts the visitor IP for rate-limiting.
func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		// First entry is the originating client per RFC 7239; trim port.
		first := strings.TrimSpace(strings.Split(x, ",")[0])
		if i := strings.LastIndex(first, ":"); i >= 0 && !strings.Contains(first, "::") {
			return first[:i]
		}
		return first
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}
