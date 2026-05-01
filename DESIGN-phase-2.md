# Everychat — Phase 2 UI Design Brief

**Companion to `PRD-phase-2.md`.** The PRD locks scope and acceptance criteria; this brief locks the user experience that satisfies them. Hand to `/design-scout`, `/ui-expert`, or a fresh Claude design session to render mockups.

## Overview

Phase 2 adds a single, opinionated authoring experience on top of the Phase 1 admin shell. The founder builds a customer's bot end-to-end inside one screen — the **Bot Editor** — that combines settings, prompt, and live chat in a workbench layout. The dashboard and new-bot wizard are entry/exit ramps; everything that *matters* happens in the editor.

## Design Principles

1. **One screen, one job.** The editor must let the founder iterate (edit prompt → test in chat → re-evaluate → publish) without route changes. Tabs, modals, and side panels are fine; full page navigations during the loop are not.
2. **Make the gate visible, not hidden.** The publish gate (eval pass + privacy/AGB URLs) lives in a persistent banner at the top of the editor. Failure isn't an error toast — it's a colored bar that explains *what's missing* and offers the fix inline.
3. **Trust signals over delight.** DACH Mittelstand admins respond to visible scores, audit timestamps, and "Stand: 12.05.2026" labels. Avoid SaaS playfulness, gamification, or motion that doesn't carry information.
4. **Desktop-first.** Bot authoring is desk work. The dashboard must work on tablet; the editor does not.
5. **Carry Phase 1's aesthetic forward.** Same dark monochrome shell (`#0f1115` bg, `#161a22` surface, `#6c8cff` accent, system fonts). New components inherit this palette; no second design language.
6. **German-first copy.** UI strings in DE; tooltips and error messages in DE. (Phase 1 shipped EN-leaning copy — Phase 2 corrects this in passing.)

## Information Architecture

```
/admin                          Dashboard (bot list + CTA)
/admin/bots/new                 New-bot wizard (3 steps, single page, hx-driven)
/admin/bots/{id}                Bot Editor — workbench (settings | prompt | chat)
/admin/bots/{id}/evals          Eval modal (route + modal — direct-linkable)
/admin/bots/{id}/sandbox/...    SSE endpoints (no UI — backs the chat panel)
/login, /logout                 Phase 1 auth (unchanged)
```

Everything else (eval CLI, ops provisioning) is non-UI and out of scope for this brief.

## Visual Inheritance from Phase 1

Phase 1 shipped a minimal shell with the following primitives — design must reuse, not replace:

- Topbar (24px h-padding, 14px v-padding, 1px bottom border `#232936`)
- Card surface (`#161a22`, 10px radius, 32px padding for hero cards, 20px for list cards)
- Input/button/label primitives in `internal/web/static/app.css`
- Accent `#6c8cff` for primary actions; muted `#98a2b3` for secondary text; `#ff7373` for errors

Design pass *adds*: split layouts, tabbed panels, streaming chat bubbles, eval scorecards, banner system. Pass *does not change*: typography scale, base palette, button shapes, input chrome.

---

## Screen 1 — Dashboard (`/admin`)

Replaces Phase 1's empty "Welcome — Create your first bot" placeholder.

### Layout
- Topbar (Phase 1) + container (Phase 1).
- **Header row**: H1 "Bots" left; primary button "+ Neuer Bot" right.
- **Bot list** below: vertical stack of cards (one per bot). Phase 2 only ever shows zero or one card; design for ≥1 anyway — schema permits it.
- Empty state replaces the list with a centered hero card (~480px wide): illustration slot (placeholder), one paragraph of copy, CTA "Ersten Bot erstellen".

### Bot card content (≈120px tall)
| Slot | Content |
|------|---------|
| Top-left | Bot name (16px, `--text`) + domain (13px, `--muted`, link icon) |
| Top-right | Status pill — `Entwurf` (yellow), `Live` (green), `Eval ausstehend` (grey) |
| Mid-left | Last eval score `12 / 15 (0.80)` with mini progress bar |
| Mid-right | "Zuletzt veröffentlicht: 12.05.2026" (or "Noch nie" if unpublished) |
| Bottom | Recent message-volume sparkline (Phase 3 will fill it; Phase 2 shows "—") |

