package host

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/store"
)

// newFlagTestHost builds a minimal Host, just enough to drive the cocreating flag state machine and the
// concurrency guard. emitEvent uses a non-blocking channel, so a buffered events is enough and no observer
// is needed.
// PauseForCoCreate's running branch calls Engine Abort (reusing the already-verified Esc pause path) and
// is not unit-tested here; this covers the non-running state plus the flag and guard logic.
func newFlagTestHost(lc lifecycle, cocreating bool) *Host {
	return &Host{
		lifecycle:  lc,
		cocreating: cocreating,
		engine:     &engine{}, // acquireExclusive 查 engine.isRunning()（停止窗口门禁）
		events:     make(chan Event, 16),
	}
}

func TestPauseForCoCreate_NonRunningSetsFlag(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("idle 态应允许进入阶段共创")
	}
	if !h.cocreating {
		t.Error("进入后 cocreating 应为 true")
	}
	if h.lifecycle != lifecycleIdle {
		t.Errorf("非运行态进入不应改 lifecycle，得 %s", h.lifecycle)
	}
}

func TestPauseForCoCreate_RejectsCompleted(t *testing.T) {
	h := newFlagTestHost(lifecycleCompleted, false)
	if h.PauseForCoCreate() {
		t.Error("全书完成后不应允许进入阶段共创")
	}
	if h.cocreating {
		t.Error("拒绝后不应置位 cocreating")
	}
}

func TestPauseForCoCreate_RejectsReentrant(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if h.PauseForCoCreate() {
		t.Error("已在共创中应拒绝重入")
	}
}

func TestCancelCoCreate_ClearsFlag(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	h.CancelCoCreate()
	if h.cocreating {
		t.Error("取消后 cocreating 应清空")
	}
	if h.lifecycle != lifecyclePaused {
		t.Errorf("取消不应改 lifecycle，得 %s", h.lifecycle)
	}
}

func TestCancelCoCreate_NoopWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecycleRunning, false)
	h.CancelCoCreate() // 不应 panic，不应改状态
	if h.cocreating || h.lifecycle != lifecycleRunning {
		t.Error("非共创态 CancelCoCreate 应为 no-op")
	}
}

func TestResumeFromCoCreate_RejectsEmptyDraft(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if err := h.ResumeFromCoCreate("   "); err == nil {
		t.Fatal("空 draft 应报错")
	}
	if !h.cocreating {
		t.Error("空 draft 在清标记前返回，cocreating 应保持 true")
	}
}

func TestResumeFromCoCreate_RejectsWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, false)
	err := h.ResumeFromCoCreate("## 后续走向\n- 进入第二卷")
	if err == nil || !strings.Contains(err.Error(), "not in co-create") {
		t.Fatalf("非共创态应报 not in co-create，得 %v", err)
	}
}

func TestAcquireExclusive(t *testing.T) {
	cases := []struct {
		name       string
		lc         lifecycle
		cocreating bool
		exclusive  string
		wantErr    string // 空=期望放行
	}{
		{"running", lifecycleRunning, false, "", "đang chạy"},
		{"cocreating", lifecyclePaused, true, "", "đồng sáng tác giai đoạn"},
		{"busy", lifecycleIdle, false, "导入", "đang chạy"},
		{"idle free", lifecycleIdle, false, "", ""},
		{"paused free", lifecyclePaused, false, "", ""},
	}
	// The Abort stop window: lifecycle is already paused while the engine goroutine has not fully exited, and it
	// must still refuse — otherwise an import would race the engine's wrap-up on the same store.
	drain := newFlagTestHost(lifecyclePaused, false)
	drain.engine.running = true
	if err := drain.acquireExclusive("导入"); err == nil {
		t.Fatal("引擎排水期应拒绝独占作业")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFlagTestHost(c.lc, c.cocreating)
			h.exclusive = c.exclusive
			err := h.acquireExclusive("导入")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应放行，得 %v", err)
				}
				if h.exclusive != "导入" {
					t.Fatalf("放行后应登记占用，得 %q", h.exclusive)
				}
				h.releaseExclusive()
				if h.exclusive != "" {
					t.Fatalf("释放后占用应清空，得 %q", h.exclusive)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("应含 %q，得 %v", c.wantErr, err)
			}
			if !strings.Contains(err.Error(), "导入") {
				t.Errorf("错误文案应带 action %q，得 %v", "导入", err)
			}
		})
	}
}

