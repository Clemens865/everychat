// Package web wires HTTP handlers, embedded HTML templates, and static
// assets for the Everychat admin UI. Phase 1 delivers magic-link login plus
// an empty admin shell; richer UI lands in Phase 2.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/auth"
	"github.com/clemenshoenig/everychat/internal/corpus"
	"github.com/clemenshoenig/everychat/internal/crawler"
	"github.com/clemenshoenig/everychat/internal/eval"
	"github.com/clemenshoenig/everychat/internal/ingest"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/prompt"
	"github.com/clemenshoenig/everychat/internal/storage"
	"github.com/clemenshoenig/everychat/internal/widget"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// CSRFCookieName guards the login POST against CSRF.
const CSRFCookieName = "everychat_csrf"

// Server bundles dependencies needed by the HTTP handlers.
type Server struct {
	pages     map[string]*template.Template
	links     *auth.MagicLinks
	sessions  *auth.Sessions
	db        *sql.DB
	prompt    *prompt.Generator
	suggester *widget.Suggester
	llm       *llm.LiteLLMClient
	staticSub fs.FS
}

// New constructs a Server with the provided dependencies.
func New(db *sql.DB, links *auth.MagicLinks, sessions *auth.Sessions, gen *prompt.Generator, suggester *widget.Suggester, llmClient *llm.LiteLLMClient) (*Server, error) {
	// Phase 1 pages share layout.html; the editor uses its own layout.
	type pagespec struct{ name, layout string }
	specs := []pagespec{
		{"login.html", "layout.html"},
		{"login_sent.html", "layout.html"},
		{"admin_home.html", "layout.html"},
		{"editor.html", "layout_editor.html"},
		{"wizard.html", "layout.html"},
	}
	pages := make(map[string]*template.Template, len(specs))
	for _, sp := range specs {
		t, err := template.ParseFS(templateFS, "templates/"+sp.layout, "templates/"+sp.name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", sp.name, err)
		}
		pages[sp.name] = t
	}
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return &Server{
		pages:     pages,
		links:     links,
		sessions:  sessions,
		db:        db,
		prompt:    gen,
		suggester: suggester,
		llm:       llmClient,
		staticSub: staticSub,
	}, nil
}

// RouteRegistrar lets callers attach extra routes (e.g. the visitor-API
// surface) to the same mux as the admin handlers, so a single
// http.Server hosts both. Phase 3 wires the api.Server through this hook.
type RouteRegistrar interface {
	Routes(mux *http.ServeMux)
}

