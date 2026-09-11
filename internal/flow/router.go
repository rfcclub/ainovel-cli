// Package flow implements the domain routing: the Host decides from facts which subagent to call
// next and for what.
//
// Design principles:
//   - Route is a pure function: State in, *Instruction out. No IO, no Store calls, unit-testable.
//   - State is built from the Store by LoadState (impure), reading every fact routing needs in one
//     pass.
//   - Returning nil is legal: it means no Worker instruction can be derived from deterministic
//     facts, and the Engine handles it via the terminal state, a start-up re-decision, or waiting
//     for user intervention.
//
// The Router covers "table-lookup" decisions (each chapter's next step, end-of-arc post-processing,
// queue-driven work) and not "semantic understanding" ones (choosing a planner, handling a user
// Steer, emitting a summary).
package flow

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// plannerForTier derives the planner identity from the persisted planning tier: short goes to the
// short-form planner, mid/long to the long-form planner (matching the start-up Arbiter's choice).
func plannerForTier(tier domain.PlanningTier) string {
	if tier == domain.PlanningTierShort {
		return "architect_short"
	}
	return "architect_long"
}

// Instruction tells the Engine which Worker to run next and with what task.
type Instruction struct {
	Agent   string // architect_long / architect_short / writer / editor
	Task    string // Task description handed to the subagent
	Reason  string // Routing rationale (used for events, logs and failure decisions)
	Chapter int    // Chapter involved in a writer task (continue/rewrite/polish); 0 means none (editor/architect tasks)
}

type AggregateKind string

const (
	AggregateArcReview     AggregateKind = "arc_review"
	AggregateArcSummary    AggregateKind = "arc_summary"
	AggregateVolumeSummary AggregateKind = "volume_summary"
	AggregateGlobalReview  AggregateKind = "global_review"
)

type AggregateRefresh struct {
	Kind         AggregateKind
	Volume       int
	Arc          int
	StartChapter int
	EndChapter   int
}

// State is Route's input: every fact must be declared explicitly here, and Route must never read the
// Store itself.
type State struct {
	Progress *domain.Progress

	// Highest chapter number among the completed chapters; 0 means writing has not started.
	LastCompleted int

	// Arc-boundary information for the last chapter; the other fields are meaningless when IsArcEnd is false.
	// Should be nil when LastCompleted=0 or the mode is not Layered.
	ArcBoundary *storepkg.ArcBoundary

	// The three end-of-arc post-processing facts: whether review / arc summary / volume summary are done.
	HasArcReview     bool
	HasArcSummary    bool
	HasVolumeSummary bool

	// Missing foundation settings (the planning-stage backfill signal).
	FoundationMissing []string

	// The persisted planning tier (written into RunMeta when save_foundation lands scale).
	// Empty = first-time planning has produced no settings yet, so the planner identity cannot be decided.
	PlanningTier domain.PlanningTier

	// Non-layered books: whether the latest completed chapter already has a scope=global review
	// (only meaningful at a ShouldReview trigger point; always false for layered books).
	HasGlobalReview bool

	// External revision impact that the Architect must handle before continuing.
	// Ordinary Writer feedback waits for the next natural structural operation; the
	// planner is not dispatched extra for every chapter.
	ImmediateFeedbackCount int

	// The earliest arc/volume artefact the Editor must regenerate after external revision.
	AggregateRefresh *AggregateRefresh

	// Whether the head of the rework queue still lacks a rewrite directive
	// (chapter_contract). Telling the writer only "rewrite chapter N" gives no
	// direction: in practice the Editor rated architect_directive_unclear as the most
	// severe problem in the queue — a pile of symptoms with no statement of the target.
	RewriteHeadNeedsDirective bool
}

