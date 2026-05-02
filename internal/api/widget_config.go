package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// publicTheme is the slice of bot state safe to ship to the visitor's
// browser. Crucially does NOT include system_prompt or draft_prompt —
// the prompts are admin-only and never leave the server.
type publicTheme struct {
	BotToken       string   `json:"bot_token"`
	Name           string   `json:"name"`
	Template       string   `json:"template"` // 'bubble' | 'inline'
	WelcomeMessage string   `json:"welcome_message"`
	StarterPrompts []string `json:"starter_prompts"`
	Accent         string   `json:"accent"`
	Locale         string   `json:"locale"`
	PrivacyURL     string   `json:"privacy_url,omitempty"`
	AGBURL         string   `json:"agb_url,omitempty"`
}

// widgetConfig serves GET /api/v1/widget/{token}/config.
//
// Returns the public theme JSON the widget runtime needs to render
// itself. A bad token returns 404 (not 401 — we don't acknowledge token
// existence to unauthenticated callers).
func (s *Server) widgetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// /api/v1/widget/{token}/config
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/widget/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[1] != "config" {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	if token == "" {
		http.NotFound(w, r)
		return
	}

	bot, err := storage.LookupBotByEmbedTokenHash(r.Context(), s.db, auth.HashEmbedToken(token))
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Origin check — even reading the public theme is gated to allowed
	// origins so we don't help token-fishing scanners enumerate.
	origin := originHeader(r)
	if !s.originAllowed(origin, bot.EmbedOriginAllow) {
		http.Error(w, "origin not allowed for this bot", http.StatusForbidden)
		return
	}

	theme := decodeTheme(bot.WidgetThemeJSON)
	out := publicTheme{
		BotToken:       token,
		Name:           bot.Name,
		Template:       coalesce(bot.WidgetTemplate, "bubble"),
		WelcomeMessage: coalesce(theme.Welcome, "Guten Tag — wie kann ich helfen?"),
		StarterPrompts: theme.StarterPrompts,
		Accent:         coalesce(theme.Accent, "#2f4cff"),
		Locale:         coalesce(theme.Locale, "de"),
	}
	if bot.PrivacyPolicyURL.Valid {
		out.PrivacyURL = bot.PrivacyPolicyURL.String
	}
	if bot.AGBURL.Valid {
		out.AGBURL = bot.AGBURL.String
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// embedJS serves the bundled loader. Sprint 1 ships a placeholder; the
// real bundle drops in Sprint 2.
func (s *Server) embedJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(`/* everychat embed.js — placeholder. Sprint 2 ships the bundled runtime. */
(function () {
  var s = document.currentScript;
  var bot = s && s.dataset && s.dataset.bot;
  console.log("[everychat] embed loader stub; bot=" + (bot || "(missing)"));
})();
`))
}

// theme is the on-disk shape of bots.widget_theme_json. Kept private to
// the api package for now; Sprint 3 lifts it into internal/widget.
type theme struct {
	Welcome        string   `json:"welcome"`
	StarterPrompts []string `json:"starter_prompts"`
	Accent         string   `json:"accent"`
	Locale         string   `json:"locale"`
}

func decodeTheme(raw string) theme {
	var t theme
	if raw == "" {
		return t
	}
	_ = json.Unmarshal([]byte(raw), &t)
	return t
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
