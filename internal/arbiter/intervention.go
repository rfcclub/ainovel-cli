package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// InterventionFacts is the fact pack for intervention triage (a snapshot at Collect time).
// Before executing a Dispatch at a boundary the Engine reconciles with Phase/QueueHead (a worker
// run sits between consultation and execution, so the facts may have advanced; on a mismatch it
// discards the decision and re-consults with fresh facts).
type InterventionFacts struct {
	Phase                    string           `json:"phase,omitempty"`
	Flow                     string           `json:"flow,omitempty"`
	Title                    string           `json:"title,omitempty"`
	CompletedChapters        int              `json:"completed_chapters"`
	OutlinedChapters         int              `json:"outlined_chapters,omitempty"`
	DynamicPlanning          bool             `json:"dynamic_planning"`
	NextChapter              int              `json:"next_chapter,omitempty"`
	PendingRewrites          []int            `json:"pending_rewrites,omitempty"`
	ReopenCount              int              `json:"reopen_count,omitempty"` // Cumulative count of explicit /reopen calls on a finished book
	FoundationMissing        []string         `json:"foundation_missing,omitempty"`
	PlanningTier             string           `json:"planning_tier,omitempty"`
	AdvanceMode              string           `json:"advance_mode,omitempty"`
	HasAdvanceHold           bool             `json:"has_advance_hold"`
	AdvanceHoldAfter         string           `json:"advance_hold_after,omitempty"`
	AdvanceHoldTargetChapter int              `json:"advance_hold_target_chapter,omitempty"`
	AdvanceHoldReason        string           `json:"advance_hold_reason,omitempty"`
	Running                  bool             `json:"running"`                  // Whether a run was active when the intervention arrived
	CheckpointSeq            int64            `json:"checkpoint_seq,omitempty"` // Latest checkpoint at Collect time; used by the Engine for reconciliation
	RecentDecisions          []RecentDecision `json:"recent_decisions,omitempty"`
}

// RecentDecision is intervention memory: summaries of the most recent decisions, covering
// cross-intervention references such as "how did last time's change turn out".
type RecentDecision struct {
	At     string `json:"at"`
	Input  string `json:"input"`
	Reason string `json:"reason,omitempty"`
}

// QueueHead returns the head of the rewrite queue (0 when empty), used by the Engine for reconciliation.
func (f InterventionFacts) QueueHead() int {
	if len(f.PendingRewrites) > 0 {
		return f.PendingRewrites[0]
	}
	return 0
}

// CollectInterventionFacts reads every triage fact from the store. Any failure to read a control
// fact returns an explicit error; the Arbiter must never make a semantic decision on an
// incomplete snapshot patched together from zero values.
func CollectInterventionFacts(st *storepkg.Store) (InterventionFacts, error) {
	var f InterventionFacts
	if st == nil {
		return f, fmt.Errorf("store không được để trống")
	}
	missing, err := st.FoundationMissing()
	if err != nil {
		return f, fmt.Errorf("đọc trạng thái thiết lập nền tảng: %w", err)
	}
	f.FoundationMissing = missing
	book, err := st.Book.Load()
	if err != nil {
		return f, fmt.Errorf("đọc thông tin tác phẩm: %w", err)
	}
	if book != nil {
		f.Title = book.Title
	}
	p, err := st.Progress.Load()
	if err != nil {
		return f, fmt.Errorf("đọc tiến độ: %w", err)
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.Flow = string(p.Flow)
		f.CompletedChapters = len(p.CompletedChapters)
		f.DynamicPlanning = p.Layered
		if p.Layered {
			outline, outlineErr := st.Outline.LoadOutline()
			if outlineErr != nil {
				return f, fmt.Errorf("đọc đại cương chi tiết hiện tại: %w", outlineErr)
			}
			f.OutlinedChapters = len(outline)
		} else {
			f.OutlinedChapters = p.TotalChapters
		}
		f.NextChapter = p.NextChapter()
		f.PendingRewrites = append([]int(nil), p.PendingRewrites...)
		f.ReopenCount = p.ReopenCount
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		return f, fmt.Errorf("đọc metadata run: %w", err)
	}
	if meta != nil {
		f.PlanningTier = string(meta.PlanningTier)
		f.AdvanceMode = string(meta.AdvanceMode)
		if meta.AdvanceHold != nil {
			f.HasAdvanceHold = true
			f.AdvanceHoldAfter = string(meta.AdvanceHold.After)
			f.AdvanceHoldTargetChapter = meta.AdvanceHold.TargetChapter
			f.AdvanceHoldReason = meta.AdvanceHold.Reason
		}
	}
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		f.CheckpointSeq = cp.Seq
	}
	recent, err := st.Decisions.Recent(5)
	if err != nil {
		return f, fmt.Errorf("đọc các phán định gần đây: %w", err)
	}
	for _, r := range recent {
		if r.Kind != "intervention" {
			continue
		}
		f.RecentDecisions = append(f.RecentDecisions, RecentDecision{
			At: r.At, Input: truncateRunes(r.Input, 80), Reason: r.Reason,
		})
	}
	return f, nil
}