Whole card is clickable → `/admin/bots/{id}` (the editor). Hovering raises elevation by lightening the surface 4%.

### Edge cases
- **Crawl in progress** (bot exists, ingestion not finished): card shows a pulsing dot + "Crawl läuft (47 Seiten)" instead of eval score; clicking still works but the editor opens with an overlay (see Editor → States).
- **Eval threshold lowered**: if a previously-failing bot now passes, the status pill changes on next dashboard load — no toast, no celebration.

---

## Screen 2 — New-Bot Wizard (`/admin/bots/new`)

Single page, three logical steps, HTMX-driven (no full nav between steps). Cancellation at any point returns to dashboard.

### Layout
- Centered card, max-width 640px.
- **Step indicator** at top: dots `● ○ ○`, active step bolded, completed steps with a check.
- **Step body** swaps in place via `hx-swap="innerHTML"`.
- **Bottom row**: secondary "Zurück" (disabled on step 1) + primary CTA changes per step.

### Step 1 — "Wer ist der Kunde?"
- Field: `Name des Kunden` (text, required) — e.g. "Steuerkanzlei Mustermann"
- Field: `Domain` (URL, required, validated client-side: must be https://, valid host)
- Field: `Branche` (text, optional) — free text; hint: "z. B. Steuerberatung, Maschinenbau, Architekturbüro"
- Field: `Tonalität` (radio, default `formal`): `formal` / `freundlich-konversationell` / `technisch-präzise`
- CTA: **"Crawl starten"** → posts the form, persists the bot row, advances.

### Step 2 — "Inhalte werden gesammelt…"
- Live progress display:
  - **Header**: "Crawle `kanzlei-mustermann.de`" + abort button
  - **Progress bar**: percentage, indeterminate fallback if total unknown
  - **Live log** (max 8 visible lines, auto-scrolls): each line shows ✓ / ✗ + URL + chunk count, e.g. `✓ /leistungen — 4 chunks`
  - **Stats footer**: pages discovered, chunks ingested, embeddings calls — three counters updating live
- SSE-driven from `internal/ingest/pipeline.Ingest`. Cancellation triggers a confirmation, then deletes the bot + chunks.
- On completion: brief 1.5s "Fertig" state with checkmark, auto-advances to step 3.

### Step 3 — "Erster Entwurf"
- Title: "Claude hat einen System-Prompt geschrieben"
- Big editable textarea (min 16 rows) pre-filled with the drafted prompt.
- Sub-text under the textarea: "Du kannst diesen Entwurf jetzt bearbeiten oder im Editor weiterentwickeln."
- Two CTAs: secondary "Zum Editor" (saves draft as-is, opens editor), primary "Direkt veröffentlichen" — *disabled* with tooltip explaining the publish gate (always blocked here because no eval has run yet).

### Edge cases
- **Crawl finds zero usable pages**: step 2 ends with an error state offering "Erneut versuchen" (re-crawl) or "Manuell weitermachen" (creates an empty KB; founder writes prompt from scratch).
- **Domain unreachable / DNS error**: blocks step 1 → step 2 with inline error.
- **Founder closes tab mid-crawl**: ingestion continues server-side; resume via dashboard card showing in-progress state.

---

## Screen 3 — Bot Editor (`/admin/bots/{id}`) — *the workbench*

The screen the founder lives in. Three columns; iteration cycle (edit → test → eval → publish) happens entirely inside this view.

### Frame
- Topbar (Phase 1, unchanged).
- **Publish-gate banner** — full-width, sticky directly under topbar, 48px tall, color-coded.
- **Three-column body** below banner, fills remaining viewport.

### Banner — three states
| State | Color | Left content | Right CTA |
|-------|-------|--------------|-----------|
| 🟢 Bereit | `--accent` background, white text | "Bereit zur Veröffentlichung — Eval 14/15, Compliance vollständig." | **"Veröffentlichen"** button |
| 🟡 Entwurf | yellow `#caa45a` | "Entwurf — noch keine Evaluation. Führe Goldfragen aus, um zu veröffentlichen." | "Evals ausführen" → opens eval modal |
| 🔴 Blockiert | `--error` | "Veröffentlichung blockiert: 9/15 Fragen bestanden (Schwelle 0.85)." If compliance also failing: "… und Datenschutz-URL fehlt." | "Bericht öffnen" → eval modal, with failed rows expanded |

Banner is *always* present in the editor — it's how the founder feels the gate.

### Column 1 — Settings rail (left, 280px fixed)
Vertical scroll. Collapsible accordion groups (clicking the header expands; only one open at a time by default — UX choice for designer).

**Group: Identität** (open by default)
- Bot name (inline-editable, click to edit)
- Status pill (read-only mirror of banner)
- Domain (read-only, with link-out icon)
- Branche, Tonalität (inline-editable)

**Group: Compliance** ← gate-critical
- Datenschutz-URL (text input, validated, required for publish)
- AGB-URL (text input, validated, required for publish)
- Aufbewahrungsdauer (number, days, default 90 — `bots.retention_days`)
- A pulsing red dot next to the group label if any required field is missing.

**Group: Wissensbasis**
- Source domain (read-only)
- Chunk count: "47 Chunks aus 12 Seiten"
- Last crawl: "vor 3 Stunden"
- Button: "Erneut crawlen" (confirms before replacing)

**Group: Evaluation**
- Goldfragen-Datei: link to `/admin/bots/{id}/evals` (eval modal)
- Schwelle: number input (0.0–1.0, default 0.85), inline-editable
- Last run: "12/15 (0.80) — vor 5 Min" with link to that run's report
- Button: "Run starten"

**Group: Gefahrenzone** (collapsed by default; red text on hover)
- "Bot löschen" — destructive, modal confirm "tippe Botname"

### Column 2 — Prompt workbench (middle, ~45%)
Three-tab bar at top:
- **Entwurf** (default, editable) — `bots.draft_prompt`
- **Veröffentlicht** (read-only) — `bots.system_prompt`. If never published: empty state "Noch nichts veröffentlicht."
- **Diff** (read-only) — colored unified diff of draft vs published. Helpful before publish click.

Below tab bar:
- The chosen tab's content fills the column. Editable tabs use a monospaced textarea (16px, line-height 1.5, soft-wrap). Read-only tabs use the same styling, dimmed.
- **Footer ribbon** under editor:
  - Left: "Entwurf gespeichert vor 3 Sek" (autosave indicator) or "Ungespeicherte Änderungen" (when dirty).
  - Right: "12 Zeichen seit letzter Veröffentlichung" + "Generieren" button (re-runs `prompt.Draft` with current KB; warns: "Aktueller Entwurf wird überschrieben").

Autosave: debounced 800ms after last keystroke → POST `/admin/bots/{id}/prompt/draft`. No save button needed; Cmd+S triggers the same endpoint immediately.

### Column 3 — Live chat sandbox (right, ~45%)
Three regions: header / messages / composer.

**Header (40px tall, surface bg)**
- Left: badge "Testet Entwurf" (yellow) or "Testet Veröffentlicht" (green) — toggle in the prompt-tab bar drives this.
- Right: kebab menu — `Verlauf zurücksetzen`, `Letzte Antwort als Goldfrage speichern`, `Verlauf exportieren`.

**Messages region (scrollable)**
- Bubbles styled per role:
  - User: right-aligned, accent bubble, white text
  - Assistant: left-aligned, surface bubble, normal text; tokens stream in with a subtle blink at the cursor while generating
  - System dividers (when prompt changes mid-conversation): horizontal rule + caption "Prompt aktualisiert — neue Nachrichten verwenden v3"
- Below each assistant message, a collapsible footer:
  - **"Quellen (5)"** — clicking expands to a list of retrieved chunks: source URL + first 80 chars + similarity score. Crucial for trust.
  - Token-cost line: "1.2k in / 0.8k out · ≈ 0.4¢"

**Composer (sticky bottom, 80–120px)**
- Multi-line textarea (auto-grows to 4 rows, then scrolls)
- Cmd+Enter sends. Plain Enter inserts newline (German typists expect this).
- "Senden" button right-aligned, disabled when empty or while a stream is in flight.
- "Stop" button replaces "Senden" while streaming; stops the SSE.

### Iteration loop in this layout
1. Founder edits prompt in column 2 → autosaves at 800ms.
2. Founder types in column 3 composer → sends → assistant streams.
3. Quellen footer reveals which chunks the model used; if wrong chunks, founder tweaks prompt or clicks "Erneut crawlen".
4. After several good turns, founder clicks kebab → "Letzte Antwort als Goldfrage speichern" → opens a small inline form prefilled with the question + suggested `must_contain` keywords. Saves to the bot's YAML.
5. Once 10–15 questions are saved, banner CTA "Evals ausführen" runs them; banner flips green if score ≥ threshold.
6. Compliance group fields filled → banner CTA becomes "Veröffentlichen" → click → confirmation modal → published. Diff tab is now identical to draft.

### States
- **Bot still ingesting** (KB not ready): right column shows full-bleed overlay "Wissensbasis wird vorbereitet — 23/47 Chunks". Prompt column stays interactive (founder can edit while waiting). Composer disabled with tooltip.
- **No `ANTHROPIC_API_KEY` set**: chat composer disabled, banner above messages says "LLM nicht konfiguriert — siehe `.env`".
- **Streaming error mid-message**: assistant bubble shows partial content + a small red error footer "Verbindung unterbrochen — wiederholen?" with retry button.
- **Prompt edited while a stream is active**: stream completes against old prompt; system divider auto-inserts on the next user message.

---

## Screen 4 — Eval Modal (`/admin/bots/{id}/evals`)

Direct-linkable route that *also* renders as a modal overlay on top of the editor. Closing returns to whichever screen opened it.

### Layout
- Modal width: 720px, scrolls vertically.
- **Header**:
  - Title "Evaluation — `steuerkanzlei-demo.yaml`"
  - Sub: "Lauf vom 12.05.2026, 14:32 — Score 12/15 (0.80) gegen Schwelle 0.85"
  - Right action: "Erneut ausführen"

### Body
Two collapsible sections, both expanded by default:

**1. Goldfragen-Datei (editor)**
- Plain `<textarea>` rendering the YAML (Phase 2 keeps it cheap; Monaco is Phase 4+).
- Linting: a thin status bar under the textarea — "Gültiges YAML — 15 Fragen" or "Fehler in Zeile 23: …".
- Save button → POST update; running an eval auto-saves first.

**2. Ergebnisse (per question)**
- Vertical list, one row per question:
  - Header: pass-fail icon · `q4-honorar` · question text (truncated)
  - On click: row expands to show:
    - Antwort des Bots (full text, monospaced)
    - Erwartete Begriffe (chips, green if found, red if missing)
    - Verbotene Begriffe (chips, red if found)
    - Quellen (same chunk list as in the chat — trust signal)
- Failed rows are pre-expanded; passed rows are collapsed.

### Footer
- Left: "Run-Verlauf" — small disclosure listing the last 10 `eval_runs` rows, each clickable to swap the modal contents.
- Right: "Schließen"

### States
- **No goldfragen file yet**: body shows empty state with "Beispiel laden" button → seeds the YAML with the demo questions.
- **Eval running**: row icons replaced with spinners; results stream in row-by-row as they complete (SSE).
- **LLM rate-limited mid-run**: surface the error in the row + offer "Diese Frage wiederholen".

---

## Screen 5 — Re-Crawl Overlay (in-editor)

Triggered from "Erneut crawlen" in the editor's settings rail.

- Confirmation modal: "Dieser Vorgang ersetzt 47 Chunks aus 12 Seiten. Fortfahren?"
- On confirm: a slide-down panel from the top of column 2 (workbench) showing the same live log + counters as wizard step 2. Editor remains usable; chat composer goes disabled until done.
- On completion: panel collapses, a one-line toast "Neue Wissensbasis: 51 Chunks aus 14 Seiten." Auto-dismisses after 4s.

---

## Component Inventory (new in Phase 2)

| Component | Purpose | Notes |
|-----------|---------|-------|
| Banner (3 colors) | Persistent gate signal | Sticky under topbar; no dismiss |
| Status pill | Bot lifecycle state | 4 variants: Entwurf / Eval ausstehend / Live / Blockiert |
| Tab bar | Prompt workbench tabs | 3 tabs: Entwurf / Veröffentlicht / Diff |
| Chat bubble (user/assistant) | Sandbox messages | Streams; supports retry footer |
| Quellen disclosure | RAG transparency | Collapsible list under each assistant message |
| Live-log panel | Crawl/eval streaming | Used in wizard, eval modal, re-crawl |
| Score chip | Compact score display | `12/15 (0.80)` with mini bar |
| Inline-edit field | Tap-to-edit settings | Used in Identität group |
| Sparkline | Future message-volume preview | Phase 2 placeholder; Phase 3 fills |

All components extend the Phase 1 primitives — same radius, fonts, focus rings.

---

## Keyboard shortcuts (editor)

| Combo | Action |
|-------|--------|
| Cmd/Ctrl + S | Save draft prompt (forces autosave) |
| Cmd/Ctrl + Enter (in composer) | Send chat message |
| Cmd/Ctrl + R | Reset chat history (with confirm) |
| Cmd/Ctrl + . | Stop streaming response |
| Cmd/Ctrl + Shift + E | Open eval modal |
| Cmd/Ctrl + Shift + P | Publish (only when banner green) |
| Esc | Close modal / overlay |

Browser shortcuts (Cmd+R reload, Cmd+W close) are *not* overridden — the editor uses Cmd+R-with-confirm; if the founder mashes it the browser still wins, no harm done.

---

## States summary (cross-screen)

For every screen, design must produce:

1. **Empty** — no data yet (no bots, no chunks, no evals, no chat history)
2. **Loading** — fetch in flight; skeleton bones in the surface color
3. **Error** — recoverable; inline message + retry CTA
4. **Disabled / blocked** — interaction not available; tooltip explains why (publish gate, missing key, ingest in progress)
5. **Success** — happy path; quiet, no celebration animation

---

## Accessibility & localization

- All copy in DE for Phase 2; EN strings tracked as `// TODO i18n` comments.
- Streaming chat bubbles announce via `aria-live="polite"` so screen readers don't get spammed mid-token. Final answer announced once the stream closes.
- Color is never the sole signal — banner colors pair with icons (✓ / ⚠ / ✗) and explicit copy.
- Focus order: topbar → banner CTA → settings rail → workbench → chat composer.
- Min-contrast 4.5:1 (banner text on banner color must pass — yellow banner uses dark text).

---

## Out of scope for Phase 2 design

- Multi-bot dashboard with filters/sorting (one bot in Phase 2)
- Self-service customer admin (founder-only login)
- Mobile editor layout (desktop-only)
- Internationalized AT/CH copy (single DE locale)
- Chat-history export beyond plain text (Phase 3)
- LLM-as-judge eval visualization (Phase 4)
- Audit-log viewer UI (data captured Phase 1; UI Phase 5)

---

## Open questions for design

1. **Diff tab**: render as side-by-side or unified? (Designer's call — I'd lean unified for narrow column.)
2. **"Letzte Antwort als Goldfrage speichern"** UX: inline expandable form vs popover modal? (Inline keeps the loop tight; popover risks losing chat context.)
3. **Sparkline placeholder copy**: "—" vs "Wird ab Phase 3 erfasst" — preference?
4. **Banner stickiness on small windows**: at <900px height the banner + topbar eat too much chrome — should banner collapse to a single-line minimized state when the viewport is short?
5. **Quellen footer default state**: collapsed (current spec) or expanded? Founders building a bot probably want expanded by default; visitors in Phase 3 want collapsed.

---

## Next step

Hand this brief to `/design-scout` for a competitive landscape pass and a SAFE-vs-RISK aesthetic split, or to `/ui-expert` for a pixel-spec design prompt. The PRD-phase-2 acceptance criteria are the ground truth — anything in this brief that conflicts with the PRD loses.
