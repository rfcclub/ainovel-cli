// Package eval is ainovel-cli's offline evaluation harness.
//
// Design stance: the evaluators (deterministic diagnostics via diag, book-wide style via
// stylestat, the seven-dimension rubric) already exist in the project, so eval stays a thin
// layer — drive cases in bulk, collect output, map diag Findings and case contracts onto gates,
// aggregate the report. One definition of the facts, never a second judgement rewritten in the
// evaluation layer. See docs/evaluation-system.md.
//
// The deterministic mainline is covered today: single-arm gates, baseline/variant A/B deltas,
// repeat aggregation and stylestat regression. An LLM Judge remains an optional later layer and
// must not pollute the deterministic gates.
package eval

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// caseIDPattern restricts a case id to safe characters: the id is joined into the output directory
// and cleaned up by RunCase's RemoveAll, so path characters such as . and / are forbidden,
// preventing a "../" traversal from deleting outside the workspace.
var caseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const defaultDeltaRatio = 0.3

// Case is one evaluation sample: a writing requirement plus a set of fact-layer assertions.
type Case struct {
	ID            string   `json:"id"`
	Category      string   `json:"category"`       // Evaluation layer: smoke/workflow/quality/longform/recovery/steering
	Role          string   `json:"role,omitempty"` // Role under test: writer/architect/editor (orthogonal to Category)
	Description   string   `json:"description,omitempty"`
	Prompt        string   `json:"prompt"`                   // User writing requirement
	Style         string   `json:"style,omitempty"`          // Overrides the configured style
	MaxChapters   int      `json:"max_chapters"`             // Chapter cap; 0 means stop once planning completes (entering writing)
	TargetPrompts []string `json:"target_prompts,omitempty"` // Prompt files this case mainly exercises (informational)
	Rubric        string   `json:"rubric,omitempty"`         // LLM Judge rubric (enabled in Phase 3)
	Expect        Expect   `json:"expect"`
	Gate          Gate     `json:"gate"`
}

// Expect holds case-level contract assertions — only expectations the generic diag rules cannot
// cover and that are tightly tied to this case.
type Expect struct {
	Phase                string   `json:"phase,omitempty"`                  // Expected final phase
	MinCompletedChapters int      `json:"min_completed_chapters,omitempty"` // Minimum chapters completed
	RequiredCheckpoints  []string `json:"required_checkpoints,omitempty"`   // Of the form "chapter:1:commit" / "arc:1:1:arc_summary" / "global:layered_outline"
	NoPending            []string `json:"no_pending,omitempty"`             // Signals that must be clear on exit: pending_commit/pending_steer/last_commit/last_review
}

// Gate holds this case's gate thresholds. Only MaxSeverity is used for now; the other fields are
// reserved for the A/B (regression) stage and are parsed without affecting the gate — they are kept
// so case files can be written against the full schema in docs/evaluation-system.md.
type Gate struct {
	MaxSeverity string `json:"max_severity,omitempty"` // Highest severity a diag Finding may have (default warning); exceeding it is a hard fail

	MaxCostDeltaRatio     *float64 `json:"max_cost_delta_ratio,omitempty"`
	MaxToolCallDeltaRatio *float64 `json:"max_tool_call_delta_ratio,omitempty"`
	StylestatRegression   string   `json:"stylestat_regression,omitempty"`
}

// Validate checks a case's required fields.
func (c *Case) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("case thiếu id")
	}
	if !caseIDPattern.MatchString(c.ID) {
		return fmt.Errorf("case id không hợp lệ %q: chỉ cho phép chữ thường/số/gạch dưới/gạch nối, và không chứa ký tự đường dẫn", c.ID)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return fmt.Errorf("case %q thiếu prompt", c.ID)
	}
	if c.Gate.MaxSeverity == "" {
		c.Gate.MaxSeverity = "warning"
	}
	if !validSeverity(c.Gate.MaxSeverity) {
		return fmt.Errorf("gate.max_severity của case %q không hợp lệ: %s", c.ID, c.Gate.MaxSeverity)
	}
	if c.Gate.MaxCostDeltaRatio == nil {
		c.Gate.MaxCostDeltaRatio = float64Ptr(defaultDeltaRatio)
	}
	if c.Gate.MaxToolCallDeltaRatio == nil {
		c.Gate.MaxToolCallDeltaRatio = float64Ptr(defaultDeltaRatio)
	}
	if c.Gate.StylestatRegression == "" {
		c.Gate.StylestatRegression = "warn"
	}
	if !validStylestatGate(c.Gate.StylestatRegression) {
		return fmt.Errorf("gate.stylestat_regression của case %q không hợp lệ: %s", c.ID, c.Gate.StylestatRegression)
	}
	return nil
}

func float64Ptr(v float64) *float64 { return &v }

func validStylestatGate(s string) bool {
	switch s {
	case "warn", "block", "off":
		return true
	default:
		return false
	}
}

// LoadCases loads cases from a single .json file or a directory. Every *.json under the directory
// is loaded recursively and sorted by id.
func LoadCases(path string) ([]Case, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var files []string
	if info.IsDir() {
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".json") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		files = []string{path}
	}

	var cases []Case
	seen := map[string]string{}
	for _, f := range files {
		c, err := loadCaseFile(f)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("case id bị lặp: %q (%s và %s)", c.ID, prev, f)
		}
		seen[c.ID] = f
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("không tìm thấy case nào: %s", path)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

func loadCaseFile(path string) (Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	var c Case
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // A misspelled field errors out immediately instead of being silently ignored.
	if err := dec.Decode(&c); err != nil {
		return Case{}, fmt.Errorf("phân tích case %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Case{}, err
	}
	return c, nil
}
