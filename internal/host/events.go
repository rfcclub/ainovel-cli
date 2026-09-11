package host

import (
	"time"
)

// Event is the structured event consumed by the TUI.
//
// For the three call event kinds TOOL / DISPATCH / DECISION, the start and end of one call share an ID:
// the start emits an event with a zero FinishedAt (rendered by the TUI in the "in progress" style); the
// end emits another with the same ID carrying FinishedAt + Duration (+ Failed), and the TUI locates the
// original row by ID and updates it in place, avoiding the redundancy of one row for the start and
// another for the completion.
//
// Non-call events such as SYSTEM / ERROR / CONTEXT carry an empty ID and are appended independently.
type Event struct {
	ID         string    // Shared by the start/end of one call; empty for non-call events
	Time       time.Time // First emission time (start moment)
	FinishedAt time.Time // Zero value = in progress; non-zero = finished
	Failed     bool      // Finished but failed (only meaningful once finished)
	Category   string    // DISPATCH / TOOL / DECISION / SYSTEM / REVIEW / CHECK / ERROR / CONTEXT
	Agent      string    // Agent that produced the event
	Summary    string
	Detail     string        // Full text, written to the log untruncated for debugging; falls back to Summary when empty. The UI reads Summary only
	Kind       string        // Error category (e.g. stream_idle), emitted with the log for filtering/alerting; empty means no output
	Level      string        // info / warn / error / success
	Depth      int           // 0 = Engine layer, 1 = Worker layer
	Duration   time.Duration // Execution time on completion
	RetryAt    time.Time     // Retry events: deadline of the next retry; the UI renders a per-second countdown and clears it on expiry (the request is already in flight)
}

// Running reports whether the event is still in progress.
// Only call events (TOOL / DISPATCH / DECISION with an ID) can be in progress; every other kind always returns false.
func (e Event) Running() bool {
	return e.hasLifecycle() && e.FinishedAt.IsZero()
}

func (e Event) hasLifecycle() bool {
	if e.ID == "" {
		return false
	}
	switch e.Category {
	case "TOOL", "DISPATCH", "DECISION":
		return true
	default:
		return false
	}
}

// UISnapshot is the aggregated state snapshot the TUI needs to render.
type UISnapshot struct {
	Provider             string
	BookTitle            string
	ModelName            string
	ModelContextWindow   int // Context window of the current default model (resolved live as /model switches)
	ThinkingLevel        string
	Style                string
	RuntimeState         string // idle / running / pausing / paused / completed
	StatusLabel          string
	Phase                string
	Flow                 string
	CurrentChapter       int
	TotalChapters        int
	CompletedCount       int
	TotalWordCount       int
	InProgressChapter    int
	PendingRewrites      []int
	RewriteReason        string
	PendingSteer         string
	AdvanceMode          string
	AdvancePermitChapter int
	HasAdvanceHold       bool
	AdvanceHoldReason    string
	RecoveryLabel        string
	IsRunning            bool
	Agents               []AgentSnapshot

	// Accumulated usage (whole session, across every agent and model switch)
	TotalInputTokens      int
	TotalOutputTokens     int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	TotalCostUSD          float64
	TotalSavedUSD         float64 // Dollars saved by CacheRead hits (versus billing every input token at the non-cached price)
	BudgetLimitUSD        float64 // Budget limit (config budget.book_usd); 0 = disabled

	// Cache diagnostics
	OverallCacheCapable    bool // At least one role ran a prompt-cache-capable model (distinguishes "not enabled" from "0% hit rate")
	OverallRecentCacheRead int  // Sum of cacheRead over the last N samples in the sliding window
	OverallRecentInput     int  // Sum of input over the last N samples in the sliding window
	OverallRecentSamples   int  // Sample count in the sliding window (<= recentSampleCap)
	TotalCacheBreaks       int  // Cache-chain breaks detected live (hit rate collapses while the prefix does not shrink); see usage.go noteCacheBreak

	// MissingAssistantUsage > 0 usually means the upstream stream never sent the final usage chunk
	// required by OpenAI's stream_options.include_usage (common with self-hosted proxies), leaving
	// UsageTracker with no accumulated data at all. The UI uses this to point the user at the backend
	// rather than let them think the cache module itself is broken.
	MissingAssistantUsage int

	// Cache per-role dimensions, ordered by descending CacheRead, with roles that consumed no tokens filtered out
	CachePerAgent []AgentCacheStat
	CachePerModel []AgentCacheStat

	// Foundation
	Synopsis         string
	Premise          string
	Outline          []OutlineSnapshot
	Characters       []string
	SupportingCount  int      // Total secondary characters in the supporting-cast roster
	RecentSupporting []string // Recently active secondary characters (at most 5, by LastSeenChapter descending)
	Layered          bool
	CurrentVolumeArc string
	NextVolumeTitle  string
	CompassDirection string
	CompassScale     string

	// Details
	LastCommitSummary  string
	LastReviewSummary  string
	LastCheckpointName string
	RecentSummaries    []string
}

// OutlineSnapshot is the display summary of an outline entry.
type OutlineSnapshot struct {
	Chapter   int
	Title     string
	CoreEvent string
}

// AgentSnapshot is the display projection of an agent's state.
type AgentSnapshot struct {
	Name      string
	State     string
	TaskID    string
	TaskKind  string
	Summary   string
	Tool      string
	Turn      int
	Context   AgentContextSnapshot
	UpdatedAt time.Time
}

// AgentCacheStat is one agent's accumulated cache hits (projected into the left panel).
// HitRate = CacheRead / Input; Input is uniformly "including CacheRead" at the litellm layer.
//
// CacheCapable distinguishes the two kinds of 0% hit rate:
//   - true  → the model supports prompt caching, so 0% means poor prompt design or an unstable prefix
//     and warrants optimisation
//   - false → the model/provider does not support prompt caching, so 0% is expected and needs no
//     investigation
//
// Recent* is the sliding window (last N calls) of hit data; comparing it against the cumulative figures separates "an early drag" from "a steady low hit rate".
type AgentCacheStat struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// AgentContextSnapshot is an agent's context usage.
type AgentContextSnapshot struct {
	Tokens          int
	ContextWindow   int
	Percent         float64
	Scope           string
	Strategy        string
	ActiveMessages  int
	SummaryMessages int
	CompactedCount  int
	KeptCount       int
}

// CoCreateMessage is a message in a cocreation conversation.
type CoCreateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CoCreateReply is the LLM reply in a cocreation conversation. Raw keeps the model's complete
// four-part original text so it can be written back into history and let the next round see its own
// previous [DRAFT], genuinely accumulating updates on the existing draft (Message alone lacks the
// [DRAFT] and would make the model re-derive from the conversation every round).
// Suggestions are the AI's proactive "what you might want to say next", which the user can drop into the input box with one number key when stuck.
type CoCreateReply struct {
	Message     string
	Prompt      string
	Ready       bool
	Suggestions []string
	Raw         string
}
