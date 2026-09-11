package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestLoadStateReturnsProgressReadError(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(st); err == nil {
		t.Fatal("损坏的 progress 必须阻止路由")
	}
}

func TestLoadStateOnlyPrioritizesExternalRevisionFeedback(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	progress := &domain.Progress{
		Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 10, CompletedChapters: []int{1},
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := st.Outline.AppendOutlineFeedback(storepkg.ChapterFeedback{
		Chapter: 1, Deviation: "không lệch rõ rệt", Suggestion: "chương sau tiếp tục đẩy",
	}); err != nil {
		t.Fatalf("Append normal feedback: %v", err)
	}

	state, err := LoadState(st)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.ImmediateFeedbackCount != 0 {
		t.Fatalf("normal writer feedback should not interrupt writing: %+v", state)
	}
	if got := Route(state); got == nil || got.Agent != "writer" {
		t.Fatalf("normal writer feedback should continue writing, got %+v", got)
	}

	if err := st.Outline.AppendOutlineFeedback(storepkg.ChapterFeedback{
		Chapter: 1, StoryChanged: true, ChangeSummary: "người dùng viết lại kết cục chương này",
	}); err != nil {
		t.Fatalf("Append external revision feedback: %v", err)
	}
	state, err = LoadState(st)
	if err != nil {
		t.Fatalf("LoadState after external revision: %v", err)
	}
	if state.ImmediateFeedbackCount != 1 {
		t.Fatalf("external revision should interrupt writing: %+v", state)
	}
	if got := Route(state); got == nil || !strings.HasPrefix(got.Agent, "architect_") {
		t.Fatalf("external revision should dispatch architect, got %+v", got)
	}
}

// helper: builds a Progress in the Writing phase with layered mode enabled.
func writingProgress(completed []int, flow domain.FlowState) *domain.Progress {
	return &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              flow,
		Layered:           true,
		CompletedChapters: completed,
	}
}

func TestRoute_NilProgress(t *testing.T) {
	if got := Route(State{Progress: nil}); got != nil {
		t.Fatalf("expected nil for nil progress, got %+v", got)
	}
}

func TestRoute_PhaseComplete(t *testing.T) {
	s := State{Progress: &domain.Progress{Phase: domain.PhaseComplete}}
	if got := Route(s); got != nil {
		t.Fatalf("expected nil at PhaseComplete, got %+v", got)
	}
}

func TestRoute_NonWritingPhasesDelegateToLLM(t *testing.T) {
	for _, phase := range []domain.Phase{domain.PhaseInit, domain.PhasePremise, domain.PhaseOutline} {
		s := State{Progress: &domain.Progress{Phase: phase}, FoundationMissing: []string{"premise"}}
		if got := Route(s); got != nil {
			t.Fatalf("phase %s should return nil, got %+v", phase, got)
		}
	}
}

func TestRoute_PendingRewritesFirst(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowRewriting)
	p.PendingRewrites = []int{3, 5}
	got := Route(State{Progress: p})
	if got == nil || got.Agent != "writer" {
		t.Fatalf("expected writer for rewrites, got %+v", got)
	}
	if got.Task != "viết lại chương 3" {
		t.Errorf("expected 'viết lại chương 3', got %q", got.Task)
	}
	if got.Chapter != 3 {
		t.Errorf("expected Chapter=3, got %d", got.Chapter)
	}
}

func TestRoute_PendingPolishingVerb(t *testing.T) {
	p := writingProgress([]int{1}, domain.FlowPolishing)
	p.PendingRewrites = []int{2}
	got := Route(State{Progress: p})
	if got == nil || got.Task != "gọt giũa chương 2" {
		t.Fatalf("expected polish verb, got %+v", got)
	}
}

func TestRoute_ReviewingDelegatesToLLM(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowReviewing)
	if got := Route(State{Progress: p}); got != nil {
		t.Fatalf("expected nil during reviewing, got %+v", got)
	}
}

func TestRoute_SteeringDelegatesToLLM(t *testing.T) {
	p := writingProgress([]int{1}, domain.FlowSteering)
	if got := Route(State{Progress: p}); got != nil {
		t.Fatalf("expected nil during steering, got %+v", got)
	}
}