// AdvanceHoldOp is the one-shot pause action: pause at a work boundary, once rework drains, or
// after a target chapter completes; it can also be cancelled.
type AdvanceHoldOp struct {
	Cancel        bool                    `json:"cancel,omitempty"`
	After         domain.AdvanceHoldAfter `json:"after,omitempty"`
	TargetChapter int                     `json:"target_chapter,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
}

// ReopenOp is completion rework: reopen the whole book into the rework state and queue the target
// chapters (legal only when phase=complete).
type ReopenOp struct {
	Chapters []int  `json:"chapters"`
	Reason   string `json:"reason,omitempty"`
}

// InterventionDecision is an intervention decision. Actions combine freely, while the Engine fixes
// the execution order: answer -> rules -> hold -> reopen -> dispatch, with at most one dispatch
// (a type-level fact).
type InterventionDecision struct {
	Answer   string         `json:"answer,omitempty"`
	Rules    string         `json:"rules,omitempty"`
	Hold     *AdvanceHoldOp `json:"hold,omitempty"`
	Reopen   *ReopenOp      `json:"reopen,omitempty"`
	Dispatch *DispatchOp    `json:"dispatch,omitempty"`
	Reason   string         `json:"reason"`
}

var interventionContract = llmcontract.Contract{
	Name:        "arbiter_intervention",
	Description: "Phán định can thiệp người dùng: trả lời, quy tắc, tạm dừng, mở lại và giao việc",
	Schema: schema.Object(
		schema.Property("answer", llmcontract.Nullable(schema.String("Văn bản hiển thị lại cho người dùng; không có thì null"))).Required(),
		schema.Property("rules", llmcontract.Nullable(schema.String("Nguyên văn quy tắc viết dài hạn cần ghi xuống đĩa; không có thì null"))).Required(),
		schema.Property("hold", llmcontract.Nullable(schema.Object(
			schema.Property("cancel", schema.Bool("Có hủy ý định tạm dừng một lần sẵn có hay không")).Required(),
			schema.Property("after", llmcontract.Nullable(schema.Enum("Điểm kích hoạt tạm dừng; null khi hủy", string(domain.AdvanceHoldAtBoundary), string(domain.AdvanceHoldAfterRewritesDrained), string(domain.AdvanceHoldAtChapter)))).Required(),
			schema.Property("target_chapter", llmcontract.Nullable(schema.Int("Chương mục tiêu khi after=chapter; các trường hợp khác thì null"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("Tóm tắt nguyện vọng người dùng; có thể null khi hủy"))).Required(),
		))).Required(),
		schema.Property("reopen", llmcontract.Nullable(schema.Object(
			schema.Property("chapters", schema.Array("Các số chương cần mở lại", schema.Int("Số chương"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("Lý do mở lại"))).Required(),
		))).Required(),
		schema.Property("dispatch", dispatchSchema("Đích giao việc; null khi không cần giao việc")).Required(),
		schema.Property("reason", schema.String("Lý do phán định trong một câu")).Required(),
	),
}

// ValidateAgainst performs the mechanical fact check (in-scenario legality; cross-scenario actions
// are already excluded by the type).
func (d *InterventionDecision) ValidateAgainst(f InterventionFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason không được để trống")
	}
	if d.Answer == "" && d.Rules == "" && d.Hold == nil && d.Reopen == nil && d.Dispatch == nil {
		return fmt.Errorf("phán định rỗng: phải có ít nhất một hành động hoặc answer")
	}
	if err := d.Dispatch.validate(); err != nil {
		return err
	}
	if err := validateDispatchAgainst(d.Dispatch, f.Phase); err != nil {
		return err
	}
	complete := f.Phase == string(domain.PhaseComplete)
	if d.Reopen != nil {
		if !complete {
			return fmt.Errorf("reopen chỉ áp dụng ở giai đoạn đã kết thúc (hiện tại phase=%s)", f.Phase)
		}
		if len(d.Reopen.Chapters) == 0 {
			return fmt.Errorf("reopen.chapters không được để trống")
		}
		for _, ch := range d.Reopen.Chapters {
			if ch < 1 || ch > f.CompletedChapters {
				return fmt.Errorf("chương reopen %d vượt giới hạn (đã hoàn thành %d chương)", ch, f.CompletedChapters)
			}
		}
	}
	if complete && d.Dispatch != nil {
		return fmt.Errorf("ở giai đoạn đã kết thúc không được giao việc trực tiếp; muốn làm lại thì dùng reopen (sau khi vào hàng đợi, Router sẽ tự giao việc)")
	}
	if d.Hold != nil && !d.Hold.Cancel {
		if f.Phase != string(domain.PhaseWriting) {
			return fmt.Errorf("tạm dừng một lần chỉ áp dụng ở giai đoạn viết (hiện tại phase=%s)", f.Phase)
		}
		hold := domain.AdvanceHold{After: d.Hold.After, TargetChapter: d.Hold.TargetChapter, Reason: d.Hold.Reason}
		if err := hold.Validate(); err != nil {
			return fmt.Errorf("hold không hợp lệ: %w", err)
		}
		nextChapter := f.NextChapter
		if nextChapter == 0 {
			nextChapter = f.CompletedChapters + 1
		}
		if hold.After == domain.AdvanceHoldAtChapter && hold.TargetChapter < nextChapter {
			return fmt.Errorf("chương mục tiêu %d sớm hơn chương kế tiếp hiện tại %d", hold.TargetChapter, nextChapter)
		}
	}
	return nil
}

// validateDispatchAgainst turns the stage discipline from the prompt into a mechanical guard.
// The Architect may maintain structure during planning and writing; the Writer/Editor may only
// consume work facts that are complete and have entered writing.
func validateDispatchAgainst(dispatch *DispatchOp, phase string) error {
	if dispatch == nil {
		return nil
	}
	if phase == "" {
		return fmt.Errorf("thiếu phase, cấm thực hiện giao việc")
	}
	if phase == string(domain.PhaseComplete) {
		return fmt.Errorf("ở giai đoạn đã kết thúc không được giao việc trực tiếp")
	}
	switch dispatch.Agent {
	case "writer", "editor":
		if phase != string(domain.PhaseWriting) {
			return fmt.Errorf("%s chỉ được giao việc ở giai đoạn writing (hiện tại phase=%s)", dispatch.Agent, phase)
		}
	}
	return nil
}

// DecideIntervention is intervention triage. Failure semantics: returning an error makes the caller
// surface the real cause explicitly and produces no write at all (better to do nothing than to act
// wrongly).
func DecideIntervention(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts InterventionFacts, text string) (InterventionDecision, error) {
	payload, err := marshalPayload(struct {
		Intervention string            `json:"intervention"`
		Facts        InterventionFacts `json:"facts"`
	}{Intervention: text, Facts: facts})
	if err != nil {
		return InterventionDecision{}, err
	}
	return decide(ctx, model, interventionContract, systemPrompt, payload, func(d *InterventionDecision) error {
		return d.ValidateAgainst(facts)
	})
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
