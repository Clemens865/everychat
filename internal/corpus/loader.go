package corpus

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"gopkg.in/yaml.v3"

	"github.com/clemenshoenig/everychat/internal/eval"
)

//go:embed all:de
var corpusFS embed.FS

// Errors callers may want to switch on.
var (
	// ErrEmpty is returned when an industry's directory is present but
	// the YAML file has no entries (placeholder for an unfilled industry).
	// Distinguishable from a parse error so the wizard can show "this
	// industry has no corpus yet — defaults still apply".
	ErrEmpty = errors.New("corpus: empty bundle")

	// ErrUnknownIndustry is returned when Load is called with an
	// industry not in the taxonomy.
	ErrUnknownIndustry = errors.New("corpus: unknown industry")

	// ErrHoldoutOverlap is the hard invariant defender: if a holdout
	// question id appears in questions.yaml, the blind eval is
	// invalidated. Loader fails loud rather than silently letting it slip.
	ErrHoldoutOverlap = errors.New("corpus: holdout overlaps with questions")
)

// QAPair is a few-shot exemplar — a question paired with the answer
// the bot should produce. Used by internal/prompt/fewshot.go.
type QAPair struct {
	Q string `yaml:"q"`
	A string `yaml:"a"`
}

// Bundle is the full set of corpus assets for one industry.
type Bundle struct {
	Industry  Industry
	Questions []eval.Question
	Exemplars []QAPair
	Holdout   []eval.Question
}

// Load reads + validates the three YAML files under
// corpus/de/<industry>/. Returns ErrEmpty if all three are
// placeholder-blank (legitimate state for non-seeded industries),
// or a more specific error on schema/invariant failure.
func Load(industry Industry) (*Bundle, error) {
	if !Valid(string(industry)) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownIndustry, industry)
	}

	base := "de/" + string(industry)
	questions, err := loadQuestions(base + "/questions.yaml")
	if err != nil {
		return nil, fmt.Errorf("corpus.Load %s/questions.yaml: %w", industry, err)
	}
	exemplars, err := loadExemplars(base + "/exemplars.yaml")
	if err != nil {
		return nil, fmt.Errorf("corpus.Load %s/exemplars.yaml: %w", industry, err)
	}
	holdout, err := loadQuestions(base + "/holdout.yaml")
	if err != nil {
		return nil, fmt.Errorf("corpus.Load %s/holdout.yaml: %w", industry, err)
	}

	if len(questions) == 0 && len(exemplars) == 0 && len(holdout) == 0 {
		return &Bundle{Industry: industry}, ErrEmpty
	}

	if err := assertNoHoldoutOverlap(questions, holdout); err != nil {
		return nil, err
	}

	return &Bundle{
		Industry:  industry,
		Questions: questions,
		Exemplars: exemplars,
		Holdout:   holdout,
	}, nil
}

// IsEmpty is a convenience check for the wizard so it can show "no
// corpus yet" without `errors.Is(err, ErrEmpty)` plumbing.
func (b *Bundle) IsEmpty() bool {
	return b == nil || (len(b.Questions) == 0 && len(b.Exemplars) == 0 && len(b.Holdout) == 0)
}

// loadQuestions reads a questions/holdout YAML in the `bot:`+`questions:`
// shape that internal/eval already uses, so the same Question struct
// flows through eval.Run without translation.
func loadQuestions(path string) ([]eval.Question, error) {
	raw, err := fs.ReadFile(corpusFS, path)
	if err != nil {
		// Missing file = placeholder = treat as empty, not error.
		// This lets non-seeded industries ship empty content without
		// breaking the loader's per-file path.
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var file struct {
		Bot       string          `yaml:"bot"`
		Questions []eval.Question `yaml:"questions"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	seen := make(map[string]bool, len(file.Questions))
	for i, q := range file.Questions {
		if q.ID == "" {
			return nil, fmt.Errorf("question %d: missing id", i+1)
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
		if q.Ask == "" {
			return nil, fmt.Errorf("question %q: empty ask", q.ID)
		}
	}
	return file.Questions, nil
}

func loadExemplars(path string) ([]QAPair, error) {
	raw, err := fs.ReadFile(corpusFS, path)
	if err != nil {
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var file struct {
		Exemplars []QAPair `yaml:"exemplars"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	for i, e := range file.Exemplars {
		if e.Q == "" || e.A == "" {
			return nil, fmt.Errorf("exemplar %d: q and a are required", i+1)
		}
	}
	return file.Exemplars, nil
}

// assertNoHoldoutOverlap is the moat-defender. If the same question
// id appears in both files, the blind 90% eval is invalidated.
func assertNoHoldoutOverlap(questions, holdout []eval.Question) error {
	if len(questions) == 0 || len(holdout) == 0 {
		return nil
	}
	qIDs := make(map[string]bool, len(questions))
	for _, q := range questions {
		qIDs[q.ID] = true
	}
	for _, h := range holdout {
		if qIDs[h.ID] {
			return fmt.Errorf("%w: %q present in both questions.yaml and holdout.yaml", ErrHoldoutOverlap, h.ID)
		}
	}
	return nil
}