// TestExclusiveBlocksCreationEntries guards #2: while a background exclusive job (import / imitation) runs,
// not only is a second background job blocked but the creation write entries (Continue/Resume) and new
// background jobs must be blocked too — otherwise Continue would have the Arbiter change state before the
// gate stopped the engine, and the engine could jump the gun during Resume/next.
func TestExclusiveBlocksCreationEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	h.exclusive = "导入"
	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("独占作业期间 ImportFrom 应被拒")
	}
	if err := h.Continue("继续写"); err == nil {
		t.Error("独占作业期间 Continue 应被拒（须在 Arbiter 裁定前挡住）")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("独占作业期间 Resume 应被拒")
	}
}

// TestStageCoCreate_OccupancyBlocksConcurrentEntries verifies every exclusivity entry point is blocked
// inside a cocreation window: import/start/resume/continue should all be refused while cocreating, closing
// the gap where only ==running was checked during a paused period.
func TestStageCoCreate_OccupancyBlocksConcurrentEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("进入阶段共创失败")
	}

	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("共创窗口内 ImportFrom 应被拒")
	}
	if err := h.StartPrepared("写个新故事"); err == nil {
		t.Error("共创窗口内 StartPrepared 应被拒")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("共创窗口内 Resume 应被拒")
	}
	if err := h.Continue("继续写"); err == nil {
		t.Error("共创窗口内 Continue 应被拒")
	}

	// Occupancy is released after leaving cocreation (via Cancel here; the Resume intervention path is covered by integration tests)
	h.CancelCoCreate()
	if h.cocreating {
		t.Fatal("退出后占用标记应解除")
	}
}

func TestBuildStoryStateSummary_NilStore(t *testing.T) {
	if got := buildStoryStateSummary(nil); got != "" {
		t.Errorf("store nil phải trả chuỗi rỗng, nhận %q", got)
	}
}

func TestBuildStoryStateSummary_Populated(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(100); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "影之诗", Synopsis: "少年追索失落的影子。"}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Progress.Load()
	p.CompletedChapters = []int{1, 2, 3}
	p.TotalWordCount = 12000
	if err := st.Progress.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "主角登临绝巅",
		OpenThreads:     []string{"师门血仇未报"},
		EstimatedScale:  "预计 4-6 卷",
	}); err != nil {
		t.Fatal(err)
	}

	got := buildStoryStateSummary(st)
	for _, want := range []string{"影之诗", "đã hoàn thành 3 chương", "chương tiếp theo là chương 4", "主角登临绝巅", "师门血仇未报", "预计 4-6 卷"} {
		if !strings.Contains(got, want) {
			t.Errorf("tóm tắt phải chứa %q, thực tế:\n%s", want, got)
		}
	}
}

func TestBuildStoryStateSummaryUsesDynamicPlanningWording(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(66); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Title: "卷一", Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, EstimatedChapters: 64},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	p, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Layered = true
	if err := st.Progress.Save(p); err != nil {
		t.Fatal(err)
	}

	got := buildStoryStateSummary(st)
	if !strings.Contains(got, "hiện đã chi tiết hóa 2 chương (phần sau quy hoạch động theo cung)") {
		t.Fatalf("cách diễn đạt tóm tắt quy hoạch động sai:\n%s", got)
	}
	if strings.Contains(got, "66") || strings.Contains(got, "quy hoạch 2 chương") {
		t.Fatalf("tóm tắt quy hoạch động không được gợi ý tổng số chương cố định:\n%s", got)
	}
}
