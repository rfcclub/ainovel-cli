// Package rules implements the input layer for user preferences (Policy): it normalises
// the writing rules from every source and merges them into this book's snapshot (see
// snapshot.go), which novel_context injects at runtime and commit_chapter checks mechanically.
//
// A Rule is a fourth kind of fact, alongside Progress / Checkpoint / Artifact, but with
// the opposite nature: the first three are system output, whereas a Rule is persisted
// user intent.
//
// Design constraints (non-negotiable):
//   - Tools return facts, never instructions (a Violation is a fact; the editor decides
//     whether it triggers a rewrite).
//   - No new verdict path (reuse PendingRewrites).
//   - No strictness field (severity maps fixedly from the rule type; the editor judges
//     semantics itself).
//   - Do not touch the Flow Router (rules do not participate in routing).
package rules

// SourceKind marks where a rules file came from; it is only used to build the source label (e.g. global:my-style.md).
type SourceKind int

const (
	// SourceGlobal — global user preferences (every .md under ~/.ainovel/rules/, merged in filename order), reused across books.
	SourceGlobal SourceKind = iota
	// SourceProject — this book's rules (every .md under ./.ainovel/rules/, merged in filename order), highest priority.
	SourceProject
)

// String returns the readable source name used as the label prefix.
func (k SourceKind) String() string {
	switch k {
	case SourceGlobal:
		return "global"
	case SourceProject:
		return "project"
	default:
		return "unknown"
	}
}

// Structured carries the mechanically checkable structured rule fields (the candidate or
// merged result after normalising every source).
// Chapter length is deliberately excluded: how long a chapter should be is a matter of
// narrative completeness and therefore of semantic judgement (writer/editor). Turning it
// into a mechanical line would push the model to pad to reach it — word-count wishes go
// through the natural-language preferences channel instead.
type Structured struct {
	Genre            string         `json:"genre,omitempty"`
	ForbiddenChars   []string       `json:"forbidden_chars,omitempty"`
	ForbiddenPhrases []string       `json:"forbidden_phrases,omitempty"`
	FatigueWords     map[string]int `json:"fatigue_words,omitempty"`
}

// IsEmpty reports whether there are no structured rules at all, letting the checker skip early.
func (s Structured) IsEmpty() bool {
	return s.Genre == "" &&
		len(s.ForbiddenChars) == 0 &&
		len(s.ForbiddenPhrases) == 0 &&
		len(s.FatigueWords) == 0
}

// Severity marks a Violation's severity level.
// Fixed mapping (not user-configurable):
//
//	forbidden_chars present            -> Error
//	forbidden_phrases present          -> Error
//	fatigue_words over threshold       -> Warning
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Violation is the checker's output: a factual statement that this chapter broke one
// mechanical rule.
//
// Note: commit_chapter passes violations through into the returned JSON without blocking
// the commit; when reviewing, the editor maps these facts onto the existing seven
// dimensions (aesthetic/pacing/character/consistency) and the LLM decides on its own
// whether to escalate the verdict and trigger polish/rewrite.
type Violation struct {
	Rule     string   `json:"rule"`             // forbidden_chars / forbidden_phrases / fatigue_words
	Target   string   `json:"target,omitempty"` // The offending item (which word/character)
	Limit    any      `json:"limit,omitempty"`  // Threshold; fatigue_words=int / forbidden_*=empty
	Actual   any      `json:"actual"`           // Actual value: occurrence count
	Severity Severity `json:"severity"`         // error / warning
}