// Route returns the next deterministic instruction from the facts; a nil return is handled by the
// Engine according to its calling context.
//
// Decision priority (mutually exclusive; the first match from the top wins):
//  1. Phase=Complete        -> nil (the Host deterministically emits the summary).
//  2. Planning-stage missing settings and the planner can decide -> the same planner fills them; otherwise nil (Engine re-decides at startup).
//  3. PendingRewrites non-empty -> the writer rewrites/polishes in queue order.
//  4. Flow=Reviewing        -> nil (dormant: no writer today; during review Flow is really writing)
//  5. Flow=Steering         -> nil (a user intervention is being handled)
//  6. External revision invalidated an aggregate artefact -> editor rebuilds it
//  7. External revision affects later planning -> the architect handles it
//  8. A layered book reached an arc end -> review, summary, arc expansion or volume continuation
//  9. A non-layered global review came due -> editor (global review)
//
// 10. A non-layered outline ran out -> architect (decide between completing and continuing the outline)
// 11. Anything else          -> writer (write next_chapter)
func Route(s State) *Instruction {
	p := s.Progress
	if p == nil {
		return nil
	}

	// 1. Terminal state: the Host generates a deterministic summary from store facts.
	if p.Phase == domain.PhaseComplete {
		return nil
	}

	// 2. Planning-stage backfill: a table-lookup decision — what is missing lives in the store, and the
	//    planner identity derives from the persisted scale (short -> architect_short, otherwise
	//    -> architect_long). An empty tier means first-time planning has landed no settings (the choice
	//    is a semantic judgement), so the Engine's planStartFallback re-decides.
	if p.Phase != domain.PhaseWriting {
		if len(s.FoundationMissing) > 0 && s.PlanningTier != "" {
			task := fmt.Sprintf("Bổ sung các mục thiếu của thiết lập nền tảng và thông tin tác phẩm: %s; book thì dùng save_book, các thiết lập nền tảng còn lại ghi xuống đĩa bằng save_foundation", strings.Join(s.FoundationMissing, ", "))
			if len(s.FoundationMissing) == 1 && s.FoundationMissing[0] == "foundation_audit" {
				task = "Thiết lập nền tảng đã đầy đủ: gọi lại novel_context để đọc toàn bộ sản phẩm đã ghi xuống đĩa cùng foundation_status.fingerprint, thẩm tra tính nhất quán ngữ nghĩa xuyên file rồi gọi audit_foundation; nếu có vấn đề thì sửa trước rồi thẩm tra lại"
			}
			return &Instruction{
				Agent:  plannerForTier(s.PlanningTier),
				Task:   task,
				Reason: "Thiết lập nền tảng còn thiếu mục, giao tiếp cho cùng kiến trúc sư theo các mục thiếu",
			}
		}
		return nil
	}

	// 3. The rewrite/polish queue comes first (the facts already landed at the tool layer; the Router
	//    just dispatches as listed).
	if len(p.PendingRewrites) > 0 {
		ch := p.PendingRewrites[0]
		verb := "viết lại"
		if p.Flow == domain.FlowPolishing {
			verb = "gọt giũa"
		}
		task := fmt.Sprintf("%s chương %d", verb, ch)
		if s.RewriteHeadNeedsDirective {
			// Saying only "rewrite chapter N" gives no direction: in practice the Editor
			// rated architect_directive_unclear as the most severe item in the rework queue.
			// We do not dispatch an extra architect — an extra dispatch would leave the queue
			// permanently undrainable when planning fails — instead the Writer is required to
			// turn the direction into a contract before starting to write.
			task += fmt.Sprintf(
				". Chương này chưa có chapter_contract: hãy gọi plan_chapter(chapter=%d) để viết rõ mục tiêu viết lại trước "+
					"(goal phải trả lời \"sửa thành cái gì\", chứ không phải thuật lại \"chỗ nào sai\"; "+
					"khi cần thì bổ sung payoff_points / continuity_checks / hook_goal), rồi dựa vào đó viết lại chính văn", ch)
		}
		return &Instruction{
			Agent:   "writer",
			Task:    task,
			Reason:  fmt.Sprintf("Hàng đợi PendingRewrites còn %d chương", len(p.PendingRewrites)),
			Chapter: ch,
		}
	}

	// 4. Under review -> hand back to the LLM. This is a dormant branch today: save_review only sets
	//    Flow to writing/rewriting/polishing and no production path sets reviewing (during review Flow
	//    is really writing, and "review before continuing" is guaranteed by agentcore steering
	//    priority rather than this branch). It is kept for symmetry with Steering, and so that routing
	//    yields to the LLM should a future editor review explicitly set reviewing.
	if p.Flow == domain.FlowReviewing {
		return nil
	}

	// 5. A user intervention is in flight: the Arbiter is deciding and the Engine does not preempt it.
	if p.Flow == domain.FlowSteering {
		return nil
	}
	if refresh := s.AggregateRefresh; refresh != nil {
		switch refresh.Kind {
		case AggregateArcReview:
			return &Instruction{
				Agent: "editor",
				Task: fmt.Sprintf(
					"Thẩm duyệt tập %d cung %d (chương %d-%d): gọi novel_context(chapter=%d), save_review dùng scope=arc, chapter=%d",
					refresh.Volume, refresh.Arc, refresh.StartChapter, refresh.EndChapter, refresh.EndChapter, refresh.EndChapter,
				),
				Reason: "Thiếu thẩm duyệt cấp cung",
			}
		case AggregateArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Sinh tóm tắt tập %d cung %d, ảnh chụp nhân vật và quy tắc viết (save_arc_summary)", refresh.Volume, refresh.Arc),
				Reason: "Thiếu tóm tắt cấp cung",
			}
		case AggregateVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Sinh tóm tắt tập %d (save_volume_summary)", refresh.Volume),
				Reason: "Thiếu tóm tắt tập",
			}
		case AggregateGlobalReview:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Thẩm duyệt %d chương đầu: gọi novel_context(chapter=%d), save_review dùng scope=global, chapter=%d", refresh.EndChapter, refresh.EndChapter, refresh.EndChapter),
				Reason: "Thiếu thẩm duyệt toàn cục",
			}
		}
	}

	if s.ImmediateFeedbackCount > 0 {
		return &Instruction{
			Agent:  plannerForTier(s.PlanningTier),
			Task:   "Chỉ xử lý writer_feedback từ tu chỉnh bên ngoài trong novel_context: đối chiếu cốt truyện đã diễn ra với kế hoạch phía sau, khi cần điều chỉnh thì gọi revise_outline hoặc công cụ cấu trúc tương ứng, khi không cần thì gọi resolve_outline_feedback; không được xử lý foundation_status hay quy hoạch khác, ghi xuống đĩa rồi kết thúc bằng một câu",
			Reason: fmt.Sprintf("Còn %d ảnh hưởng từ tu chỉnh bên ngoài chưa lan tới quy hoạch phía sau", s.ImmediateFeedbackCount),
		}
	}

	// 8. End-of-arc post-processing in layered mode.
	if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
		b := s.ArcBoundary
		switch {
		case !s.HasArcReview:
			return &Instruction{
				Agent: "editor",
				Task: fmt.Sprintf(
					"Thẩm duyệt cấp cung cho tập %d cung %d (chương %d-%d): gọi novel_context(chapter=%d), save_review dùng scope=arc, chapter=%d; issues[].chapters chỉ được nằm trong khoảng đó",
					b.Volume, b.Arc, b.StartChapter, b.EndChapter, b.EndChapter, b.EndChapter,
				),
				Reason: "Thẩm duyệt cuối cung chưa hoàn tất",
			}
		case !s.HasArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Sinh tóm tắt tập %d cung %d, ảnh chụp nhân vật và quy tắc viết (save_arc_summary)", b.Volume, b.Arc),
				Reason: "Tóm tắt cung chưa hoàn tất",
			}
		case b.IsVolumeEnd && !s.HasVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Sinh tóm tắt tập %d (save_volume_summary)", b.Volume),
				Reason: "Tóm tắt tập chưa hoàn tất",
			}
		case b.NeedsExpansion && b.NextArc > 0:
			return &Instruction{
				Agent:  "architect_long",
				Task:   fmt.Sprintf("Mở rộng tập %d cung %d (save_foundation type=expand_arc)", b.NextVolume, b.NextArc),
				Reason: "Cung khung xương kế tiếp đang chờ mở rộng",
			}
		case b.NeedsNewVolume:
			return &Instruction{
				Agent:  "architect_long",
				Task:   "Tạo tập kế tiếp: đánh giá theo danh sách tiêu chí kết thúc rồi gọi save_foundation — truyện tiếp diễn → type=append_volume; truyện gần tới đích → type=append_volume với tầng trên cùng của JSON tập mang \"final\": true (tập kết thúc, thu dây cả tập, viết xong tự kết sách); mọi điều kiện kết thúc đã thoả ngay lúc này → type=complete_book. Cả ba lựa chọn đều phải kèm tham số reason ghi rõ lý do phán định",
				Reason: "Cuối tập cần quyết định thêm tập mới, tập kết thúc hay kết thúc toàn sách",
			}
		}
	}

	// 11. Non-layered global review: once every ReviewInterval chapters (the fact being that the
	//     chapter's global review has not landed). It was originally the review_required signal in
	//     commit_chapter's return value, now derived from facts — the return value merely mirrors the
	//     fact, and Route reads the same fact straight from the store.
	if !p.Layered && s.LastCompleted > 0 {
		if due, reason := domain.ShouldReview(len(p.CompletedChapters)); due && !s.HasGlobalReview {
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Thẩm duyệt toàn cục %d chương đầu (save_review scope=global, chapter=%d)", s.LastCompleted, s.LastCompleted),
				Reason: reason,
			}
		}
	}

	// 12. When a non-layered outline runs out, no out-of-range chapter may be dispatched. Let the
	//     Architect decide to complete based on current story facts, or continue the plan from the next
	//     chapter with revise_outline.
	next := p.NextChapter()
	if next <= 0 {
		return nil
	}
	if !p.Layered && p.TotalChapters > 0 && next > p.TotalChapters {
		return &Instruction{
			Agent: plannerForTier(s.PlanningTier),
			Task: fmt.Sprintf(
				"Đại cương không phân tầng đã viết hết (đã hoàn thành %d chương, tổng %d chương): nếu truyện đã khép lại, gọi save_foundation(type=complete_book); nếu vẫn cần tiếp, dùng revise_outline để nối kế hoạch phía sau từ chương %d",
				len(p.CompletedChapters), p.TotalChapters, next,
			),
			Reason: "Đại cương không phân tầng đã cạn, cần quyết định kết thúc hay nối tiếp",
		}
	}

	// 13. Normal continuation.
	return &Instruction{
		Agent:   "writer",
		Task:    fmt.Sprintf("Viết chương %d", next),
		Reason:  "Viết tiếp chương kế tiếp",
		Chapter: next,
	}
}
