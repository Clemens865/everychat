package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/clemenshoenig/everychat/internal/adversary"
	"github.com/clemenshoenig/everychat/internal/llm"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// Phase 5 Sprint 2 — dashboard mini test pane + adversary modal.
//
// Design choices:
//   - HTML is rendered inline via fmt.Fprintf, matching the surrounding
//     web.go convention. Splitting these into Go-template partials is a
//     refactor for a different day; new code should look like its
//     neighbours.
//   - Adversary runs are kicked off in a goroutine with context.Background
//     so they outlive the HTTP request that started them. The polling
//     endpoint reads from storage — there's no in-memory state to lose.
//   - The cost-cap default (0.50 EUR) and turn-cap default (8) match the
//     CLI defaults so dashboard runs are reproducible from `everychat
//     adversary` and vice versa.

const (
	defaultAdversaryTurns        = 8
	defaultAdversaryMaxCostCents = 50 // 0.50 EUR
	maxAdversaryTurns            = 20
	maxAdversaryMaxCostCents     = 500 // 5.00 EUR — UI guardrail
)

// testPanePartial returns the empty test-pane shell. HTMX swaps it
// into the bot card on click; the user types a question, posts to
// testPaneRun, and the answer renders inside this shell.
func (s *Server) testPanePartial(w http.ResponseWriter, _ *http.Request, id int64) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: id is int64, no user input
		`<form class="pane pane--test"
		      hx-post="/admin/bots/%d/test-pane/run"
		      hx-target="#test-pane-out-%d"
		      hx-swap="innerHTML">
		  <textarea name="message" rows="2" placeholder="Frage an den Bot…" required></textarea>
		  <div class="pane__row">
		    <button type="submit" class="ghost-btn">Senden</button>
		    <button type="button" class="link"
		            hx-get="/admin/bots/%d/test-pane/close"
		            hx-target="#test-pane-%d"
		            hx-swap="innerHTML">schließen</button>
		  </div>
		  <div id="test-pane-out-%d" class="pane__out"></div>
		</form>`, id, id, id, id, id)
}

// testPaneClose clears the pane.
func (s *Server) testPaneClose(w http.ResponseWriter, _ *http.Request, _ int64) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(""))
}

