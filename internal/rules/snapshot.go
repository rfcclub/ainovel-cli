package rules

import (
	"fmt"
	"maps"
	"strings"
)

// Snapshot is this book's normalised user-rules snapshot (meta/user_rules.json).
//
// It is the single source of truth at runtime: it is built by normalising and merging every
// source when the book starts, is imported, or is refreshed, after which novel_context's
// injection and commit_chapter's checks both read only this copy rather than re-reading the
// rules files (avoiding drift and two readers diverging).
//
// Only Structured + Preferences reach the model (see Payload); Version / Status / Sources /
// Uncertain are operational and diagnostic metadata and never enter working_memory.user_rules.
type Snapshot struct {
	Version     int        `json:"version"`
	Status      Status     `json:"status"`
	Structured  Structured `json:"structured"`
	Preferences string     `json:"preferences"`
	Sources     []string   `json:"sources"`
	Uncertain   []string   `json:"uncertain"`
}

// Status marks whether snapshot normalisation completed successfully.
type Status string

const (
	// StatusReady — every source normalised successfully.
	StatusReady Status = "ready"
	// StatusDegraded — at least one source failed to normalise and degraded to raw preferences (see Uncertain / the log).
	StatusDegraded Status = "degraded"
)

// SnapshotVersion is the current snapshot schema version, for future migrations.
// v2: chapter_words left structured (word count is a semantic soft constraint and goes
// through preferences).
// v1 snapshots load as-is: unknown fields are ignored on decode and naturally converge to v2
// on the next overlay save. Rebuilding on a version mismatch is deliberately avoided — that
// would discard the non-reproducible rules appended at runtime by AddRuntimeRule.
const SnapshotVersion = 2

// Candidate is one source's normalised result.
//
// Sources are ordered from low to high priority and handed to BuildSnapshot for deterministic
// merging. The LLM only turns one source's natural language into candidate
// Structured/Preferences; priority and field precedence are decided by BuildSnapshot (Go).
type Candidate struct {
	Source      string     // Readable source label, recorded in Snapshot.Sources (e.g. system_defaults / startup_prompt / global:my.md)
	Structured  Structured // This source's candidate structured fields
	Preferences string     // This source's natural-language preference text
	Uncertain   []string   // Items this source deliberately left out of structured, with reasons (diagnostic)
	Degraded    bool       // This source failed to normalise and degraded to raw preferences
}

// Payload returns the shape injected into working_memory.user_rules: structured and
// preferences only. It returns a stable structure even when both are empty, so the LLM never
// sees user_rules=null and takes an exceptional branch.
func (s Snapshot) Payload() map[string]any {
	return map[string]any{
		"structured":  s.Structured,
		"preferences": s.Preferences,
	}
}

// BuildSnapshot deterministically merges candidates ordered from low to high priority into a
// snapshot.
//
// Merge rules (all deterministic on the Go side, never handed to the LLM):
//   - structured: overridden per field, with higher-priority sources overriding lower ones;
//     fatigue_words accumulate per word.
//   - preferences: never overridden; concatenated in source order (highest priority last),
//     each with a source heading.
//   - Empty/zero values count as absent fields and never override an existing value
//     (sanitizeStructured).
//   - Any degraded source -> snapshot status=degraded.
func BuildSnapshot(cands []Candidate) Snapshot {
	snap := Snapshot{
		Version: SnapshotVersion,
		Status:  StatusReady,
		Sources: make([]string, 0, len(cands)),
	}
	var prefs []string
	for _, c := range cands {
		s := sanitizeStructured(c.Structured)
		if s.Genre != "" {
			snap.Structured.Genre = s.Genre
		}
		if len(s.ForbiddenChars) > 0 {
			snap.Structured.ForbiddenChars = s.ForbiddenChars
		}
		if len(s.ForbiddenPhrases) > 0 {
			snap.Structured.ForbiddenPhrases = s.ForbiddenPhrases
		}
		if len(s.FatigueWords) > 0 {
			snap.Structured.FatigueWords = mergeFatigueWords(snap.Structured.FatigueWords, s.FatigueWords)
		}

		if p := strings.TrimSpace(c.Preferences); p != "" {
			if src := strings.TrimSpace(c.Source); src != "" {
				prefs = append(prefs, fmt.Sprintf("## [%s]\n\n%s", src, p))
			} else {
				prefs = append(prefs, p)
			}
		}
		if src := strings.TrimSpace(c.Source); src != "" {
			snap.Sources = append(snap.Sources, src)
		}
		snap.Uncertain = append(snap.Uncertain, c.Uncertain...)
		if c.Degraded {
			snap.Status = StatusDegraded
		}
	}
	snap.Preferences = strings.Join(prefs, "\n\n")
	return snap
}

