package userrules

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// A nil model plus empty rules directories: normalisation degrades entirely, but the snapshot is still
// produced (with system_defaults as the fallback) and persisted. Both directories in LoadOptions{} are
// empty strings and RawFileSources returns nil, so the test never touches the real disk.
func newDegradedService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := store.NewStore(t.TempDir())
	return NewService(st, nil, rules.LoadOptions{}), st
}

func TestService_Build_DegradesButPersists(t *testing.T) {
	svc, st := newDegradedService(t)

	snap, err := svc.Build(t.Context(), "每章1200字，主角冷静克制")
	if err != nil {
		t.Fatalf("Build 不应报错（降级而非阻断）：%v", err)
	}
	if snap.Status != rules.StatusDegraded {
		t.Fatalf("无模型应降级，status=%q", snap.Status)
	}
	// system_defaults always backs the mechanical baseline.
	if len(snap.Structured.FatigueWords) == 0 || len(snap.Structured.ForbiddenPhrases) == 0 {
		t.Fatalf("应保留 system_defaults 机械基线，got %+v", snap.Structured)
	}
	// The startup prompt degrades to raw preferences, losing none of the original text.
	if snap.Preferences == "" {
		t.Fatal("降级应把启动 prompt 原文记入 preferences")
	}

	// Already persisted: GetOrBuild reads the same copy back instead of rebuilding.
	reloaded, err := st.UserRules.Load()
	if err != nil || reloaded == nil {
		t.Fatalf("快照应已落盘：err=%v snap=%v", err, reloaded)
	}
	if reloaded.Preferences != snap.Preferences {
		t.Fatal("落盘内容与返回不一致")
	}
}

func TestService_GetOrBuildInitializesMissingSnapshot(t *testing.T) {
	svc, st := newDegradedService(t)

	if cur, _ := st.UserRules.Load(); cur != nil {
		t.Fatal("初始应无快照")
	}
	snap, err := svc.GetOrBuild(t.Context())
	if err != nil {
		t.Fatalf("GetOrBuild 不应报错：%v", err)
	}
	if len(snap.Structured.FatigueWords) == 0 {
		t.Fatal("惰性生成应含 system_defaults")
	}
	if cur, _ := st.UserRules.Load(); cur == nil {
		t.Fatal("GetOrBuild 应顺带落盘")
	}
}

func TestService_AddRuntimeRule_PersistsAndReturnsCandidate(t *testing.T) {
	svc, st := newDegradedService(t)

	const text = "以后少用比喻"
	merged, cand, err := svc.AddRuntimeRule(t.Context(), text)
	if err != nil {
		t.Fatalf("AddRuntimeRule 不应报错：%v", err)
	}
	// The candidate is for echo-back: it degrades without a model and the original text goes into preferences.
	if !cand.Degraded {
		t.Fatal("无模型时本次候选应降级")
	}
	if cand.Preferences != text {
		t.Fatalf("候选应保留原文，got %q", cand.Preferences)
	}
	// After merging, the snapshot contains that entry and is on disk.
	if merged.Preferences == "" {
		t.Fatal("叠加后 preferences 不应为空")
	}
	reloaded, err := st.UserRules.Load()
	if err != nil || reloaded == nil {
		t.Fatalf("叠加后应落盘：err=%v", err)
	}
	if reloaded.Status != rules.StatusDegraded {
		t.Fatalf("含降级来源，status 应为 degraded，got %q", reloaded.Status)
	}
}
