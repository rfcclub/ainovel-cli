package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// PlanChapterTool saves a chapter's conception, with the Agent deciding the planning granularity for itself.
type PlanChapterTool struct {
	store *store.Store
}

func NewPlanChapterTool(store *store.Store) *PlanChapterTool {
	return &PlanChapterTool{store: store}
}

func (t *PlanChapterTool) Name() string { return "plan_chapter" }
func (t *PlanChapterTool) Description() string {
	return "Lưu ý tưởng viết chương. Agent tự quyết độ chi tiết khi quy hoạch, không bắt buộc chia phân cảnh"
}
func (t *PlanChapterTool) Label() string { return "Quy hoạch chương" }

// A write tool; concurrency is forbidden.
func (t *PlanChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *PlanChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *PlanChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương")).Required(),
		schema.Property("title", schema.String("Tiêu đề chương tạm thời; có thể chỉnh theo chính văn sau khi viết")).Required(),
		schema.Property("goal", schema.String("Mục tiêu của chương này")).Required(),
		schema.Property("conflict", schema.String("Xung đột cốt lõi")).Required(),
		schema.Property("hook", schema.String("Móc câu cuối chương")).Required(),
		schema.Property("emotion_arc", schema.String("Cung cảm xúc")),
		schema.Property("notes", schema.String("Ghi chú tự do (bất cứ điều gì bạn thấy cần nhớ khi viết)")),
		schema.Property("required_beats", schema.Array("Các bước tiến bắt buộc phải hoàn thành trong chương này", schema.String(""))),
		schema.Property("forbidden_moves", schema.Array("Những diễn tiến tuyệt đối không được xảy ra trong chương này", schema.String(""))),
		schema.Property("continuity_checks", schema.Array("Các điểm liên tục cần đối chiếu riêng trong chương này", schema.String(""))),
		schema.Property("evaluation_focus", schema.Array("Các mục Editor cần kiểm tra trọng tâm", schema.String(""))),
		schema.Property("emotion_target", schema.String("Tùy chọn: cảm xúc bạn muốn độc giả chủ yếu cảm nhận ở chương này")),
		schema.Property("payoff_points", schema.Array("Tùy chọn: các điểm tình tiết hoặc điểm hồi đáp muốn trả ở chương then chốt", schema.String(""))),
		schema.Property("hook_goal", schema.String("Tùy chọn: mục tiêu khơi gợi ham muốn đọc tiếp hoặc treo lửng ở cuối chương")),
	)
}

func (t *PlanChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	plan, err := decodeChapterPlanArgs(args)
	if err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if plan.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	completed := progress != nil && slices.Contains(progress.CompletedChapters, plan.Chapter)
	// A chapter in the rework queue is the sole exception: when a completed chapter must be rewritten, "what the
	// rewrite should look like" needs somewhere to be persisted, and the Contract (goal / payoff_points /
	// continuity_checks / hook_goal) is exactly that instruction. Refusing here outright used to block all three routes
	// — revise_outline (only unwritten chapters), save_foundation(outline) (no full overwrite during writing) and this
	// tool — leaving the architect nowhere to write the rewrite direction, measured as four idle spins before the
	// circuit breaker.
	queuedForRewrite := progress != nil && slices.Contains(progress.PendingRewrites, plan.Chapter)
	if completed && !queuedForRewrite {
		return json.Marshal(map[string]any{
			"chapter":   plan.Chapter,
			"skipped":   true,
			"completed": true,
			"reason":    fmt.Sprintf("Chương %d đã commit xong, không thể quy hoạch lại", plan.Chapter),
		})
	}
	if err := t.store.Progress.ValidateChapterWork(plan.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureChapterExpanded(t.store, plan.Chapter); err != nil {
		return nil, err
	}

	if err := t.store.Drafts.SaveChapterPlan(plan); err != nil {
		return nil, fmt.Errorf("save chapter plan: %w", err)
	}
	// Recording a rework instruction is not the same as starting to write the chapter: the Engine already pre-marks it
	// in progress when dispatching the writer (engine.go) and draft_chapter marks it again when the pen touches paper,
	// so it would be redundant here for a rework chapter. StartChapter, by contrast, unconditionally rewrites
	// InProgressChapter and clears CompletedScenes — if the planner wrote an instruction for chapter 20 in the queue
	// while the writer is on chapter 13, the pointer would be yanked away.
	if !queuedForRewrite || progress.InProgressChapter == plan.Chapter {
		if err := t.store.Progress.StartChapter(plan.Chapter); err != nil {
			return nil, fmt.Errorf("mark chapter in progress: %w", err)
		}
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(plan.Chapter), "plan",
		fmt.Sprintf("drafts/%02d.plan.json", plan.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint chapter plan: %w", err)
	}

	return json.Marshal(map[string]any{
		"planned":   true,
		"chapter":   plan.Chapter,
		"next_step": "Gọi ngay draft_chapter(chapter=<số chương>, content=<chuỗi chính văn đầy đủ>) để ghi chính văn, đừng quy hoạch lại cùng một chương",
	})
}

func decodeChapterPlanArgs(args json.RawMessage) (domain.ChapterPlan, error) {
	var a struct {
		Chapter          int      `json:"chapter"`
		Title            string   `json:"title"`
		Goal             string   `json:"goal"`
		Conflict         string   `json:"conflict"`
		Hook             string   `json:"hook"`
		EmotionArc       string   `json:"emotion_arc"`
		Notes            string   `json:"notes"`
		RequiredBeats    []string `json:"required_beats"`
		ForbiddenMoves   []string `json:"forbidden_moves"`
		ContinuityChecks []string `json:"continuity_checks"`
		EvaluationFocus  []string `json:"evaluation_focus"`
		EmotionTarget    string   `json:"emotion_target"`
		PayoffPoints     []string `json:"payoff_points"`
		HookGoal         string   `json:"hook_goal"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return domain.ChapterPlan{}, err
	}

	return domain.ChapterPlan{
		Chapter:    a.Chapter,
		Title:      a.Title,
		Goal:       a.Goal,
		Conflict:   a.Conflict,
		Hook:       a.Hook,
		EmotionArc: a.EmotionArc,
		Notes:      a.Notes,
		Contract: domain.ChapterContract{
			RequiredBeats:    a.RequiredBeats,
			ForbiddenMoves:   a.ForbiddenMoves,
			ContinuityChecks: a.ContinuityChecks,
			EvaluationFocus:  a.EvaluationFocus,
			EmotionTarget:    a.EmotionTarget,
			PayoffPoints:     a.PayoffPoints,
			HookGoal:         a.HookGoal,
		},
	}, nil
}
