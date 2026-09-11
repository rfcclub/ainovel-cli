package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/stylestat"
)

// Collected is the read-only collection from one run. Everything comes from the existing evaluators
// and the fact layer; eval never recomputes it itself.
type Collected struct {
	Dir         string
	Report      diag.Report // diag.Diagnose: artefacts + runtime Findings + Stats
	Progress    *domain.Progress
	Checkpoints []domain.Checkpoint
	Pending     map[string]bool // Residual signals: pending_commit/pending_steer/last_commit/last_review
	LoadErrors  []string        // Real read failures of contract-dependent artefacts (not "absent"); Grade hard-fails on these
	RuntimeErr  string          // Runtime error captured by the runner (hard fail); empty = none
	Style       StyleCollection
	Usage       UsageMetrics
	ToolCalls   int
}

// StyleCollection is the book-wide style fact collected from the final chapter texts.
type StyleCollection struct {
	Status string           `json:"status"` // ok / insufficient_sample
	Stats  *stylestat.Stats `json:"stats,omitempty"`
}

// UsageMetrics is the reliable cost/token fact already present in meta/usage.json.
type UsageMetrics struct {
	Input         int     `json:"input,omitempty"`
	Output        int     `json:"output,omitempty"`
	CacheRead     int     `json:"cache_read,omitempty"`
	CacheWrite    int     `json:"cache_write,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
	MissingUsage  int     `json:"missing_usage,omitempty"`
	UsageRecorded bool    `json:"usage_recorded"`
}

// Collect performs offline collection on a finished output directory. runtimeErr is an error from
// driving the runner, if any.
// Artefact read errors are never swallowed: a missing file counts as "no data", while anything else
// (corruption, permission) goes into LoadErrors, so that "could not read the pending file" is not
// misjudged as "there is no pending" and passed falsely (fail-loud).
func Collect(dir string, runtimeErr error) Collected {
	s := store.NewStore(dir)
	rep, _ := diag.Diagnose(s)

	var loadErrors []string
	check := func(name string, err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			loadErrors = append(loadErrors, fmt.Sprintf("%s: %v", name, err))
		}
	}

	prog, err := s.Progress.Load()
	check("progress", err)
	cks := s.Checkpoints.All()

	pending := map[string]bool{}
	pc, err := s.Signals.LoadPendingCommit()
	check("pending_commit", err)
	if pc != nil {
		pending["pending_commit"] = true
	}
	lc, err := s.Signals.LoadLastCommit()
	check("last_commit", err)
	if lc != nil {
		pending["last_commit"] = true
	}
	lr, err := s.Signals.LoadLastReviewSignal()
	check("last_review", err)
	if lr != nil {
		pending["last_review"] = true
	}
	rm, err := s.RunMeta.Load()
	check("run_meta", err)
	if rm != nil && rm.PendingSteer != "" {
		pending["pending_steer"] = true
	}
	style := collectStyle(s, prog, check)
	usage := collectUsage(s, check)
	toolCalls := countToolCalls(dir, check)

	errStr := ""
	if runtimeErr != nil {
		errStr = runtimeErr.Error()
	}
	return Collected{
		Dir:         dir,
		Report:      rep,
		Progress:    prog,
		Checkpoints: cks,
		Pending:     pending,
		LoadErrors:  loadErrors,
		RuntimeErr:  errStr,
		Style:       style,
		Usage:       usage,
		ToolCalls:   toolCalls,
	}
}

func collectStyle(s *store.Store, prog *domain.Progress, check func(string, error)) StyleCollection {
	input := stylestat.Input{}
	if prog != nil {
		chapters := append([]int(nil), prog.CompletedChapters...)
		sort.Ints(chapters)
		for _, ch := range chapters {
			text, err := s.Drafts.LoadChapterText(ch)
			check(fmt.Sprintf("chapter:%d", ch), err)
			if strings.TrimSpace(text) == "" {
				check(fmt.Sprintf("chapter:%d", ch), fmt.Errorf("progress đánh dấu đã hoàn thành nhưng bản chung cuộc rỗng"))
				continue
			}
			input.Chapters = append(input.Chapters, text)
			input.Titles = append(input.Titles, chapterTitle(s, ch, text, check))
		}
	}
	chars, err := s.Characters.Load()
	check("characters", err)
	for _, c := range chars {
		if c.Name != "" {
			input.Stopwords = append(input.Stopwords, c.Name)
		}
		input.Stopwords = append(input.Stopwords, c.Aliases...)
	}

	stats := stylestat.Compute(input)
	if stats == nil {
		return StyleCollection{Status: "insufficient_sample"}
	}
	return StyleCollection{Status: "ok", Stats: stats}
}

func chapterTitle(s *store.Store, chapter int, text string, check func(string, error)) string {
	summary, err := s.Summaries.LoadSummary(chapter)
	check(fmt.Sprintf("summary:%d", chapter), err)
	if summary != nil && strings.TrimSpace(summary.Title) != "" {
		return summary.Title
	}
	entries, err := s.Outline.LoadOutline()
	check("outline", err)
	for _, entry := range entries {
		if entry.Chapter == chapter && strings.TrimSpace(entry.Title) != "" {
			return entry.Title
		}
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	check("layered_outline", err)
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for _, entry := range a.Chapters {
				if entry.Chapter == chapter && strings.TrimSpace(entry.Title) != "" {
					return entry.Title
				}
			}
		}
	}
	return firstMarkdownTitle(text)
}

func firstMarkdownTitle(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, "#"))
	}
	return ""
}

func collectUsage(s *store.Store, check func(string, error)) UsageMetrics {
	state, err := s.Usage.Load()
	check("usage", err)
	if state == nil {
		return UsageMetrics{}
	}
	return UsageMetrics{
		Input:         state.Overall.Input,
		Output:        state.Overall.Output,
		CacheRead:     state.Overall.CacheRead,
		CacheWrite:    state.Overall.CacheWrite,
		CostUSD:       state.Overall.Cost,
		MissingUsage:  state.MissingUsage,
		UsageRecorded: true,
	}
}

type sessionLine struct {
	agentcore.Message
}

func countToolCalls(dir string, check func(string, error)) int {
	sessionDir := filepath.Join(dir, "meta", "sessions")
	var total int
	err := filepath.WalkDir(sessionDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		n, err := countToolCallsInFile(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		check("sessions", err)
	}
	return total
}

func countToolCallsInFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var total int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var sl sessionLine
		if err := json.Unmarshal(sc.Bytes(), &sl); err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		total += len(sl.Message.ToolCalls())
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

// HasCheckpoint reports whether any collected checkpoint matches the spec.
// spec looks like "chapter:1:commit" / "arc:1:1:arc_summary" / "volume:1:volume_summary" / "global:layered_outline".
func (c Collected) HasCheckpoint(spec string) (bool, error) {
	scope, step, err := parseCheckpointSpec(spec)
	if err != nil {
		return false, err
	}
	for _, ck := range c.Checkpoints {
		if ck.Step == step && ck.Scope == scope {
			return true, nil
		}
	}
	return false, nil
}

// parseCheckpointSpec parses a contract string into (Scope, step).
func parseCheckpointSpec(spec string) (domain.Scope, string, error) {
	parts := strings.Split(spec, ":")
	bad := func() (domain.Scope, string, error) {
		return domain.Scope{}, "", fmt.Errorf("contract checkpoint không hợp lệ: %q", spec)
	}
	if len(parts) < 2 {
		return bad()
	}
	kind := parts[0]
	switch domain.ScopeKind(kind) {
	case domain.ScopeChapter: // chapter:N:step
		if len(parts) != 3 {
			return bad()
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeChapter, Chapter: n}, parts[2], nil
	case domain.ScopeArc: // arc:V:A:step
		if len(parts) != 4 {
			return bad()
		}
		v, err1 := strconv.Atoi(parts[1])
		a, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeArc, Volume: v, Arc: a}, parts[3], nil
	case domain.ScopeVolume: // volume:V:step
		if len(parts) != 3 {
			return bad()
		}
		v, err := strconv.Atoi(parts[1])
		if err != nil {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeVolume, Volume: v}, parts[2], nil
	case domain.ScopeGlobal: // global:step
		if len(parts) != 2 {
			return bad()
		}
		return domain.Scope{Kind: domain.ScopeGlobal}, parts[1], nil
	default:
		return bad()
	}
}