// testPaneRun runs one round-trip through the visitor RAG pipeline and
// renders the answer + source list as a partial. Single-shot, not
// streaming — the dashboard isn't the editor, and a smoke test
// shouldn't need an SSE rig.
func (s *Server) testPaneRun(w http.ResponseWriter, r *http.Request, id int64) {
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

	answer, sources, err := s.runMiniTest(r.Context(), bot, userMsg)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		_, _ = fmt.Fprintf(w, //nolint:gosec // G705: error message HTML-escaped
			`<div class="pane__error">Fehler: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	var srcsHTML strings.Builder
	if len(sources) > 0 {
		srcsHTML.WriteString(`<details class="pane__sources"><summary>Quellen (`)
		fmt.Fprintf(&srcsHTML, "%d)", len(sources))
		srcsHTML.WriteString(`</summary><ul>`)
		for _, src := range sources {
			fmt.Fprintf(&srcsHTML, `<li>%s</li>`, template.HTMLEscapeString(src))
		}
		srcsHTML.WriteString(`</ul></details>`)
	}

	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: dynamic values HTML-escaped above
		`<div class="pane__qa">
		  <div class="pane__q"><strong>Frage:</strong> %s</div>
		  <div class="pane__a">%s</div>
		  %s
		</div>`,
		template.HTMLEscapeString(userMsg),
		template.HTMLEscapeString(answer),
		srcsHTML.String(),
	)
}

// runMiniTest reuses the visitor RAG path (embed → SearchChunks →
// stream chat → drain to string). Pulled out so tests can hit the
// pure logic without spinning up the full HTTP server.
func (s *Server) runMiniTest(ctx context.Context, bot storage.Bot, userMsg string) (string, []string, error) {
	vecs, err := s.llm.Embed(ctx, []string{userMsg})
	if err != nil || len(vecs) != 1 {
		return "", nil, fmt.Errorf("embed: %w", err)
	}
	hits, err := storage.SearchChunks(ctx, s.db, bot.ID, vecs[0], 5)
	if err != nil {
		return "", nil, fmt.Errorf("retrieval: %w", err)
	}
	sys := bot.DraftPrompt
	if strings.TrimSpace(sys) == "" {
		sys = bot.SystemPrompt
	}
	if len(hits) > 0 {
		var b strings.Builder
		b.WriteString("\n\nRelevante Auszüge aus der Wissensbasis:\n")
		for i, h := range hits {
			fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, h.Source, truncate(h.Content, 800))
		}
		sys += b.String()
	}

	stream, err := s.llm.Stream(ctx, llm.ChatRequest{
		System:      sys,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: userMsg}},
		MaxTokens:   600,
		Temperature: 0.4,
	})
	if err != nil {
		return "", nil, fmt.Errorf("chat: %w", err)
	}
	var ans strings.Builder
	for chunk := range stream {
		if chunk.Err != nil {
			return "", nil, chunk.Err
		}
		ans.WriteString(chunk.Delta)
	}

	srcs := make([]string, 0, len(hits))
	for _, h := range hits {
		srcs = append(srcs, h.Source)
	}
	return ans.String(), srcs, nil
}

// adversaryModalPartial renders the form that POSTs to adversaryStart.
// HTMX swaps it into the bot card on click.
func (s *Server) adversaryModalPartial(w http.ResponseWriter, _ *http.Request, id int64) {
	personas, err := adversary.Personas()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var optsHTML strings.Builder
	for _, p := range personas {
		fmt.Fprintf(&optsHTML, `<option value="%s">%s</option>`,
			template.HTMLEscapeString(p.ID),
			template.HTMLEscapeString(p.Name),
		)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: dynamic values HTML-escaped
		`<form class="pane pane--adversary"
		      hx-post="/admin/bots/%d/adversary"
		      hx-target="#adversary-pane-%d"
		      hx-swap="innerHTML">
		  <label>Persona
		    <select name="persona" required>%s</select>
		  </label>
		  <label>Turns
		    <input type="number" name="turns" value="%d" min="1" max="%d">
		  </label>
		  <label>Max-Cost (EUR)
		    <input type="number" name="max_cost_eur" value="0.50" step="0.10" min="0.10" max="%.2f">
		  </label>
		  <div class="pane__row">
		    <button type="submit" class="ghost-btn">Adversary-Lauf starten</button>
		    <button type="button" class="link"
		            hx-get="/admin/bots/%d/adversary/close"
		            hx-target="#adversary-pane-%d"
		            hx-swap="innerHTML">abbrechen</button>
		  </div>
		</form>`,
		id, id,
		optsHTML.String(),
		defaultAdversaryTurns, maxAdversaryTurns,
		float64(maxAdversaryMaxCostCents)/100.0,
		id, id,
	)
}

// adversaryClose clears the modal target.
func (s *Server) adversaryClose(w http.ResponseWriter, _ *http.Request, _ int64) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(""))
}

// adversaryStart kicks off a run in a goroutine and immediately
// returns a polling pane targeted at adversaryRunStatus. The HTTP
// request that triggered this returns in milliseconds; the run
// continues in the background and writes to adversary_runs.
func (s *Server) adversaryStart(w http.ResponseWriter, r *http.Request, id int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	personaID := strings.TrimSpace(r.PostFormValue("persona"))
	persona, err := adversary.LoadPersona(personaID)
	if err != nil {
		http.Error(w, "unknown persona", http.StatusBadRequest)
		return
	}

	turns, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("turns")))
	if err != nil || turns <= 0 || turns > maxAdversaryTurns {
		turns = defaultAdversaryTurns
	}
	maxCostEUR, err := strconv.ParseFloat(strings.TrimSpace(r.PostFormValue("max_cost_eur")), 64)
	if err != nil || maxCostEUR <= 0 {
		maxCostEUR = float64(defaultAdversaryMaxCostCents) / 100.0
	}
	maxCostCents := int64(maxCostEUR * 100)
	if maxCostCents > maxAdversaryMaxCostCents {
		maxCostCents = maxAdversaryMaxCostCents
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

	// Create the run row up front so we can hand the id back to the
	// browser before the goroutine starts. The runner re-creates the
	// row inside Run() — to keep the runner self-contained we don't
	// pre-insert here; instead we kick the run and poll for the
	// most-recent run id for this bot. (Simpler than threading the id
	// across goroutines.)
	go s.runAdversaryDetached(bot, *persona, turns, maxCostCents)

	// Render the polling pane. We don't yet know the run-id; the
	// pane polls `latest` and renders whatever it finds.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: id is int64
		`<div class="pane pane--adversary-running"
		      hx-get="/admin/bots/%d/adversary/latest"
		      hx-trigger="load delay:1s, every 2s"
		      hx-swap="innerHTML">
		  <p class="muted">Adversary-Lauf läuft …</p>
		</div>`, id)
}

// runAdversaryDetached owns the goroutine lifecycle. ctx is
// background — runs must outlive the originating HTTP request.
func (s *Server) runAdversaryDetached(bot storage.Bot, persona adversary.Persona, turns int, maxCostCents int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner := adversary.NewRunner(s.db, s.llm, s.llm)
	if _, err := runner.Run(ctx, bot, persona, adversary.Options{
		TurnCap: turns, MaxCostCents: maxCostCents,
	}); err != nil {
		log.Printf("adversary: run for bot %d failed: %v", bot.ID, err)
	}
}

// adversaryLatest renders the most recent run's transcript + verdict
// for the bot. Polled by the running pane every 2s.
func (s *Server) adversaryLatest(w http.ResponseWriter, r *http.Request, id int64) {
	run, err := storage.LatestAdversaryRunForBot(r.Context(), s.db, id)
	if err != nil {
		// No row yet (just kicked off; insert lags) — keep polling.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, //nolint:gosec // G705: id is int64
			`<div class="pane pane--adversary-running"
			      hx-get="/admin/bots/%d/adversary/latest"
			      hx-trigger="every 2s"
			      hx-swap="innerHTML">
			  <p class="muted">Adversary-Lauf wird vorbereitet …</p>
			</div>`, id)
		return
	}
	s.renderAdversaryRun(w, run, true)
}

// adversaryRunStatus renders a specific run by id. Useful for sharing
// a deep link into a finished transcript.
func (s *Server) adversaryRunStatus(w http.ResponseWriter, r *http.Request, runID int64) {
	run, err := storage.LoadAdversaryRun(r.Context(), s.db, runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderAdversaryRun(w, run, false)
}

// renderAdversaryRun is the shared partial used by both the polling
// pane and the deep-link route. polling=true keeps an hx-get refresh
// loop running until status leaves 'running'.
func (s *Server) renderAdversaryRun(w http.ResponseWriter, run storage.AdversaryRun, polling bool) {
	turns, err := storage.ListAdversaryTurns(context.Background(), s.db, run.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var transcript strings.Builder
	for _, t := range turns {
		role := template.HTMLEscapeString(t.Role)
		fmt.Fprintf(&transcript,
			`<div class="adversary-turn adversary-turn--%s">
			  <div class="adversary-turn__role">%s · turn %d</div>
			  <div class="adversary-turn__content">%s</div>
			</div>`,
			role, role, t.TurnIndex,
			template.HTMLEscapeString(t.Content),
		)
	}

	verdict := ""
	if run.Verdict.Valid {
		verdict = run.Verdict.String
	}

	statusClass := "adversary-status--" + template.HTMLEscapeString(run.Status)
	pollAttr := ""
	if polling && run.Status == adversary.StatusRunning {
		pollAttr = fmt.Sprintf(`hx-get="/admin/bots/%d/adversary/latest" hx-trigger="every 2s" hx-swap="innerHTML"`, run.BotID)
	}

	_, _ = fmt.Fprintf(w, //nolint:gosec // G705: dynamic values HTML-escaped above; pollAttr has fixed shape
		`<div class="pane pane--adversary-result %s" %s>
		  <header class="adversary__head">
		    <span class="adversary__persona">%s</span>
		    <span class="adversary__status">%s</span>
		    <span class="adversary__cost">%d ¢ / %d ¢</span>
		  </header>
		  <div class="adversary__verdict">%s</div>
		  <div class="adversary__transcript">%s</div>
		</div>`,
		statusClass, pollAttr,
		template.HTMLEscapeString(run.Persona),
		template.HTMLEscapeString(run.Status),
		run.TotalCostCents, run.MaxCostCents,
		template.HTMLEscapeString(verdict),
		transcript.String(),
	)
}