func TestRoute_ArcEndNeedsReview(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:     true,
			Volume:       1,
			Arc:          2,
			StartChapter: 11,
			EndChapter:   22,
		},
	}
	got := Route(s)
	if got == nil || got.Agent != "editor" {
		t.Fatalf("expected editor for arc review, got %+v", got)
	}
	if got.Reason != "Thẩm duyệt cuối cung chưa hoàn tất" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
	if !strings.Contains(got.Task, "chương 11-22") || !strings.Contains(got.Task, "chapter=22") {
		t.Fatalf("arc review task must carry exact span and endpoint: %q", got.Task)
	}
}

func TestRoute_ArcEndHasReviewNeedsSummary(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd: true,
			Volume:   1,
			Arc:      2,
		},
		HasArcReview: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "editor" || got.Reason != "Tóm tắt cung chưa hoàn tất" {
		t.Fatalf("expected arc summary editor call, got %+v", got)
	}
}

func TestRoute_VolumeEndNeedsVolumeSummary(t *testing.T) {
	p := writingProgress([]int{20}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 20,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:    true,
			IsVolumeEnd: true,
			Volume:      1,
			Arc:         3,
		},
		HasArcReview:  true,
		HasArcSummary: true,
	}
	got := Route(s)
	if got == nil || got.Reason != "Tóm tắt tập chưa hoàn tất" {
		t.Fatalf("expected volume summary request, got %+v", got)
	}
}

func TestRoute_NeedsArcExpansion(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			Volume:         1,
			Arc:            2,
			NextVolume:     1,
			NextArc:        3,
			NeedsExpansion: true,
		},
		HasArcReview:  true,
		HasArcSummary: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("expected architect_long for expansion, got %+v", got)
	}
	if got.Reason != "Cung khung xương kế tiếp đang chờ mở rộng" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
}

func TestRoute_NeedsNewVolume(t *testing.T) {
	p := writingProgress([]int{30}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 30,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			IsVolumeEnd:    true,
			Volume:         2,
			Arc:            4,
			NeedsNewVolume: true,
		},
		HasArcReview:     true,
		HasArcSummary:    true,
		HasVolumeSummary: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" || got.Reason != "Cuối tập cần quyết định thêm tập mới, tập kết thúc hay kết thúc toàn sách" {
		t.Fatalf("expected append_volume/complete_book dispatch, got %+v", got)
	}
}

func TestRoute_NormalContinue(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	p.TotalChapters = 20
	got := Route(State{Progress: p, LastCompleted: 3})
	if got == nil || got.Agent != "writer" {
		t.Fatalf("expected writer for next chapter, got %+v", got)
	}
	if got.Task != "Viết chương 4" {
		t.Errorf("expected 'Viết chương 4', got %q", got.Task)
	}
	if got.Chapter != 4 {
		t.Errorf("expected Chapter=4, got %d", got.Chapter)
	}
}

func TestRoute_ExternalRevisionDispatchesArchitectBeforeWriter(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	p.TotalChapters = 20
	got := Route(State{
		Progress: p, LastCompleted: 3, PlanningTier: domain.PlanningTierShort,
		ImmediateFeedbackCount: 2,
	})
	if got == nil || got.Agent != "architect_short" || !strings.Contains(got.Reason, "2 ảnh hưởng") {
		t.Fatalf("expected architect to consume feedback, got %+v", got)
	}
}

func TestRoute_AggregateRefreshPrecedesExternalRevision(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowWriting)
	got := Route(State{
		Progress: p, ImmediateFeedbackCount: 1,
		AggregateRefresh: &AggregateRefresh{
			Kind: AggregateArcSummary, Volume: 1, Arc: 1, StartChapter: 1, EndChapter: 2,
		},
	})
	if got == nil || got.Agent != "editor" || !strings.Contains(got.Task, "save_arc_summary") {
		t.Fatalf("expected editor aggregate refresh, got %+v", got)
	}
}

func TestRoute_NonLayeredOutlineExhaustedDispatchesArchitect(t *testing.T) {
	p := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CompletedChapters: []int{1, 2, 3},
		TotalChapters:     3,
	}
	got := Route(State{Progress: p, LastCompleted: 3, PlanningTier: domain.PlanningTierShort})
	if got == nil || got.Agent != "architect_short" {
		t.Fatalf("expected architect_short at outline exhaustion, got %+v", got)
	}
	for _, want := range []string{"complete_book", "revise_outline", "chương 4"} {
		if !strings.Contains(got.Task, want) {
			t.Errorf("task missing %q: %s", want, got.Task)
		}
	}
}