// Routes returns the configured http.Handler with all routes wired up.
// `extras` lets callers register additional routes on the same mux.
func (s *Server) Routes(extras ...RouteRegistrar) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticSub))))

	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/auth/verify", s.verify)
	mux.HandleFunc("/logout", s.logout)
	mux.Handle("/admin", s.sessions.Require(http.HandlerFunc(s.admin)))
	mux.Handle("/admin/bots/", s.sessions.Require(http.HandlerFunc(s.botRoutes)))
	mux.Handle("/admin/visitors/", s.sessions.Require(http.HandlerFunc(s.visitorRoutes)))

	for _, e := range extras {
		e.Routes(mux)
	}

	mux.HandleFunc("/", s.root)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		csrf := s.issueCSRF(w, r)
		s.render(w, "login.html", map[string]any{
			"Title": "Sign in",
			"CSRF":  csrf,
		})
	case http.MethodPost:
		// Cap login bodies; nothing legitimate is larger than a kilobyte.
		r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
		if err := s.checkCSRF(r); err != nil {
			s.renderLoginError(w, r, "Your form expired — please try again.")
			return
		}
		if err := r.ParseForm(); err != nil {
			s.renderLoginError(w, r, "Invalid request.")
			return
		}
		addr := strings.TrimSpace(r.PostFormValue("email"))
		if err := s.links.RequestLink(r.Context(), addr); err != nil {
			if errors.Is(err, auth.ErrInvalidEmail) {
				s.renderLoginError(w, r, "Please enter a valid email address.")
				return
			}
			log.Printf("magic link request: %v", err)
			s.renderLoginError(w, r, "Something went wrong. Please try again.")
			return
		}
		s.render(w, "login_sent.html", map[string]any{"Title": "Check your terminal"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	addr, err := s.links.VerifyLink(r.Context(), token)
	if err != nil {
		log.Printf("magic link verify: %v", err)
		http.Redirect(w, r, "/login?err=link", http.StatusSeeOther)
		return
	}
	sessionToken, err := s.sessions.Create(r.Context(), addr)
	if err != nil {
		log.Printf("session create: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.sessions.SetCookie(w, sessionToken)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.ClearCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	addr := auth.SessionEmail(r.Context())
	bots, err := storage.ListBots(r.Context(), s.db)
	if err != nil {
		log.Printf("admin: list bots: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "admin_home.html", map[string]any{
		"Title": "Admin",
		"Email": addr,
		"Bots":  bots,
	})
}

// botRoutes is a tiny path router for /admin/bots/...
// We avoid a third-party router for now; chi/mux can come in Phase 3.
func (s *Server) botRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/bots/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	// Wizard endpoints sit under /admin/bots/new — handle before
	// numeric-id parsing.
	if rest == "new" {
		switch r.Method {
		case http.MethodGet:
			s.wizardStart(w, r)
		case http.MethodPost:
			s.wizardCreate(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tail := ""
	if len(parts) > 1 {
		tail = strings.Join(parts[1:], "/")
	}

	switch tail {
	case "":
		if r.Method == http.MethodGet {
			s.editor(w, r, id)
			return
		}
	case "prompt/draft":
		if r.Method == http.MethodPost {
			s.savePromptDraft(w, r, id)
			return
		}
	case "prompt/generate":
		if r.Method == http.MethodPost {
			s.generatePrompt(w, r, id)
			return
		}
	case "prompt/publish":
		if r.Method == http.MethodPost {
			s.publishPrompt(w, r, id)
			return
		}
	case "compliance":
		if r.Method == http.MethodPost {
			s.saveCompliance(w, r, id)
			return
		}
	case "evals/run":
		if r.Method == http.MethodPost {
			s.runEvals(w, r, id)
			return
		}
	case "sandbox":
		if r.Method == http.MethodPost {
			s.sandboxStream(w, r, id)
			return
		}
	case "wizard/crawl":
		if r.Method == http.MethodGet {
			s.wizardCrawlStream(w, r, id)
			return
		}
	case "wizard/draft":
		if r.Method == http.MethodPost {
			s.wizardDraft(w, r, id)
			return
		}
	case "embed/token":
		if r.Method == http.MethodPost {
			s.regenEmbedToken(w, r, id)
			return
		}
	case "embed/origins":
		if r.Method == http.MethodPost {
			s.saveEmbedOrigins(w, r, id)
			return
		}
	case "widget/template":
		if r.Method == http.MethodPost {
			s.saveWidgetTemplate(w, r, id)
			return
		}
	case "widget/theme":
		if r.Method == http.MethodPost {
			s.saveWidgetTheme(w, r, id)
			return
		}
	case "widget/theme/suggest":
		if r.Method == http.MethodPost {
			s.suggestWidgetTheme(w, r, id)
			return
		}
	case "dsgvo/doc-url":
		if r.Method == http.MethodPost {
			s.saveDSGVODocURL(w, r, id)
			return
		}
	}
	http.NotFound(w, r)
}

// editor renders the bot-editor workbench.
func (s *Server) editor(w http.ResponseWriter, r *http.Request, id int64) {
	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("editor: load bot %d: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	chunkCount, _ := storage.CountChunks(r.Context(), s.db, id)

	missing := 0
	if !bot.PrivacyPolicyURL.Valid || strings.TrimSpace(bot.PrivacyPolicyURL.String) == "" {
		missing++
	}
	if !bot.AGBURL.Valid || strings.TrimSpace(bot.AGBURL.String) == "" {
		missing++
	}

	gateBlocked := false
	gateReason := "Goldfragen ausführen, dann veröffentlichen."
	if missing > 0 {
		gateReason = fmt.Sprintf("%d Compliance-Felder offen.", missing)
	}

	// JSON-encode prompts for the client-side tab swap. We render them
	// inside <script type="application/json"> blocks (see editor.html)
	// rather than splicing into a JS expression — keeps the boundary
	// safe even though only authenticated admins can edit prompts.
	draftJSON, _ := json.Marshal(bot.DraftPrompt)
	publishedJSON, _ := json.Marshal(bot.SystemPrompt)

	dsgvoURL, _ := storage.LoadDSGVODocURL(r.Context(), s.db, id)
	s.render(w, "editor.html", map[string]any{
		"Title":               bot.Name,
		"Email":               auth.SessionEmail(r.Context()),
		"Bot":                 bot,
		"Theme":               widget.DecodeTheme(bot.WidgetThemeJSON),
		"DSGVODocURL":         dsgvoURL,
		"ChunkCount":          chunkCount,
		"ComplianceMissing":   missing,
		"GateBlocked":         gateBlocked,
		"GateReason":          gateReason,
		"LastEvalLabel":       "noch nie",
		"DraftPromptJSON":     string(draftJSON),
		"PublishedPromptJSON": string(publishedJSON),
	})
}

func (s *Server) savePromptDraft(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	draft := r.PostFormValue("draft_prompt")
	if err := storage.UpdateDraftPrompt(r.Context(), s.db, id, draft); err != nil {
		log.Printf("savePromptDraft: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) generatePrompt(w http.ResponseWriter, r *http.Request, id int64) {
	if s.prompt == nil {
		http.Error(w, "prompt generator not configured", http.StatusServiceUnavailable)
		return
	}
	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	draft, err := s.prompt.Draft(r.Context(), prompt.Bot{
		ID:   bot.ID,
		Name: bot.Name,
	})
	if err != nil {
		log.Printf("generatePrompt: %v", err)
		http.Error(w, "generation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := storage.UpdateDraftPrompt(r.Context(), s.db, id, draft); err != nil {
		log.Printf("generatePrompt: persist: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	// Return a fresh editor textarea so HTMX can swap it in (outerHTML).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<textarea id="draft-editor" class="editor-text" spellcheck="false" `+
		`hx-post="/admin/bots/%d/prompt/draft" hx-trigger="keyup changed delay:800ms" `+
		`hx-swap="none" name="draft_prompt">%s</textarea>`,
		id, template.HTMLEscapeString(draft))
}

// publishPrompt enforces the Phase 2 publish gate, then promotes draft
// to published. The gate has two doors that must both be open:
//
//  1. Compliance URLs (Datenschutz + AGB) are non-null.
//  2. The latest eval_runs row exists AND its score >= bot.eval_threshold.
//
// No override path. Phase 2 acceptance criterion #4: "Override is not
// possible in Phase 2."
func (s *Server) publishPrompt(w http.ResponseWriter, r *http.Request, id int64) {
	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !bot.PrivacyPolicyURL.Valid || !bot.AGBURL.Valid {
		http.Error(w, "Publish-Gate: Datenschutz-URL und AGB-URL erforderlich", http.StatusPreconditionFailed)
		return
	}
	rep, err := eval.LatestRun(r.Context(), s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Publish-Gate: Goldfragen wurden noch nie ausgeführt", http.StatusPreconditionFailed)
		return
	}
	if err != nil {
		log.Printf("publishPrompt: latest eval: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rep.Score < bot.EvalThreshold {
		http.Error(w,
			fmt.Sprintf("Publish-Gate: Eval-Score %.2f unter Schwelle %.2f", rep.Score, bot.EvalThreshold),
			http.StatusPreconditionFailed,
		)
		return
	}
	// Phase 3 Sprint 6: third + fourth doors. Embed origin allow-list
	// must be non-empty (no wildcard publishes) and DSGVO doc URL must
	// be present (legal-basis surface for the widget consent line).
	if strings.TrimSpace(bot.EmbedOriginAllow) == "" {
		http.Error(w,
			"Publish-Gate: Erlaubte Origins (Einbettung) müssen gesetzt sein",
			http.StatusPreconditionFailed,
		)
		return
	}
	dsgvoURL, err := storage.LoadDSGVODocURL(r.Context(), s.db, id)
	if err != nil || strings.TrimSpace(dsgvoURL) == "" {
		http.Error(w,
			"Publish-Gate: DSGVO-Dokumentation-URL fehlt",
			http.StatusPreconditionFailed,
		)
		return
	}
	if err := storage.PromoteDraftToPublished(r.Context(), s.db, id); err != nil {
		log.Printf("publishPrompt: promote: %v", err)
		http.Error(w, "publish failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<div class="gate gate--ready"><span class="gate__icon">✓</span><div class="gate__text">Veröffentlicht — Eval %d/%d (%.2f).</div></div>`,
		rep.Passed, rep.Total, rep.Score)
}

// runEvals executes the bot's golden-questions file synchronously and
// returns an HTML scorecard fragment. Path defaults to the demo file
// shipped with the repo; ?file=<path> overrides for callers with a
// custom YAML.
func (s *Server) runEvals(w http.ResponseWriter, r *http.Request, id int64) {
	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("file"))
	if path == "" {
		// Fall back to the demo file. Phase 3 lets the founder upload
		// per-bot YAML via the eval modal.
		path = "tests/eval/" + bot.Name + ".yaml"
	}
	runner := eval.NewRunner(s.db, s.llm)
	rep, err := runner.Run(r.Context(), eval.Bot{
		ID:           bot.ID,
		Name:         bot.Name,
		SystemPrompt: bot.SystemPrompt,
		Threshold:    bot.EvalThreshold,
	}, path)
	if err != nil {
		log.Printf("runEvals: %v", err)
		http.Error(w, "eval failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	verdict := "unter Schwelle"
	if rep.MeetsThreshold() {
		verdict = "über Schwelle"
	}
	// All variadic args are either fixed-domain strings ("über/unter
	// Schwelle"), numeric scores, or operator-supplied paths run through
	// template.HTMLEscapeString — safe to splice.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: path is HTML-escaped above; other args have fixed domain
		`<div class="eval-scorecard"><strong>%d/%d (%.2f)</strong> — %s %.2f<br><small>%s</small></div>`,
		rep.Passed, rep.Total, rep.Score, verdict, rep.Threshold,
		template.HTMLEscapeString(path),
	)
}

// sandboxStream handles the test-chat SSE: embed the user message, RAG
// over kb_vec, stream the LLM completion back, and persist the turn.
// Wire format: text/event-stream with two event names —
//
//	event: token   data: <token chunk>
//	event: done    data: {input_tokens, output_tokens, sources: [...]}
//
// The browser handles the rest (see editor.html sandbox script).
func (s *Server) sandboxStream(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	userMsg := strings.TrimSpace(r.PostFormValue("message"))
	if userMsg == "" {
		http.Error(w, "empty message", http.StatusBadRequest)
		return
	}

	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 1. Embed the question.
	vecs, err := s.llm.Embed(r.Context(), []string{userMsg})
	if err != nil || len(vecs) != 1 {
		log.Printf("sandbox: embed: %v", err)
		http.Error(w, "embed failed", http.StatusBadGateway)
		return
	}

	// 2. RAG retrieval — top 5 chunks for this bot.
	hits, err := storage.SearchChunks(r.Context(), s.db, id, vecs[0], 5)
	if err != nil {
		log.Printf("sandbox: search: %v", err)
		http.Error(w, "retrieval failed", http.StatusInternalServerError)
		return
	}

	// 3. Build the system prompt: bot's draft (or published) + retrieved
	// context. Draft wins so the founder feels prompt edits immediately.
	sys := bot.DraftPrompt
	if strings.TrimSpace(sys) == "" {
		sys = bot.SystemPrompt
	}
	if len(hits) > 0 {
		var ctx strings.Builder
		ctx.WriteString("\n\nRelevante Auszüge aus der Wissensbasis:\n")
		for i, h := range hits {
			fmt.Fprintf(&ctx, "[%d] %s\n%s\n\n", i+1, h.Source, truncate(h.Content, 800))
		}
		sys += ctx.String()
	}

	// 4. Stream tokens via SSE.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	stream, err := s.llm.Stream(r.Context(), llm.ChatRequest{
		System:      sys,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: userMsg}},
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
			// SSE data lines can't contain raw newlines — split if needed.
			for _, line := range strings.Split(chunk.Delta, "\n") {
				_, _ = fmt.Fprintf(w, "event: token\ndata: %s\n", line)
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

	// 5. Final event with sources + token usage.
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

	// 6. Persist the turn (best-effort; don't fail the response).
	go s.persistSandboxTurn(id, userMsg, answer.String(), inTok, outTok)
}

// persistSandboxTurn writes a chats row + two messages rows for one user
// turn against the founder's sandbox visitor_id. Best-effort: errors only
// log because the response has already left the building.
func (s *Server) persistSandboxTurn(botID int64, user, assistant string, inTok, outTok int) {
	res, err := s.db.Exec(`INSERT INTO chats (bot_id, visitor_id) VALUES (?, ?)`, botID, "founder-sandbox")
	if err != nil {
		log.Printf("sandbox persist chat: %v", err)
		return
	}
	chatID, _ := res.LastInsertId()
	if _, err := s.db.Exec(
		`INSERT INTO messages (chat_id, role, content, tokens_in) VALUES (?, 'user', ?, ?)`,
		chatID, user, inTok); err != nil {
		log.Printf("sandbox persist user msg: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO messages (chat_id, role, content, tokens_out) VALUES (?, 'assistant', ?, ?)`,
		chatID, assistant, outTok); err != nil {
		log.Printf("sandbox persist assistant msg: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (s *Server) saveCompliance(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	privacy := strings.TrimSpace(r.PostFormValue("privacy_policy_url"))
	agb := strings.TrimSpace(r.PostFormValue("agb_url"))
	if err := storage.SetCompliance(r.Context(), s.db, id, privacy, agb); err != nil {
		log.Printf("saveCompliance: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// regenEmbedToken issues a new public bot-token and returns an HTML
// fragment showing the cleartext value ONCE. Subsequent calls invalidate
// the previous token (sha256 collision-resistance is doing the work).
func (s *Server) regenEmbedToken(w http.ResponseWriter, r *http.Request, id int64) {
	token, err := auth.IssueEmbedToken(r.Context(), s.db, id)
	if err != nil {
		log.Printf("regenEmbedToken: %v", err)
		http.Error(w, "Token konnte nicht erzeugt werden", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: token is hex-encoded, ids are int64
		`<div class="embed-token">
  <div class="rail__label">Bot-Token (einmalig sichtbar)</div>
  <code class="embed-token__value">%s</code>
  <div class="rail__sub">Snippet:<br><code>&lt;script src="/embed.js" data-bot="%s"&gt;&lt;/script&gt;</code></div>
</div>`, template.HTMLEscapeString(token), template.HTMLEscapeString(token))
}

// saveWidgetTemplate flips the bot between bubble and inline variants.
// Called by the rail's HTMX-driven radio.
func (s *Server) saveWidgetTemplate(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tmpl := strings.TrimSpace(r.PostFormValue("template"))
	if err := storage.SetWidgetTemplate(r.Context(), s.db, id, tmpl); err != nil {
		log.Printf("saveWidgetTemplate: %v", err)
		http.Error(w, "Ungültige Variante", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// saveWidgetTheme persists the editor's theme tweaks (accent, welcome,
// starters, etc.). Form fields → typed widget.Theme → JSON → bots row.
func (s *Server) saveWidgetTheme(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Decode existing theme, then patch the fields the form submitted.
	t := widget.DecodeTheme(bot.WidgetThemeJSON)
	if v := r.PostForm.Get("accent"); v != "" {
		t.Accent = strings.TrimSpace(v)
	}
	if v := r.PostForm.Get("welcome"); v != "" {
		t.Welcome = strings.TrimSpace(v)
	}
	if v := r.PostForm.Get("name"); v != "" {
		t.Name = strings.TrimSpace(v)
	}
	if v := r.PostForm["starter"]; len(v) > 0 {
		// Drop empties; cap at 4 to match the brief.
		clean := make([]string, 0, len(v))
		for _, p := range v {
			if s := strings.TrimSpace(p); s != "" {
				clean = append(clean, s)
			}
			if len(clean) == 4 {
				break
			}
		}
		t.StarterPrompts = clean
	}

	encoded, err := widget.EncodeTheme(t)
	if err != nil {
		http.Error(w, "encode theme: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := storage.UpdateWidgetTheme(r.Context(), s.db, id, encoded); err != nil {
		log.Printf("saveWidgetTheme: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// suggestWidgetTheme asks Claude for accent + welcome + starter prompts
// grounded in the bot's KB, persists the suggestion, and returns an
// HTML fragment with the new values pre-filled in the rail inputs.
func (s *Server) suggestWidgetTheme(w http.ResponseWriter, r *http.Request, id int64) {
	if s.suggester == nil {
		http.Error(w, "suggester not configured", http.StatusServiceUnavailable)
		return
	}
	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The Suggester needs Name/Tone/Industry. We don't store Tone +
	// Industry on the bot row in Phase 2 schema; reuse Name and let the
	// meta-prompt's industry/tone fall through to defaults derived
	// from the chunks themselves.
	suggested, err := s.suggester.Suggest(r.Context(), widget.SuggestBot{ID: bot.ID, Name: bot.Name})
	if err != nil {
		log.Printf("suggestWidgetTheme: %v", err)
		http.Error(w, "Vorschlag fehlgeschlagen: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Merge: keep template + locale + radius from existing theme; override
	// the AI-generated fields.
	current := widget.DecodeTheme(bot.WidgetThemeJSON)
	current.Accent = suggested.Accent
	current.Welcome = suggested.Welcome
	current.StarterPrompts = suggested.StarterPrompts

	encoded, err := widget.EncodeTheme(current)
	if err != nil {
		http.Error(w, "encode theme", http.StatusInternalServerError)
		return
	}
	if err := storage.UpdateWidgetTheme(r.Context(), s.db, id, encoded); err != nil {
		log.Printf("suggestWidgetTheme persist: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	// Return a small fragment that swaps the accent + welcome inputs
	// with the new values. Each input keeps its own hx-post so the
	// founder can still tweak after acceptance.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	startersHTML := strings.Builder{}
	for _, p := range current.StarterPrompts {
		fmt.Fprintf(&startersHTML, `<li>%s</li>`, template.HTMLEscapeString(p))
	}
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: all dynamic values HTMLEscaped
		`<div id="theme-suggested" class="theme-suggested">
  <div class="rail__sub">Vorschläge angewendet · einzelne Felder bleiben editierbar.</div>
  <div class="rail__row">
    <div class="rail__label">Akzent (vorgeschlagen)</div>
    <span style="display:inline-flex;align-items:center;gap:8px">
      <span style="width:18px;height:18px;border-radius:4px;background:%s;border:1px solid var(--border)"></span>
      <code>%s</code>
    </span>
  </div>
  <div class="rail__row">
    <div class="rail__label">Begrüßung (vorgeschlagen)</div>
    <div class="rail__value" style="font-size:12.5px">%s</div>
  </div>
  <div class="rail__row">
    <div class="rail__label">Vorgeschlagene Fragen</div>
    <ul style="margin:0;padding-left:18px;font-size:12.5px;color:var(--text)">%s</ul>
  </div>
</div>`,
		template.HTMLEscapeString(current.Accent),
		template.HTMLEscapeString(current.Accent),
		template.HTMLEscapeString(current.Welcome),
		startersHTML.String(),
	)
}

// saveEmbedOrigins updates the origin allow-list (CSV).
func (s *Server) saveEmbedOrigins(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	csv := strings.TrimSpace(r.PostFormValue("origins"))
	if err := storage.SetEmbedOriginAllow(r.Context(), s.db, id, csv); err != nil {
		log.Printf("saveEmbedOrigins: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// saveDSGVODocURL persists the bot's DSGVO documentation URL — gates
// the third publish-gate door alongside origin-allow.
func (s *Server) saveDSGVODocURL(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(r.PostFormValue("dsgvo_doc_url"))
	if err := storage.SetDSGVODocURL(r.Context(), s.db, id, url); err != nil {
		log.Printf("saveDSGVODocURL: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── DSGVO visitor admin (Phase 3 Sprint 6) ────────────────────────
//
// Routes:
//
//	GET  /admin/visitors/{id}            visitor record (chats + leads)
//	POST /admin/visitors/{id}/erase      cascade-delete + audit-log
//
// The erase endpoint requires the operator to type the visitor_id as
// the form's `confirm` field — defends against accidental clicks.
func (s *Server) visitorRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/visitors/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	visitorID := parts[0]
	tail := ""
	if len(parts) > 1 {
		tail = parts[1]
	}

	switch tail {
	case "":
		if r.Method == http.MethodGet {
			s.visitorDetail(w, r, visitorID)
			return
		}
	case "erase":
		if r.Method == http.MethodPost {
			s.visitorErase(w, r, visitorID)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) visitorDetail(w http.ResponseWriter, r *http.Request, visitorID string) {
	rec, err := storage.LoadVisitor(r.Context(), s.db, visitorID)
	if err != nil {
		log.Printf("visitorDetail: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: all values HTMLEscaped below
		`<!doctype html><html lang="de"><head><meta charset="utf-8"><title>Visitor %s</title><link rel="stylesheet" href="/static/app.css"></head>
<body><header class="topbar"><a class="brand" href="/admin">Everychat</a></header>
<main class="container">
<h1>Besucher-Akte</h1>
<p class="muted">Visitor-ID: <code>%s</code> · %d Chats · %d Nachrichten · %d Leads</p>
<form method="POST" action="/admin/visitors/%s/erase" onsubmit="return confirm('Daten dieses Besuchers unwiderruflich löschen?');">
  <input type="text" name="confirm" placeholder="Visitor-ID zum Bestätigen tippen" required style="width:280px">
  <button type="submit" style="background:var(--error);color:#fff">Daten löschen (DSGVO)</button>
</form>
<h2 style="margin-top:32px">Chats</h2>%s
<h2>Leads</h2>%s
</main></body></html>`,
		template.HTMLEscapeString(visitorID),
		template.HTMLEscapeString(visitorID),
		len(rec.Chats), rec.BotMessages, len(rec.Leads),
		template.HTMLEscapeString(visitorID),
		renderVisitorChats(rec.Chats),
		renderVisitorLeads(rec.Leads),
	)
}

func renderVisitorChats(chats []storage.VisitorChat) string {
	if len(chats) == 0 {
		return `<p class="muted">Keine Chats.</p>`
	}
	var b strings.Builder
	b.WriteString(`<ul>`)
	for _, c := range chats {
		fmt.Fprintf(&b, `<li><code>chat #%d</code> · Bot: %s · gestartet: %s</li>`,
			c.ChatID,
			template.HTMLEscapeString(c.BotName),
			c.StartedAt.Format("2006-01-02 15:04 MST"))
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func renderVisitorLeads(leads []storage.VisitorLead) string {
	if len(leads) == 0 {
		return `<p class="muted">Keine Leads.</p>`
	}
	var b strings.Builder
	b.WriteString(`<ul>`)
	for _, l := range leads {
		fmt.Fprintf(&b, `<li><code>lead #%d</code> · %s · %s</li>`,
			l.LeadID,
			template.HTMLEscapeString(l.Email),
			l.CapturedAt.Format("2006-01-02 15:04 MST"))
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func (s *Server) visitorErase(w http.ResponseWriter, r *http.Request, visitorID string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("confirm") != visitorID {
		http.Error(w, "Bestätigung stimmt nicht überein", http.StatusBadRequest)
		return
	}
	actor := auth.SessionEmail(r.Context())
	summary, err := storage.EraseVisitor(r.Context(), s.db, visitorID, actor)
	if err != nil {
		log.Printf("visitorErase: %v", err)
		http.Error(w, "Erasure fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: visitorID HTMLEscaped, counts are int64
		`<!doctype html><html lang="de"><head><meta charset="utf-8"><title>Erasure-Quittung</title><link rel="stylesheet" href="/static/app.css"></head>
<body><header class="topbar"><a class="brand" href="/admin">Everychat</a></header>
<main class="container">
<section class="card">
  <h1>Daten gelöscht</h1>
  <p>Visitor-ID <code>%s</code> wurde gemäß DSGVO Art. 17 gelöscht.</p>
  <p class="muted">%d Chats · %d Nachrichten · %d Leads<br>Audit-Log-Eintrag erstellt um %s</p>
  <p><a href="/admin">Zurück zum Dashboard</a></p>
</section>
</main></body></html>`,
		template.HTMLEscapeString(visitorID),
		summary.ChatsDeleted, summary.MessagesDeleted, summary.LeadsDeleted,
		summary.ErasedAt.Format(time.RFC3339))
}

// ─── New-bot wizard ──────────────────────────────────────────────────
//
// Three steps live on a single page; HTMX swaps fragments in place.
//
//	GET  /admin/bots/new                       — step 1 (form)
//	POST /admin/bots/new                       — creates bot, returns step 2
//	GET  /admin/bots/{id}/wizard/crawl?d=…     — SSE crawl progress
//	POST /admin/bots/{id}/wizard/draft         — runs prompt.Draft, returns step 3
//
// The wizard ends with "Zum Editor" → /admin/bots/{id}.

func (s *Server) wizardStart(w http.ResponseWriter, r *http.Request) {
	type industryOpt struct{ Value, Label string }
	opts := make([]industryOpt, 0, 6)
	for _, ind := range corpus.Industries() {
		opts = append(opts, industryOpt{Value: string(ind), Label: ind.DisplayName()})
	}
	s.render(w, "wizard.html", map[string]any{
		"Title":      "Neuer Bot",
		"Email":      auth.SessionEmail(r.Context()),
		"Industries": opts,
	})
}

// wizardCreate creates the bot row and returns the step-2 SSE-listener
// fragment. The browser reads ?bot_id from the response and connects to
// /admin/bots/{id}/wizard/crawl.
func (s *Server) wizardCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	domain := strings.TrimSpace(r.PostFormValue("domain"))
	industry := strings.TrimSpace(r.PostFormValue("industry"))
	if name == "" || domain == "" {
		http.Error(w, "Name und Domain sind erforderlich.", http.StatusBadRequest)
		return
	}
	if industry != "" && !corpus.Valid(industry) {
		http.Error(w, "Ungültige Branche.", http.StatusBadRequest)
		return
	}
	id, err := storage.CreateBot(r.Context(), s.db, name, domain)
	if err != nil {
		log.Printf("wizardCreate: %v", err)
		http.Error(w, "Anlegen fehlgeschlagen.", http.StatusInternalServerError)
		return
	}
	if industry != "" {
		if err := storage.SetIndustry(r.Context(), s.db, id, industry); err != nil {
			log.Printf("wizardCreate: SetIndustry: %v", err)
			// Non-fatal — the editor's settings rail can backfill later.
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// All user-derived values are HTML-escaped (domain) or fmt-printed
	// from controlled types (id is int64); the literal markup is a
	// constant string. %q on `domain` is for the JS string literal.
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: domain HTMLEscaped, ids are int64
		`
<section id="wizard" class="card">
  <h2 style="margin:0 0 12px">Schritt 2 von 3 — Inhalte werden gesammelt …</h2>
  <p class="muted">Crawle <code>%s</code> · max 30 Seiten · 1 Anfrage pro Sekunde.</p>
  <pre id="crawl-log" class="crawl-log"></pre>
  <div id="crawl-stats" class="muted" style="margin-top:8px">Verbinde …</div>
  <div id="crawl-actions" style="margin-top:16px;display:none">
    <button class="ghost-btn"
            hx-post="/admin/bots/%d/wizard/draft"
            hx-target="#wizard"
            hx-swap="outerHTML">
      Schritt 3 — System-Prompt entwerfen lassen
    </button>
  </div>
</section>
<script>
  (function(){
    const log    = document.getElementById('crawl-log');
    const stats  = document.getElementById('crawl-stats');
    const actions = document.getElementById('crawl-actions');
    const url = '/admin/bots/%d/wizard/crawl?d=' + encodeURIComponent(%q);
    const es = new EventSource(url);
    let pages = 0, chunks = 0;

    es.addEventListener('page', e => {
      pages++;
      const line = document.createElement('div');
      line.textContent = e.data;
      log.appendChild(line);
      log.scrollTop = log.scrollHeight;
      const m = e.data.match(/(\d+)\s+chunk/);
      if (m) chunks += parseInt(m[1], 10);
      stats.textContent = pages + ' Seiten · ' + chunks + ' Chunks';
    });
    es.addEventListener('done', e => {
      es.close();
      stats.textContent = e.data;
      actions.style.display = 'block';
    });
    es.addEventListener('error', e => {
      stats.textContent = 'Fehler beim Crawl. Versuche es erneut.';
      es.close();
    });
  })();
</script>`,
		template.HTMLEscapeString(domain), id, id, domain)
}

// wizardCrawlStream runs the ingest pipeline, forwarding each log line
// as a `page` SSE event and emitting a `done` event with final stats.
func (s *Server) wizardCrawlStream(w http.ResponseWriter, r *http.Request, id int64) {
	domain := strings.TrimSpace(r.URL.Query().Get("d"))
	if domain == "" {
		http.Error(w, "missing ?d", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	emit := func(event, data string) {
		// SSE is text/event-stream, not HTML — the browser delivers `data`
		// to the EventSource consumer as a plain string (we set it via
		// .textContent, not .innerHTML, on the JS side). No XSS surface.
		safe := strings.ReplaceAll(data, "\n", " ")
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, safe) //nolint:gosec // G705: SSE wire is not HTML
		if flusher != nil {
			flusher.Flush()
		}
	}

	sseLines := ingest.NewSSELines(func(line string) {
		emit("page", line)
	})

	pipe := ingest.New(s.db, s.llm)
	pipe.Logger = sseLines

	stats, err := pipe.Ingest(r.Context(), id, domain, crawler.Options{MaxPages: 30})
	sseLines.Flush()
	if err != nil {
		emit("error", err.Error())
		return
	}
	emit("done", fmt.Sprintf("Fertig — %d Seiten · %d Chunks · %d Embeddings-Calls",
		stats.PagesFetched, stats.ChunksProduced, stats.EmbeddingsCalls))
}

// wizardDraft runs prompt.Draft and returns the step-3 fragment that
// shows the drafted prompt with a "Zum Editor" button.
func (s *Server) wizardDraft(w http.ResponseWriter, r *http.Request, id int64) {
	if s.prompt == nil {
		http.Error(w, "prompt generator not configured", http.StatusServiceUnavailable)
		return
	}
	bot, err := storage.LoadBot(r.Context(), s.db, id)
	if errors.Is(err, storage.ErrBotNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	draft, err := s.prompt.Draft(r.Context(), prompt.Bot{ID: bot.ID, Name: bot.Name})
	if err != nil {
		log.Printf("wizardDraft: %v", err)
		http.Error(w, "Generierung fehlgeschlagen: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := storage.UpdateDraftPrompt(r.Context(), s.db, id, draft); err != nil {
		log.Printf("wizardDraft persist: %v", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	// Best-effort theme suggestion — the editor's Verhalten rail reflects
	// these defaults. Failure here is logged but doesn't fail the wizard;
	// founder can re-trigger via "Vorschläge generieren" in the rail.
	if s.suggester != nil {
		if t, err := s.suggester.Suggest(r.Context(), widget.SuggestBot{ID: bot.ID, Name: bot.Name}); err == nil {
			if encoded, err := widget.EncodeTheme(t); err == nil {
				_ = storage.UpdateWidgetTheme(r.Context(), s.db, id, encoded)
			}
		} else {
			log.Printf("wizardDraft theme suggest (best-effort): %v", err)
		}
	}

	// Phase 4 Sprint 2: if the bot has an industry tag, seed its eval
	// YAML from the corpus and flip eval_mode to llm_judge. Best-effort
	// like the theme suggest above — corpus seeding failure shouldn't
	// fail the wizard's primary "drafted prompt" deliverable.
	if bot.Industry.Valid && corpus.Valid(bot.Industry.String) {
		if err := corpus.Seed(r.Context(), s.db, bot.ID, bot.Name, corpus.Industry(bot.Industry.String)); err != nil {
			log.Printf("wizardDraft corpus seed (best-effort): %v", err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `
<section id="wizard" class="card">
  <h2 style="margin:0 0 12px">Schritt 3 von 3 — Erster Entwurf</h2>
  <p class="muted">Claude hat einen System-Prompt aus den Inhalten geschrieben. Du kannst ihn jetzt im Editor weiter bearbeiten.</p>
  <pre class="prompt-preview">%s</pre>
  <div style="display:flex;gap:8px;margin-top:16px">
    <a class="ghost-btn" href="/admin/bots/%d">Zum Editor →</a>
  </div>
</section>`,
		template.HTMLEscapeString(draft), id)
}

func (s *Server) renderLoginError(w http.ResponseWriter, r *http.Request, msg string) {
	csrf := s.issueCSRF(w, r)
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, "login.html", map[string]any{
		"Title": "Sign in",
		"CSRF":  csrf,
		"Error": msg,
	})
}

func (s *Server) render(w http.ResponseWriter, page string, data map[string]any) {
	tpl, ok := s.pages[page]
	if !ok {
		log.Printf("template %s: not found", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "layout", merge(data, page)); err != nil {
		log.Printf("template %s: %v", page, err)
	}
}

func merge(data map[string]any, _ string) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["Email"]; !ok {
		data["Email"] = ""
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = "Everychat"
	}
	if _, ok := data["Error"]; !ok {
		data["Error"] = ""
	}
	if _, ok := data["CSRF"]; !ok {
		data["CSRF"] = ""
	}
	return data
}

// issueCSRF sets a per-request CSRF cookie (double-submit pattern) and
// returns the value to embed as a hidden form field.
func (s *Server) issueCSRF(w http.ResponseWriter, _ *http.Request) string {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		log.Printf("csrf rand: %v", err)
		return ""
	}
	tok := hex.EncodeToString(buf[:])
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    tok,
		Path:     "/login",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(30 * time.Minute),
	})
	return tok
}

func (s *Server) checkCSRF(r *http.Request) error {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil || c.Value == "" {
		return errors.New("csrf: missing cookie")
	}
	form := r.PostFormValue("csrf")
	if form == "" {
		_ = r.ParseForm()
		form = r.PostFormValue("csrf")
	}
	if subtle.ConstantTimeCompare([]byte(c.Value), []byte(form)) != 1 {
		return errors.New("csrf: mismatch")
	}
	return nil
}
