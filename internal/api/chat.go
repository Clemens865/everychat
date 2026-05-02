package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/moderation"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// chatRequest is the JSON body POSTed by the widget runtime.
type chatRequest struct {
	BotToken  string `json:"bot_token"`
	Message   string `json:"message"`
	VisitorID string `json:"visitor_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// chat is the visitor-facing chat endpoint. Wire format mirrors the
// admin-side sandbox SSE shape for symmetry — clients receive
//
//	event: token   data: <delta>
//	event: done    data: {sources, input_tokens, output_tokens}
//	event: error   data: <message>
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" || req.BotToken == "" {
		http.Error(w, "bot_token and message required", http.StatusBadRequest)
		return
	}

	bot, err := storage.LookupBotByEmbedTokenHash(r.Context(), s.db, auth.HashEmbedToken(req.BotToken))
	if errors.Is(err, storage.ErrBotNotFound) {
		http.Error(w, "unknown bot_token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Printf("api/chat: lookup bot: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	origin := originHeader(r)
	if !s.originAllowed(origin, bot.EmbedOriginAllow) {
		http.Error(w, "origin not allowed for this bot", http.StatusForbidden)
		return
	}

	if !s.rate.allow(clientIP(r)) {
		http.Error(w, "rate limit exceeded — try again in a minute", http.StatusTooManyRequests)
		return
	}

	// 0. Moderation gate (input). Phase 3 ships a Noop default; the
	// real Llama Guard 3 8B impl arrives in Phase 5 — same Moderator
	// interface, no caller changes.
	if s.moderator != nil {
		v, err := s.moderator.Check(r.Context(), moderation.RoleUser, req.Message)
		if err != nil {
			log.Printf("api/chat: moderation: %v", err)
			http.Error(w, "moderation check failed", http.StatusInternalServerError)
			return
		}
		if !v.Allowed {
			http.Error(w,
				"Diese Anfrage konnte nicht beantwortet werden. Bitte umformulieren oder den Support kontaktieren.",
				http.StatusUnprocessableEntity)
			return
		}
	}

	// 1. Embed the question.
	vecs, err := s.llm.Embed(r.Context(), []string{req.Message})
	if err != nil || len(vecs) != 1 {
		log.Printf("api/chat: embed: %v", err)
		http.Error(w, "embed failed", http.StatusBadGateway)
		return
	}

	// 2. RAG: top 5 chunks scoped to this bot.
	hits, err := storage.SearchChunks(r.Context(), s.db, bot.ID, vecs[0], 5)
	if err != nil {
		log.Printf("api/chat: search: %v", err)
		http.Error(w, "retrieval failed", http.StatusInternalServerError)
		return
	}

	// 3. Build the system prompt — published wins for visitor traffic
	// (draft is editor-only). Append retrieved chunks as context.
	sys := bot.SystemPrompt
	if strings.TrimSpace(sys) == "" {
		sys = bot.DraftPrompt
	}
	if len(hits) > 0 {
		var ctx strings.Builder
		ctx.WriteString("\n\nRelevante Auszüge aus der Wissensbasis:\n")
		for i, h := range hits {
			fmt.Fprintf(&ctx, "[%d] %s\n%s\n\n", i+1, h.Source, truncate(h.Content, 800))
		}
		sys += ctx.String()
	}

	// 4. SSE stream.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	stream, err := s.llm.Stream(r.Context(), llm.ChatRequest{
		System:      sys,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: req.Message}},
		MaxTokens:   600,
		Temperature: 0.4,
	})
	if err != nil {
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	var answer strings.Builder
	var inTok, outTok int
	for chunk := range stream {
		if chunk.Err != nil {
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", chunk.Err.Error())
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if chunk.Delta != "" {
			answer.WriteString(chunk.Delta)
			for _, line := range strings.Split(chunk.Delta, "\n") {
				_, _ = fmt.Fprintf(w, "event: token\ndata: %s\n", line) //nolint:gosec // G705: SSE wire is not HTML
			}
			_, _ = fmt.Fprint(w, "\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		if chunk.Usage != nil {
			inTok = chunk.Usage.InputTokens
			outTok = chunk.Usage.OutputTokens
		}
	}

	type sourceOut struct {
		Source   string  `json:"source"`
		Excerpt  string  `json:"excerpt"`
		Distance float64 `json:"distance"`
	}
	srcs := make([]sourceOut, 0, len(hits))
	for _, h := range hits {
		srcs = append(srcs, sourceOut{Source: h.Source, Excerpt: truncate(h.Content, 160), Distance: h.Distance})
	}
	doneJSON, _ := json.Marshal(map[string]any{
		"input_tokens":  inTok,
		"output_tokens": outTok,
		"sources":       srcs,
	})
	_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", string(doneJSON))
	if flusher != nil {
		flusher.Flush()
	}

	// 4b. Lead-intent check on the completed turn. If triggered, emit
	// a `lead_capture` SSE event the widget runtime renders as an
	// inline form (per blueprint §3.6 — artifact card in widget).
	if s.intent != nil {
		if m := s.intent.Detect(req.Message, answer.String()); m.Triggered {
			leadEvt, _ := json.Marshal(map[string]any{
				"reason":  m.Reason,
				"summary": truncate(answer.String(), 480),
			})
			_, _ = fmt.Fprintf(w, "event: lead_capture\ndata: %s\n\n", string(leadEvt))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// 5. Persist the visitor turn (best-effort).
	go s.persistTurn(bot.ID, req.VisitorID, req.Message, answer.String(), inTok, outTok)
}

// persistTurn writes a chats + 2 messages rows for a visitor turn.
func (s *Server) persistTurn(botID int64, visitorID, user, assistant string, inTok, outTok int) {
	if visitorID == "" {
		visitorID = "anonymous"
	}
	res, err := s.db.Exec(`INSERT INTO chats (bot_id, visitor_id) VALUES (?, ?)`, botID, visitorID)
	if err != nil {
		log.Printf("api/chat persist: %v", err)
		return
	}
	chatID, _ := res.LastInsertId()
	if _, err := s.db.Exec(
		`INSERT INTO messages (chat_id, role, content, tokens_in) VALUES (?, 'user', ?, ?)`,
		chatID, user, inTok); err != nil {
		log.Printf("api/chat persist user: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO messages (chat_id, role, content, tokens_out) VALUES (?, 'assistant', ?, ?)`,
		chatID, assistant, outTok); err != nil {
		log.Printf("api/chat persist assistant: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