func TestRoute_ArcEndNonLayeredSkipsBoundary(t *testing.T) {
	// Non-Layered mode never takes the arc-end branch even when ArcBoundary is non-nil
	p := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           false,
		CompletedChapters: []int{10},
		TotalChapters:     20,
	}
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary:   &storepkg.ArcBoundary{IsArcEnd: true, Volume: 1, Arc: 2},
	}
	got := Route(s)
	if got == nil || got.Agent != "writer" {
		t.Fatalf("non-layered should fall through to writer, got %+v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Planning-stage catch-up: a missing foundation item with an identifiable planner → re-dispatch the same planner for the gap.
func TestRoute_PlanningFillDispatchesSamePlanner(t *testing.T) {
	base := State{
		Progress:          &domain.Progress{Phase: domain.PhaseOutline},
		FoundationMissing: []string{"characters", "world_rules"},
	}

	short := base
	short.PlanningTier = domain.PlanningTierShort
	if got := Route(short); got == nil || got.Agent != "architect_short" {
		t.Fatalf("short tier 应续派 architect_short,got %+v", got)
	}

	long := base
	long.PlanningTier = domain.PlanningTierLong
	got := Route(long)
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("long tier 应续派 architect_long,got %+v", got)
	}
	for _, want := range []string{"Bổ sung các mục thiếu của thiết lập nền tảng", "characters", "world_rules", "save_foundation"} {
		if !contains(got.Task, want) {
			t.Errorf("补齐任务缺少 %q: %s", want, got.Task)
		}
	}

	bookMissing := base
	bookMissing.PlanningTier = domain.PlanningTierLong
	bookMissing.FoundationMissing = []string{"book"}
	if got := Route(bookMissing); got == nil || !contains(got.Task, "save_book") {
		t.Fatalf("book 缺失时应指示 save_book,got %+v", got)
	}

	// The first planning round has landed no foundation at all (empty tier) → choosing the type is a semantic judgement, left to the LLM
	unknown := base
	if got := Route(unknown); got != nil {
		t.Fatalf("tier 未知时应交 LLM 裁定,got %+v", got)
	}

	// No items missing → no catch-up instruction (wait for the phase to advance)
	done := base
	done.PlanningTier = domain.PlanningTierLong
	done.FoundationMissing = nil
	if got := Route(done); got != nil {
		t.Fatalf("缺项已齐时不应派补齐,got %+v", got)
	}
}

// Chương trong hàng viết lại mà chưa có chỉ thị: chỉ thị phải yêu cầu writer
// lập kế hoạch trước. Không đổi agent — chèn thêm một lượt điều phối sẽ khiến
// hàng đợi không bao giờ rút nếu bước lập kế hoạch thất bại.
func TestRoute_RewriteWithoutDirectiveAsksWriterToPlanFirst(t *testing.T) {
	s := State{
		Progress: &domain.Progress{
			Phase:           domain.PhaseWriting,
			Flow:            domain.FlowRewriting,
			PendingRewrites: []int{13},
		},
		RewriteHeadNeedsDirective: true,
	}
	inst := Route(s)
	if inst == nil || inst.Agent != "writer" || inst.Chapter != 13 {
		t.Fatalf("phải vẫn điều writer cho chương 13, nhận %+v", inst)
	}
	if !strings.Contains(inst.Task, "plan_chapter") {
		t.Errorf("chỉ thị phải yêu cầu lập kế hoạch trước: %q", inst.Task)
	}
}

func TestRoute_RewriteWithDirectiveGoesStraightToWriting(t *testing.T) {
	s := State{
		Progress: &domain.Progress{
			Phase:           domain.PhaseWriting,
			Flow:            domain.FlowRewriting,
			PendingRewrites: []int{13},
		},
	}
	inst := Route(s)
	if inst == nil || inst.Agent != "writer" {
		t.Fatalf("phải điều writer, nhận %+v", inst)
	}
	if strings.Contains(inst.Task, "plan_chapter") {
		t.Errorf("đã có chỉ thị thì không nhắc lập kế hoạch nữa: %q", inst.Task)
	}
}