// OverlaySnapshot overlays a high-priority candidate onto an existing snapshot (the
// candidate wins).
//
// Used by the Arbiter rules action at runtime: instead of re-normalising every source, it
// overlays only the new rules onto the current snapshot —
// structured is overridden per field, preferences appends one block, sources/uncertain
// accumulate and degradation propagates.
func OverlaySnapshot(base Snapshot, cand Candidate) Snapshot {
	out := base
	out.Version = SnapshotVersion
	s := sanitizeStructured(cand.Structured)
	if s.Genre != "" {
		out.Structured.Genre = s.Genre
	}
	if len(s.ForbiddenChars) > 0 {
		out.Structured.ForbiddenChars = s.ForbiddenChars
	}
	if len(s.ForbiddenPhrases) > 0 {
		out.Structured.ForbiddenPhrases = s.ForbiddenPhrases
	}
	if len(s.FatigueWords) > 0 {
		out.Structured.FatigueWords = mergeFatigueWords(cloneFatigue(out.Structured.FatigueWords), s.FatigueWords)
	}
	if p := strings.TrimSpace(cand.Preferences); p != "" {
		section := p
		if src := strings.TrimSpace(cand.Source); src != "" {
			section = fmt.Sprintf("## [%s]\n\n%s", src, p)
		}
		if strings.TrimSpace(out.Preferences) == "" {
			out.Preferences = section
		} else {
			out.Preferences = out.Preferences + "\n\n" + section
		}
	}
	if src := strings.TrimSpace(cand.Source); src != "" {
		out.Sources = append(append([]string{}, out.Sources...), src)
	}
	if len(cand.Uncertain) > 0 {
		out.Uncertain = append(append([]string{}, out.Uncertain...), cand.Uncertain...)
	}
	if cand.Degraded {
		out.Status = StatusDegraded
	}
	return out
}

// mergeFatigueWords accumulates fatigue-word thresholds per word, with src overriding the
// same word in dst (nearest source wins). It lets a user add a few fatigue words without
// restating the built-in baseline.
func mergeFatigueWords(dst, src map[string]int) map[string]int {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]int, len(src))
	}
	maps.Copy(dst, src)
	return dst
}

func cloneFatigue(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	maps.Copy(out, m)
	return out
}

// SystemDefaults returns the mechanical baseline built into the code (the
// lowest-priority source).
//
// The basis: thresholds migrate from the old assets/rules/default.md front matter. The
// later batch of fatigue words comes from an empirical 196-chapter long run — once the
// earlier table of AI cliches was extinguished, the model shifted onto these "beat words"
// at 5-7 uses per chapter, so the thresholds are loosened to tolerate normal use.
//
// The baseline is Vietnamese-only. The old Chinese table was dead weight on a Vietnamese
// novel: all 16 entries silently did nothing, so the mechanical floor never fired.
func SystemDefaults() Candidate {
	return Candidate{
		Source: "system_defaults",
		Structured: Structured{
			// Fixed AI phrases; the checker does literal substring matching. Patterns
			// with a variable belong to the semantic layer.
			ForbiddenPhrases: []string{
				"một cách nào đó", "đáng chú ý là", "không hiểu vì sao", "ngũ vị tạp trần",
				"trong lúc vô thức", "không khỏi", "không thể không",
			},
			FatigueWords: map[string]int{
				"bỗng nhiên": 3, "đột nhiên": 3, "dường như": 3, "phảng phất": 2,
				"thoáng chốc": 2, "trong nháy mắt": 2, "nhất thời": 3, "bất giác": 2,
				"thật lâu": 2, "im lặng một lúc": 2, "không nói gì": 3, "hít sâu một hơi": 2,
				"siết chặt nắm đấm": 2, "ánh mắt lóe lên": 2, "khóe miệng giật giật": 2,
			},
		},
	}
}


// sanitizeStructured enforces "empty/zero value means field absent": the normaliser may
// emit placeholders such as genre:"" (observed in the prototype), which must be treated
// as undeclared so they do not pollute merging and the mechanical checks.
func sanitizeStructured(s Structured) Structured {
	out := Structured{}
	if g := strings.TrimSpace(s.Genre); g != "" {
		out.Genre = g
	}
	out.ForbiddenChars = nonEmptyStrings(s.ForbiddenChars)
	out.ForbiddenPhrases = nonEmptyStrings(s.ForbiddenPhrases)
	out.FatigueWords = sanitizeFatigueWords(s.FatigueWords)
	return out
}

func nonEmptyStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func sanitizeFatigueWords(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	for w, n := range m {
		if w = strings.TrimSpace(w); w == "" || n <= 0 {
			continue
		}
		out[w] = n
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
