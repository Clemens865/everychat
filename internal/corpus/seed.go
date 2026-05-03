package corpus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/clemenshoenig/everychat/internal/storage"
)

// EvalSeedDir is where corpus.Seed writes per-bot eval YAML files. Kept
// at the repo-root location existing tests/eval/ already uses so the
// `everychat eval` CLI works without a path-resolution change.
const EvalSeedDir = "tests/eval"

// safeSlug is the cap on bot-name characters used in the seed filename.
// Restrictive on purpose — anything outside [a-z0-9_-] gets stripped so
// the path stays well-defined regardless of admin-input weirdness.
var safeSlug = regexp.MustCompile(`[^a-z0-9_-]+`)

// Seed copies the industry's `questions.yaml` into a per-bot file under
// EvalSeedDir, persists the path to `bots.eval_questions_path`, and
// flips `bots.eval_mode` to `llm_judge` so corpus-seeded bots default
// to the semantic scorer (Sprint 4 ships the judge implementation;
// until then the runner falls back to keyword for keyword-mode bots).
//
// If the industry has no corpus content (placeholder), Seed is a no-op
// and returns nil — the wizard's lifecycle stays linear.
func Seed(ctx context.Context, db *sql.DB, botID int64, botName string, industry Industry) error {
	bundle, err := Load(industry)
	if err != nil {
		if errors.Is(err, ErrEmpty) {
			return nil // placeholder industry — nothing to seed yet
		}
		return fmt.Errorf("Seed: load corpus: %w", err)
	}
	if len(bundle.Questions) == 0 {
		return nil
	}

	// Read the source questions.yaml byte-for-byte so the seeded file
	// exactly matches what the corpus PR-review approved. Avoids any
	// re-marshal-shape drift between curators and the runner.
	src := "de/" + string(industry) + "/questions.yaml"
	raw, err := fs.ReadFile(corpusFS, src)
	if err != nil {
		return fmt.Errorf("Seed: read embedded %s: %w", src, err)
	}

	if err := os.MkdirAll(EvalSeedDir, 0o750); err != nil {
		return fmt.Errorf("Seed: mkdir %s: %w", EvalSeedDir, err)
	}
	slug := slugify(botName)
	path := filepath.Join(EvalSeedDir, slug+".yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("Seed: write %s: %w", path, err)
	}

	if err := storage.SetEvalQuestionsPath(ctx, db, botID, path); err != nil {
		return err
	}
	if err := storage.SetEvalMode(ctx, db, botID, "llm_judge"); err != nil {
		return err
	}
	return nil
}

// slugify trims + lowercases + restricts a bot name to [a-z0-9_-].
// Empty input returns "bot" so we always have a stable filename.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = safeSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "bot"
	}
	return s
}
