// Package widget owns the visitor-facing artifact: the embeddable chat
// that runs on a customer's website. Two surfaces:
//
//	/embed.js                      cross-origin loader + <everychat-launcher>
//	                                Web Component, runs ON THE CUSTOMER PAGE
//	/widget/{token}/shell          server-rendered iframe HTML, runs INSIDE
//	                                the iframe (same-origin to everychat)
//	/widget/assets/runtime.js      chat behavior inside the iframe
//	/widget/assets/dompurify.min.js  vendored sanitizer
//
// The two-process split is deliberate: cross-origin embed code is the
// minimum-surface contract (one CSS-isolated launcher button); all chat
// rendering happens inside the iframe under our CSP.
package widget

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"database/sql"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/storage"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets/*
var assetFS embed.FS

// Server hosts the widget routes.
type Server struct {
	db        *sql.DB
	pages     map[string]*template.Template
	assetsSub fs.FS
}

// New constructs a widget Server.
func New(db *sql.DB) (*Server, error) {
	t, err := template.ParseFS(templateFS, "templates/bubble.html")
	if err != nil {
		return nil, fmt.Errorf("widget: parse bubble: %w", err)
	}
	assetsSub, err := fs.Sub(assetFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("widget: assets sub: %w", err)
	}
	return &Server{
		db:        db,
		pages:     map[string]*template.Template{"bubble": t},
		assetsSub: assetsSub,
	}, nil
}

// Routes wires this server's handlers onto the given mux. Implements
// web.RouteRegistrar so it slots into the same http.Server as the
// admin and api surfaces.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/embed.js", s.embedJS)
	mux.Handle("/widget/assets/", http.StripPrefix("/widget/assets/", http.FileServer(http.FS(s.assetsSub))))
	mux.HandleFunc("/widget/", s.shellRouter)
}

// shellRouter dispatches /widget/{token}/shell.
func (s *Server) shellRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/widget/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] != "shell" {
		http.NotFound(w, r)
		return
	}
	s.shell(w, r, parts[0])
}

// shell renders the iframe HTML for a given bot token.
func (s *Server) shell(w http.ResponseWriter, r *http.Request, token string) {
	bot, err := storage.LookupBotByEmbedTokenHash(r.Context(), s.db, auth.HashEmbedToken(token))
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("widget shell: lookup bot: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	theme := decodeTheme(bot.WidgetThemeJSON)

	// Strict CSP: scripts come from same origin only; no inline eval; no
	// remote network calls except the everychat origin (the chat SSE
	// endpoint resolves via the page's own origin so 'self' covers it).
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"connect-src 'self'; "+
			"frame-ancestors *",
	)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	data := map[string]any{
		"BotToken":       token,
		"BotDisplayName": coalesce(theme.Name, bot.Name),
		"Welcome":        coalesce(theme.Welcome, "Guten Tag — wie kann ich helfen?"),
		"StarterPrompts": theme.StarterPrompts,
		"Accent":         coalesce(theme.Accent, "#2f4cff"),
		"Locale":         coalesce(theme.Locale, "de"),
		"PrivacyURL":     nullableString(bot.PrivacyPolicyURL),
		"AGBURL":         nullableString(bot.AGBURL),
		"Avatar":         abbreviate(coalesce(theme.Name, bot.Name), 2),
	}
	if err := s.pages["bubble"].ExecuteTemplate(w, "bubble", data); err != nil {
		log.Printf("widget shell render: %v", err)
	}
}

// embedJS serves the loader bundle. Sprint 2 swaps the Sprint-1 placeholder
// for the real implementation. Cached for 5 minutes; widget changes ship
// via a new commit + redeploy, not via a per-visitor refetch.
func (s *Server) embedJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*") // loader is meant to run cross-origin
	body, err := fs.ReadFile(s.assetsSub, "embed.js")
	if err != nil {
		http.Error(w, "embed.js missing", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

// theme is the on-disk shape of bots.widget_theme_json. Lifted into the
// widget package once Sprint 3 ships the typed Theme struct.
type theme struct {
	Name           string   `json:"name"`
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
	_ = jsonUnmarshal([]byte(raw), &t)
	return t
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func nullableString(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

func abbreviate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return strings.ToUpper(string(r))
	}
	return strings.ToUpper(string(r[:n]))
}
