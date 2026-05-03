package corpus

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/clemenshoenig/everychat/internal/storage"
)

// chdirTo points the working directory at a fresh tempdir so Seed's
// EvalSeedDir-relative writes don't pollute the repo. t.Cleanup
// restores the original CWD.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestSeed_PlaceholderIndustryIsNoop(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)

	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	bots, _ := storage.ListBots(context.Background(), db)
	id := bots[0].ID

	// versicherung is a placeholder in Sprint 1 — Seed must not error
	// and must not write a file.
	if err := Seed(context.Background(), db, id, "demo", Versicherung); err != nil {
		t.Fatalf("Seed (placeholder): %v", err)
	}
	if _, err := os.Stat(filepath.Join(EvalSeedDir, "demo.yaml")); !os.IsNotExist(err) {
		t.Fatalf("placeholder industry should not write file; stat=%v", err)
	}

	// And the bot's eval_questions_path stays nil.
	bot, _ := storage.LoadBot(context.Background(), db, id)
	if bot.EvalQuestionsPath.Valid {
		t.Errorf("eval_questions_path should remain null for placeholder industry, got %q", bot.EvalQuestionsPath.String)
	}
}

func TestSlugify_StripsUnsafeChars(t *testing.T) {
	cases := map[string]string{
		"steuerkanzlei-demo":       "steuerkanzlei-demo",
		"Steuerkanzlei Mustermann": "steuerkanzlei-mustermann",
		"Müller & Sohn / GmbH":     "m-ller-sohn-gmbh",
		"  spaces  ":               "spaces",
		"":                         "bot",
		"---":                      "bot",
		"123_test":                 "123_test",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestSeed_WritesFileAndPersistsPath_WithRealCorpus(t *testing.T) {
	// This test runs against whatever Sprint 6 actually shipped under
	// internal/corpus/de/steuerberater/. Before Sprint 6 the file is
	// a placeholder — in that case the test logs and skips instead
	// of failing the regression bar.
	bundle, err := Load(Steuerberater)
	if err != nil || len(bundle.Questions) == 0 {
		t.Skipf("steuerberater corpus is still a placeholder (sprint 6 fills it): err=%v len=%d",
			err, func() int {
				if bundle == nil {
					return 0
				}
				return len(bundle.Questions)
			}())
	}

	dir := t.TempDir()
	chdirTo(t, dir)

	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	bots, _ := storage.ListBots(context.Background(), db)
	id := bots[0].ID

	if err := Seed(context.Background(), db, id, "Steuerkanzlei Mustermann", Steuerberater); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	want := filepath.Join(EvalSeedDir, "steuerkanzlei-mustermann.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected file at %s, got %v", want, err)
	}
	bot, _ := storage.LoadBot(context.Background(), db, id)
	if !bot.EvalQuestionsPath.Valid || bot.EvalQuestionsPath.String != want {
		t.Errorf("eval_questions_path = %v, want %q", bot.EvalQuestionsPath, want)
	}
	if bot.EvalMode != "llm_judge" {
		t.Errorf("eval_mode = %q, want llm_judge after corpus seed", bot.EvalMode)
	}
}
