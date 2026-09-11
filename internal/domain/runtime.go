package domain

import (
	"fmt"
	"strings"
)

// Phase is a stage of novel writing.
type Phase string

const (
	PhaseInit     Phase = "init"
	PhasePremise  Phase = "premise"
	PhaseOutline  Phase = "outline"
	PhaseWriting  Phase = "writing"
	PhaseComplete Phase = "complete"
)

// FlowState is the type of the currently active flow, used for checkpoint recovery.
type FlowState string

const (
	FlowWriting   FlowState = "writing"
	FlowReviewing FlowState = "reviewing"
	FlowRewriting FlowState = "rewriting"
	FlowPolishing FlowState = "polishing"
	FlowSteering  FlowState = "steering"
)

// PlanningTier is the length tier a work is planned at.
type PlanningTier string

const (
	PlanningTierShort PlanningTier = "short"
	PlanningTierMid   PlanningTier = "mid"
	PlanningTierLong  PlanningTier = "long"
)

// Progress tracks progress and persists to meta/progress.json.
type Progress struct {
	Phase          Phase `json:"phase"`
	CurrentChapter int   `json:"current_chapter"`
	// TotalChapters is the detailed-outline chapter count in non-layered mode; in layered mode
	// it is only an internal capacity figure including skeleton estimates, used for context
	// strategy and never representing a fixed total for the whole book.
	TotalChapters     int         `json:"total_chapters"`
	CompletedChapters []int       `json:"completed_chapters"`
	TotalWordCount    int         `json:"total_word_count"`
	ChapterWordCounts map[int]int `json:"chapter_word_counts,omitempty"` // Per-chapter word counts, letting a rewrite correct the total
	InProgressChapter int         `json:"in_progress_chapter,omitempty"` // Chapter currently being written (scene-level recovery)
	CompletedScenes   []int       `json:"completed_scenes,omitempty"`    // Completed scene indices of the current chapter
	Flow              FlowState   `json:"flow,omitempty"`                // Current flow
	PendingRewrites   []int       `json:"pending_rewrites,omitempty"`    // Queue of chapters pending rewrite
	RewriteReason     string      `json:"rewrite_reason,omitempty"`      // Rewrite reason
	StrandHistory     []string    `json:"strand_history,omitempty"`      // Records dominant_strand in chapter order
	HookHistory       []string    `json:"hook_history,omitempty"`        // Records hook_type in chapter order
	// Layered long-form tracking (used only in long-form mode; zero for short/mid).
	CurrentVolume int  `json:"current_volume,omitempty"`
	CurrentArc    int  `json:"current_arc,omitempty"`
	Layered       bool `json:"layered,omitempty"`
	// ReopenedFromComplete marks a book reopened into rework from the completed state via
	// reopen. Rework only edits existing chapters and neither adds nor removes structure, so
	// once drained it should pass as "structurally complete, therefore complete again" (this
	// avoids a foreshadow disturbed by rework at the end of the final volume leaving the book
	// stuck in a writing -> out-of-range continuation loop). Forward writing never sets this
	// marker, so completion keeps its conservative thread-convergence semantics.
	ReopenedFromComplete bool `json:"reopened_from_complete,omitempty"`
	// ReopenCount records how many times this book has been reopened from the completed state
	// (a /reopen audit fact). It also guarantees that the re-completion differs in content from
	// the previous completion's progress.json: checkpoints dedupe idempotently by digest, so a
	// byte-identical re-completion creates no new checkpoint and the StopGuard would misread a
	// successful complete_book as spinning and escalate to termination.
	ReopenCount int `json:"reopen_count,omitempty"`
}

// IsResumable reports whether the run can be resumed from a breakpoint.
func (p *Progress) IsResumable() bool {
	return p.Phase == PhaseWriting && p.CurrentChapter > 0
}

// NextChapter returns the chapter number to write next.
func (p *Progress) NextChapter() int {
	return p.LatestCompleted() + 1
}

// LatestCompleted returns the highest completed chapter number, or 0 when none are complete.
func (p *Progress) LatestCompleted() int {
	max := 0
	for _, ch := range p.CompletedChapters {
		if ch > max {
			max = ch
		}
	}
	return max
}

// ContextProfile is the context-loading strategy, adapting to the total chapter count.
type ContextProfile struct {
	SummaryWindow  int  // Load summaries of the last N chapters
	TimelineWindow int  // Load the timeline of the last N chapters
	Layered        bool // true = enable layered summary loading (volume + arc + chapter summaries)
}

