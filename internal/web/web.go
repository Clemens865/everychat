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
	"github.com/clemenshoenig/everychat/internal/eval"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/prompt"
	"github.com/clemenshoenig/everychat/internal/storage"
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
	llm       *llm.LiteLLMClient
	staticSub fs.FS
}

// New constructs a Server with the provided dependencies.
func New(db *sql.DB, links *auth.MagicLinks, sessions *auth.Sessions, gen *prompt.Generator, llmClient *llm.LiteLLMClient) (*Server, error) {
	// Phase 1 pages share layout.html; the editor uses its own layout.
	type pagespec struct{ name, layout string }
	specs := []pagespec{
		{"login.html", "layout.html"},
		{"login_sent.html", "layout.html"},
		{"admin_home.html", "layout.html"},
		{"editor.html", "layout_editor.html"},
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
		llm:       llmClient,
		staticSub: staticSub,
	}, nil
}

// Routes returns the configured http.Handler with all routes wired up.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticSub))))

	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/auth/verify", s.verify)
	mux.HandleFunc("/logout", s.logout)
	mux.Handle("/admin", s.sessions.Require(http.HandlerFunc(s.admin)))
	mux.Handle("/admin/bots/", s.sessions.Require(http.HandlerFunc(s.botRoutes)))
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

// botRoutes is a tiny path router for /admin/bots/{id}/...
// We avoid a third-party router for now; chi/mux can come in Phase 3.
func (s *Server) botRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/bots/")
	if rest == "" {
		http.NotFound(w, r)
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

	s.render(w, "editor.html", map[string]any{
		"Title":               bot.Name,
		"Email":               auth.SessionEmail(r.Context()),
		"Bot":                 bot,
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
