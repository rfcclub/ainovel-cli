package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// TestHostReopen guards /reopen's user-level reopening path: completion is a heavyweight decision and
// reopening can only be initiated explicitly by the user — refused when not complete and refused while
// running; a successful reopen drops phase back to writing, registers the attached continuation direction
// as a pending intervention (PendingSteer), and on recovery the Arbiter adjudicates and injects it before
// the engine resumes.
func TestHostReopen(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st, events: make(chan Event, 8)}

	if err := st.Progress.Init(2); err != nil {
		t.Fatal(err)
	}
	if err := h.Reopen(""); err == nil {
		t.Fatal("未完结的书应拒绝重开")
	}

	_ = st.Progress.UpdatePhase(domain.PhaseWriting)
	if err := st.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := h.Reopen("以八十年大限开新卷"); err != nil {
		t.Fatalf("完结书重开应成功：%v", err)
	}
	p, _ := st.Progress.Load()
	if p.Phase != domain.PhaseWriting {
		t.Fatalf("重开后 phase 应为 writing，得 %s", p.Phase)
	}
	if len(p.PendingRewrites) != 0 || p.ReopenedFromComplete {
		t.Fatalf("续写重开不得携带返工语义：%+v", p)
	}
	// The reopen counter must be persisted so that a second completion's progress digest differs from the
	// last one — a checkpoint with the same digest is deduplicated, so a byte-identical re-completion would
	// add no checkpoint and the StopGuard would misjudge a successful finish as idling to a halt.
	if p.ReopenCount != 1 {
		t.Fatalf("重开计数应为 1，得 %d", p.ReopenCount)
	}
	meta, _ := st.RunMeta.Load()
	if meta == nil || !strings.Contains(meta.PendingSteer, "八十年大限") {
		t.Fatalf("续写方向应登记为待处理干预，得 %+v", meta)
	}

	running := &Host{store: st, lifecycle: lifecycleRunning, events: make(chan Event, 1)}
	if err := running.Reopen(""); err == nil {
		t.Fatal("引擎运行中应拒绝重开")
	}
}
