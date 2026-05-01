// Package eval is the Phase 2 publish-gate quality bar. It runs a bot's
// system prompt against a YAML file of golden questions and reports a
// pass/fail scorecard. Bots that score below their `eval_threshold` are
// blocked from publishing.
//
// Phase 2 keeps scoring keyword-based: a question passes iff every entry
// in `must_contain` appears (case-insensitive) AND no entry in
// `must_not_contain` appears. LLM-as-judge scoring is intentionally
// deferred to Phase 4 to keep eval cost predictable.
package eval

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// File is the on-disk schema for golden-questions YAML.
//
//	bot: steuerkanzlei-demo
//	questions:
//	  - id: q1-leistungen
//	    ask: "Welche Leistungen bietet die Kanzlei an?"
//	    must_contain: ["Steuerberatung", "Lohnbuchhaltung"]
//	    must_not_contain: ["Rechtsberatung"]
//	    max_tokens: 400
type File struct {
	Bot       string     `yaml:"bot"`
	Questions []Question `yaml:"questions"`
}

// Question is one entry in the golden-questions list.
type Question struct {
	ID             string   `yaml:"id"`
	Ask            string   `yaml:"ask"`
	MustContain    []string `yaml:"must_contain"`
	MustNotContain []string `yaml:"must_not_contain"`
	MaxTokens      int      `yaml:"max_tokens"`
}

// Validate returns the first structural problem with f, or nil.
func (f *File) Validate() error {
	if strings.TrimSpace(f.Bot) == "" {
		return errors.New("eval: yaml is missing `bot:` field")
	}
	if len(f.Questions) == 0 {
		return errors.New("eval: at least one question is required")
	}
	seen := make(map[string]bool, len(f.Questions))
	for i, q := range f.Questions {
		if strings.TrimSpace(q.ID) == "" {
			return fmt.Errorf("eval: question %d is missing `id:`", i+1)
		}
		if seen[q.ID] {
			return fmt.Errorf("eval: duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
		if strings.TrimSpace(q.Ask) == "" {
			return fmt.Errorf("eval: question %q has empty `ask:`", q.ID)
		}
	}
	return nil
}

// Parse reads a golden-questions YAML byte slice and validates it.
func Parse(b []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("eval: parse yaml: %w", err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Item is the scored result for a single question.
type Item struct {
	ID           string    `json:"id"`
	Question     string    `json:"question"`
	Answer       string    `json:"answer"`
	Pass         bool      `json:"pass"`
	Reasons      []string  `json:"reasons,omitempty"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

// Report is the full scorecard for one eval run.
type Report struct {
	Bot           string    `json:"bot"`
	QuestionsFile string    `json:"questions_file"`
	Total         int       `json:"total"`
	Passed        int       `json:"passed"`
	Score         float64   `json:"score"`
	Threshold     float64   `json:"threshold"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Items         []Item    `json:"items"`
}

// MeetsThreshold reports whether the run's score >= threshold.
func (r *Report) MeetsThreshold() bool { return r.Score >= r.Threshold }

// scoreAnswer applies the keyword rules and returns (pass, reasons) where
// reasons is empty on pass and lists the failing checks otherwise.
func scoreAnswer(q Question, answer string) (bool, []string) {
	lower := strings.ToLower(answer)
	var reasons []string
	for _, kw := range q.MustContain {
		if !strings.Contains(lower, strings.ToLower(kw)) {
			reasons = append(reasons, fmt.Sprintf("missing keyword %q", kw))
		}
	}
	for _, kw := range q.MustNotContain {
		if strings.Contains(lower, strings.ToLower(kw)) {
			reasons = append(reasons, fmt.Sprintf("forbidden keyword %q present", kw))
		}
	}
	return len(reasons) == 0, reasons
}
