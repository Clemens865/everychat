// Package web wires HTTP handlers, embedded HTML templates, and static
// assets for the Everychat admin UI. Phase 1 delivers magic-link login plus
// an empty admin shell; richer UI lands in Phase 2.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/auth"
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
	staticSub fs.FS
}

// New constructs a Server with the provided auth helpers.
func New(links *auth.MagicLinks, sessions *auth.Sessions) (*Server, error) {
	pageNames := []string{"login.html", "login_sent.html", "admin_home.html"}
	pages := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return &Server{
		pages:     pages,
		links:     links,
		sessions:  sessions,
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
	s.render(w, "admin_home.html", map[string]any{
		"Title": "Admin",
		"Email": addr,
	})
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