// MemoryPolicy is the memory-usage strategy shared at runtime.
// It serves both context output and the host layer's handoff / reminder decisions.
type MemoryPolicy struct {
	Mode                string `json:"mode,omitempty"`
	SummaryWindow       int    `json:"summary_window,omitempty"`
	TimelineWindow      int    `json:"timeline_window,omitempty"`
	LayeredSummaries    bool   `json:"layered_summaries,omitempty"`
	SummaryStrategy     string `json:"summary_strategy,omitempty"`
	WorkingRefresh      string `json:"working_refresh,omitempty"`
	EpisodicRefresh     string `json:"episodic_refresh,omitempty"`
	PlanningRefresh     string `json:"planning_refresh,omitempty"`
	FoundationRefresh   string `json:"foundation_refresh,omitempty"`
	PlanningFocus       string `json:"planning_focus,omitempty"`
	FoundationFocus     string `json:"foundation_focus,omitempty"`
	PreviousTailChars   int    `json:"previous_tail_chars,omitempty"`
	ChapterPlanEnabled  bool   `json:"chapter_plan_enabled,omitempty"`
	RelatedLookup       bool   `json:"related_chapter_lookup,omitempty"`
	CurrentOutlineBound bool   `json:"current_outline_bound,omitempty"`
	HandoffPreferred    bool   `json:"handoff_preferred,omitempty"`
	ReadOnlyThreshold   int    `json:"read_only_threshold,omitempty"`
}

// NewContextProfile derives the context strategy from the total chapter count.
func NewContextProfile(totalChapters int) ContextProfile {
	switch {
	case totalChapters <= 15:
		return ContextProfile{SummaryWindow: 10, TimelineWindow: 10}
	case totalChapters <= 50:
		return ContextProfile{SummaryWindow: 5, TimelineWindow: 8}
	default:
		return ContextProfile{SummaryWindow: 3, TimelineWindow: 5, Layered: true}
	}
}

// NewChapterMemoryPolicy builds the chapter runtime memory policy from progress and the context strategy.
func NewChapterMemoryPolicy(progress *Progress, profile ContextProfile, currentOutlineBound bool) MemoryPolicy {
	policy := MemoryPolicy{
		Mode:                "chapter",
		SummaryWindow:       profile.SummaryWindow,
		TimelineWindow:      profile.TimelineWindow,
		LayeredSummaries:    profile.Layered,
		WorkingRefresh:      "Làm mới mỗi lần nạp theo chương",
		EpisodicRefresh:     "Làm mới khi commit chương, thẩm duyệt và thay đổi trạng thái truyện dài",
		PreviousTailChars:   800,
		ChapterPlanEnabled:  true,
		CurrentOutlineBound: currentOutlineBound,
		ReadOnlyThreshold:   5,
	}
	if profile.Layered {
		policy.SummaryStrategy = "tóm tắt tập + tóm tắt cung + tóm tắt chương gần đây"
	} else {
		policy.SummaryStrategy = "tóm tắt chương gần đây"
	}
	if progress != nil {
		if progress.TotalChapters > 30 {
			policy.RelatedLookup = true
		}
		if progress.Flow == FlowReviewing || progress.Flow == FlowRewriting || progress.Flow == FlowPolishing {
			policy.HandoffPreferred = true
		}
		if progress.Layered && len(progress.CompletedChapters) >= 6 {
			policy.HandoffPreferred = true
		}
		if len(progress.CompletedChapters) >= 12 {
			policy.HandoffPreferred = true
		}
		if progress.Layered && len(progress.CompletedChapters) >= 6 {
			policy.ReadOnlyThreshold = 4
		}
		if len(progress.CompletedChapters) >= 12 {
			policy.ReadOnlyThreshold = 4
		}
	}
	return policy
}

// NewArchitectMemoryPolicy returns the memory policy used during the planning stage.
func NewArchitectMemoryPolicy() MemoryPolicy {
	return MemoryPolicy{
		Mode:               "architect",
		PlanningRefresh:    "Làm mới khi cấu trúc tập-cung, la bàn hoặc tóm tắt thay đổi",
		FoundationRefresh:  "Làm mới khi nhân vật, phục bút, thiết lập thay đổi",
		PlanningFocus:      "đại cương phân tầng, la bàn, tóm tắt tập",
		FoundationFocus:    "thiết lập nhân vật, ảnh chụp nhân vật, sổ phục bút",
		HandoffPreferred:   true,
		ChapterPlanEnabled: false,
		ReadOnlyThreshold:  4,
	}
}

