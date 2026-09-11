package host

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ChapterAdvanceGate is the Host's sole creation-advance policy component:
//   - AdvanceHold: executes the one-shot pause this intervention signed;
//   - review permit: blocks an unlicensed forward new chapter.
//
// It takes no part in Route, interprets no Task/Reason, and makes no literary judgement.
type ChapterAdvanceGate struct {
	store  *store.Store
	pause  func(reason string)
	report func(level, summary string)
}

func NewChapterAdvanceGate(s *store.Store, pause func(reason string), report func(level, summary string)) *ChapterAdvanceGate {
	return &ChapterAdvanceGate{store: s, pause: pause, report: report}
}

// HandleBoundary consumes a hit hold and reconciles the chapter licence. A true result means the Engine must stop.
// In auto mode with no hold it reads RunMeta once and touches neither Progress/PendingCommit nor checkpoints.
func (g *ChapterAdvanceGate) HandleBoundary() bool {
	if g == nil || g.store == nil {
		return false
	}
	meta, err := g.store.RunMeta.Load()
	if err != nil {
		return g.fail(fmt.Errorf("đọc RunMeta: %w", err))
	}
	if meta == nil {
		return g.fail(fmt.Errorf("RunMeta chưa được khởi tạo"))
	}
	if !meta.AdvanceMode.Valid() {
		return g.fail(&domain.UnsupportedAdvanceModeError{Mode: meta.AdvanceMode})
	}
	if meta.AdvanceMode == domain.ChapterAdvanceAuto && meta.AdvancePermitChapter != 0 {
		return g.fail(fmt.Errorf("chế độ auto còn sót giấy phép chương %d", meta.AdvancePermitChapter))
	}

	if meta.AdvanceHold != nil {
		if g.handleHold(*meta.AdvanceHold) {
			return true
		}
		// handleHold may consume the book-completion hold; permit reconciliation continues.
	}
	if meta.AdvanceMode == domain.ChapterAdvanceAuto {
		return false
	}
	return g.reconcilePermit(meta.AdvancePermitChapter)
}

func (g *ChapterAdvanceGate) handleHold(hold domain.AdvanceHold) bool {
	progress, err := g.store.Progress.Load()
	if err != nil {
		return g.fail(fmt.Errorf("đọc Progress để phân tích tạm dừng một lần: %w", err))
	}
	resolution, err := flow.ResolveAdvanceHold(&hold, progress)
	if err != nil {
		return g.fail(err)
	}
	if hold.After == domain.AdvanceHoldAtChapter && progress.LatestCompleted() >= hold.TargetChapter {
		stable, err := g.targetChapterCommitted(progress, hold.TargetChapter)
		if err != nil {
			return g.fail(err)
		}
		if !stable {
			return false
		}
	}
	switch resolution {
	case flow.AdvanceHoldKeep:
		return false
	case flow.AdvanceHoldConsume:
		if err := g.store.RunMeta.ClearAdvanceHold(hold); err != nil {
			return g.fail(fmt.Errorf("tiêu thụ tạm dừng một lần: %w", err))
		}
		g.reportEvent("info", withAdvanceReason("Toàn sách đã kết thúc, ý định tạm dừng một lần đã được gỡ", hold.Reason))
		return false
	case flow.AdvanceHoldConsumeAndStop:
		if err := g.store.RunMeta.ClearAdvanceHold(hold); err != nil {
			return g.fail(fmt.Errorf("tiêu thụ tạm dừng một lần: %w", err))
		}
		msg := "Đã tạm dừng ở ranh giới công việc hiện tại theo yêu cầu người dùng"
		switch hold.After {
		case domain.AdvanceHoldAfterRewritesDrained:
			msg = "Hàng đợi làm lại đã cạn, đã tạm dừng chờ nghiệm thu"
		case domain.AdvanceHoldAtChapter:
			msg = fmt.Sprintf("Đã viết tới chương %d, tạm dừng theo yêu cầu người dùng", hold.TargetChapter)
		}
		g.pauseNow(withAdvanceReason(msg, hold.Reason))
		return true
	default:
		return g.fail(fmt.Errorf("kết quả phân tích tạm dừng một lần không xác định %d", resolution))
	}
}

func (g *ChapterAdvanceGate) targetChapterCommitted(progress *domain.Progress, chapter int) (bool, error) {
	pending, err := g.store.Signals.LoadPendingCommit()
	if err != nil {
		return false, fmt.Errorf("đọc PendingCommit để đối chiếu chương mục tiêu: %w", err)
	}
	if pending != nil {
		return false, nil
	}
	if !slices.Contains(progress.CompletedChapters, chapter) {
		return false, fmt.Errorf("chương mục tiêu %d không xuất hiện trong danh sách chương đã hoàn thành", chapter)
	}
	if g.store.Checkpoints.LatestByStep(domain.ChapterScope(chapter), "commit") == nil {
		return false, fmt.Errorf("chương mục tiêu %d đã đánh dấu hoàn thành nhưng thiếu checkpoint commit", chapter)
	}
	return true, nil
}

