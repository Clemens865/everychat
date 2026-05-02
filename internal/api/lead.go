package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/lead"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// leadRequest is what the widget posts when the visitor submits the
// lead-capture artifact form.
type leadRequest struct {
	BotToken  string `json:"bot_token"`
	VisitorID string `json:"visitor_id,omitempty"`
	ChatID    int64  `json:"chat_id,omitempty"`
	Email     string `json:"email"`
	Name      string `json:"name,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// leadCapture handles POST /api/v1/lead. Same auth shape as /chat:
// bot_token + origin allow-list + per-IP rate limit.
func (s *Server) leadCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)

	var req leadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.BotToken == "" {
		http.Error(w, "bot_token and email required", http.StatusBadRequest)
		return
	}

	bot, err := storage.LookupBotByEmbedTokenHash(r.Context(), s.db, auth.HashEmbedToken(req.BotToken))
	if errors.Is(err, storage.ErrBotNotFound) {
		http.Error(w, "unknown bot_token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Printf("api/lead: lookup bot: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !s.originAllowed(originHeader(r), bot.EmbedOriginAllow) {
		http.Error(w, "origin not allowed for this bot", http.StatusForbidden)
		return
	}
	if !s.rate.allow(clientIP(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	if s.lead == nil {
		log.Printf("api/lead: dispatcher not configured")
		http.Error(w, "lead capture not configured", http.StatusServiceUnavailable)
		return
	}

	leadID, err := s.lead.Capture(r.Context(), lead.Lead{
		BotID:     bot.ID,
		ChatID:    req.ChatID,
		VisitorID: req.VisitorID,
		Email:     req.Email,
		Name:      req.Name,
		Summary:   req.Summary,
	})
	if err != nil {
		log.Printf("api/lead: capture: %v", err)
		http.Error(w, "lead capture failed", http.StatusInternalServerError)
		return
	}

	// Best-effort: process the queue inline so the founder sees the
	// webhook delivery happen on the same turn the visitor submitted.
	// We deliberately detach from r.Context() because the visitor's
	// response is already sent before this goroutine completes — the
	// short timeout below caps total work without depending on the
	// caller's deadline. In Phase 5 this becomes a long-lived worker.
	// G118: deliberately detached from r.Context() — the visitor response
	// is sent before delivery completes; we cap with a fresh 30s deadline.
	go func() { //nolint:gosec // G118
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.lead.ProcessOnce(ctx, 1); err != nil {
			log.Printf("api/lead: ProcessOnce: %v", err)
		}
	}()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"lead_id": leadID, "status": "queued"})
}