// RunMeta holds run metadata, persisted to meta/run.json.
type RunMeta struct {
	StartedAt            string             `json:"started_at"`
	Provider             string             `json:"provider,omitempty"`
	Style                string             `json:"style"`
	Model                string             `json:"model"`
	PlanningTier         PlanningTier       `json:"planning_tier,omitempty"`
	StartPrompt          string             `json:"start_prompt,omitempty"`           // Original user requirement (an input fact persisted before the plan-start decision; used to re-decide if that fails)
	PlanStart            *PlanStartRecord   `json:"plan_start,omitempty"`             // Plan-start decision fact; the sole basis for recovering from a planning-phase crash
	PendingSteer         string             `json:"pending_steer,omitempty"`          // Unfinished Steer instruction, re-injected on interrupt recovery
	AdvanceMode          ChapterAdvanceMode `json:"advance_mode"`                     // Chapter advance mode: auto / review
	AdvancePermitChapter int                `json:"advance_permit_chapter,omitempty"` // One-shot permitted forward chapter in review mode
	AdvanceHold          *AdvanceHold       `json:"advance_hold,omitempty"`           // One-shot pause intent signed by the current intervention
}

// ChapterAdvanceMode decides whether a new chapter needs a per-chapter permit.
type ChapterAdvanceMode string

const (
	ChapterAdvanceAuto   ChapterAdvanceMode = "auto"
	ChapterAdvanceReview ChapterAdvanceMode = "review"
)

// Valid reports whether the chapter advance mode is supported by this build.
func (m ChapterAdvanceMode) Valid() bool {
	return m == ChapterAdvanceAuto || m == ChapterAdvanceReview
}

// UnsupportedAdvanceModeError means the book's control mode is not supported by this binary.
// Callers must stop building a writable Host and tell the user to use a matching version;
// guessing at a degraded mode is forbidden.
type UnsupportedAdvanceModeError struct {
	Mode ChapterAdvanceMode
}

func (e *UnsupportedAdvanceModeError) Error() string {
	return fmt.Sprintf("chế độ đẩy chương không được hỗ trợ %q, hãy dùng bản ainovel mới hơn đã tạo dự án này", e.Mode)
}

// AdvanceHoldAfter is the deterministic trigger condition for a one-shot pause.
type AdvanceHoldAfter string

const (
	AdvanceHoldAtBoundary           AdvanceHoldAfter = "boundary"
	AdvanceHoldAfterRewritesDrained AdvanceHoldAfter = "rewrites_drained"
	AdvanceHoldAtChapter            AdvanceHoldAfter = "chapter"
)

// Valid reports whether the pause condition is supported by this build.
func (a AdvanceHoldAfter) Valid() bool {
	return a == AdvanceHoldAtBoundary || a == AdvanceHoldAfterRewritesDrained || a == AdvanceHoldAtChapter
}

// AdvanceHold is the one-shot pause intent signed by the current intervention, consumed at a Host boundary.
type AdvanceHold struct {
	After         AdvanceHoldAfter `json:"after"`
	TargetChapter int              `json:"target_chapter,omitempty"`
	Reason        string           `json:"reason"`
}

// Validate checks the structural constraints of a one-shot pause intent itself.
func (h AdvanceHold) Validate() error {
	if !h.After.Valid() {
		return fmt.Errorf("điều kiện tạm dừng một lần không được hỗ trợ %q", h.After)
	}
	if h.After == AdvanceHoldAtChapter {
		if h.TargetChapter <= 0 {
			return fmt.Errorf("chương mục tiêu phải lớn hơn 0")
		}
	} else if h.TargetChapter != 0 {
		return fmt.Errorf("điều kiện tạm dừng %q không được đặt chương mục tiêu", h.After)
	}
	if strings.TrimSpace(h.Reason) == "" {
		return fmt.Errorf("lý do tạm dừng một lần không được để trống")
	}
	return nil
}

// PlanStartRecord is the persisted fact of the plan-start decision (the fact lands first,
// execution follows; a resume never re-decides).
// Once the first save_foundation persists scale, planning-phase recovery derives from
// PlanningTier instead, so this record only covers the window "from decision to first write".
// DecisionID links to the decisions.jsonl audit.
type PlanStartRecord struct {
	RawPrompt   string `json:"raw_prompt"`
	Planner     string `json:"planner"`
	PlannerTask string `json:"planner_task"`
	DecisionID  string `json:"decision_id,omitempty"`
}