func (g *ChapterAdvanceGate) reconcilePermit(permit int) bool {
	if permit == 0 {
		return false
	}
	if permit < 0 {
		return g.fail(fmt.Errorf("giấy phép chương không được là số âm: %d", permit))
	}
	progress, err := g.store.Progress.Load()
	if err != nil {
		return g.fail(fmt.Errorf("đọc Progress để đối chiếu giấy phép chương: %w", err))
	}
	if progress == nil {
		return g.fail(fmt.Errorf("thiếu Progress, không thể đối chiếu giấy phép chương %d", permit))
	}
	pending, err := g.store.Signals.LoadPendingCommit()
	if err != nil {
		return g.fail(fmt.Errorf("đọc PendingCommit để đối chiếu giấy phép chương: %w", err))
	}
	completed := slices.Contains(progress.CompletedChapters, permit)
	if completed {
		if pending != nil {
			if pending.Chapter != permit {
				return g.fail(fmt.Errorf("giấy phép chương %d xung đột với PendingCommit của chương %d", permit, pending.Chapter))
			}
			return false
		}
		if g.store.Checkpoints.LatestByStep(domain.ChapterScope(permit), "commit") == nil {
			return g.fail(fmt.Errorf("chương %d đã đánh dấu hoàn thành nhưng thiếu checkpoint commit", permit))
		}
		if err := g.store.RunMeta.ClearAdvancePermit(permit); err != nil {
			return g.fail(fmt.Errorf("tiêu thụ giấy phép chương %d: %w", permit, err))
		}
		return false
	}
	if permit != progress.NextChapter() {
		return g.fail(fmt.Errorf("giấy phép chương %d không khớp chương kế tiếp hiện tại %d", permit, progress.NextChapter()))
	}
	return false
}

// Allow performs the final licence check before a Worker dispatch.
func (g *ChapterAdvanceGate) Allow(inst *flow.Instruction) (bool, error) {
	if g == nil || g.store == nil {
		return true, nil
	}
	meta, err := g.store.RunMeta.Load()
	if err != nil {
		return false, fmt.Errorf("đọc RunMeta: %w", err)
	}
	if meta == nil {
		return false, fmt.Errorf("RunMeta chưa được khởi tạo")
	}
	if !meta.AdvanceMode.Valid() {
		return false, &domain.UnsupportedAdvanceModeError{Mode: meta.AdvanceMode}
	}
	if meta.AdvanceMode == domain.ChapterAdvanceAuto {
		if meta.AdvancePermitChapter != 0 {
			return false, fmt.Errorf("chế độ auto còn sót giấy phép chương %d", meta.AdvancePermitChapter)
		}
		return true, nil
	}
	progress, err := g.store.Progress.Load()
	if err != nil {
		return false, fmt.Errorf("đọc Progress: %w", err)
	}
	pending, err := g.store.Signals.LoadPendingCommit()
	if err != nil {
		return false, fmt.Errorf("đọc PendingCommit: %w", err)
	}
	if !flow.StartsForwardChapter(inst, progress, pending) {
		return true, nil
	}
	target := inst.Chapter
	if target == 0 {
		target = progress.NextChapter()
	}
	if meta.AdvancePermitChapter == target {
		return true, nil
	}
	if meta.AdvancePermitChapter != 0 {
		return false, fmt.Errorf("việc giao chương %d không khớp giấy phép chương %d", target, meta.AdvancePermitChapter)
	}
	if hold := meta.AdvanceHold; hold != nil && hold.After == domain.AdvanceHoldAtChapter && target <= hold.TargetChapter {
		return true, nil
	}
	latest := progress.LatestCompleted()
	message := fmt.Sprintf("Đã hoàn thành tới chương %d, nghiệm thu từng chương đang chờ cho qua chương %d; dùng /next để sinh, hoặc nhập ý kiến sửa", latest, target)
	if latest == 0 {
		message = fmt.Sprintf("Quy hoạch đã sẵn sàng, nghiệm thu từng chương đang chờ cho qua chương %d; dùng /next để sinh, hoặc nhập ý kiến sửa", target)
	}
	g.pauseNow(message)
	return false, nil
}

func (g *ChapterAdvanceGate) fail(err error) bool {
	g.pauseNow("Lỗi điều khiển đẩy chương, đã tạm dừng: " + err.Error())
	return true
}

func (g *ChapterAdvanceGate) pauseNow(reason string) {
	if g.pause != nil {
		g.pause(reason)
		return
	}
	g.reportEvent("error", reason)
}

func (g *ChapterAdvanceGate) reportEvent(level, summary string) {
	if g.report != nil {
		g.report(level, summary)
	}
}

func withAdvanceReason(msg, reason string) string {
	if reason == "" {
		return msg
	}
	return msg + " (nguyện vọng: " + reason + ")"
}
