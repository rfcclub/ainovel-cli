package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/models"
)

func TestUsageTrackerReplaySessionsReadsWorkerLogs(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "meta", "sessions", "agents")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	rec := sessionRecord{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Input: 1200, Output: 300, CacheRead: 800,
		},
		Meta: &sessionRecordMeta{Provider: "openrouter", Model: "test-model"},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(sessionsDir, "writer-ch01.jsonl"), data, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	tk := NewUsageTracker(nil, nil)
	n, err := tk.ReplaySessions(dir)
	if err != nil {
		t.Fatalf("ReplaySessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("replayed messages = %d, want 1", n)
	}
	_, input, output, cacheRead, _ := tk.Totals()
	if input != 1200 || output != 300 || cacheRead != 800 {
		t.Fatalf("replayed totals = input:%d output:%d cache:%d", input, output, cacheRead)
	}
}

// makeUsageMsg builds a message the OnMessage callback accepts (carrying Usage). Role must be set explicitly
// to assistant because UsageTracker.Record now filters by role and only assistant messages accumulate (other
// roles carry no usage naturally).
func makeUsageMsg(input, cacheRead, cacheWrite, output int) agentcore.AgentMessage {
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite,
		},
	}
}

// Test_pushSample_RingBuffer verifies the sliding window's rotation semantics: the first N append directly
// and afterwards sampleIdx overwrites the oldest entry, with recentSums always reflecting "the last N".
func Test_pushSample_RingBuffer(t *testing.T) {
	var tot agentTotals

	for i := 1; i <= recentSampleCap; i++ {
		pushSample(&tot, i, i*100)
	}
	if got := len(tot.samples); got != recentSampleCap {
		t.Fatalf("after %d pushes, samples len=%d want %d", recentSampleCap, got, recentSampleCap)
	}

	pushSample(&tot, 999, 99900)
	if got := len(tot.samples); got != recentSampleCap {
		t.Fatalf("after overflow, samples len=%d want %d (no growth)", got, recentSampleCap)
	}
	cacheRead, input := recentSums(&tot)
	expectedCacheRead := 999
	expectedInput := 99900
	for i := 2; i <= recentSampleCap; i++ {
		expectedCacheRead += i
		expectedInput += i * 100
	}
	if cacheRead != expectedCacheRead || input != expectedInput {
		t.Fatalf("recentSums after overflow = (%d, %d), want (%d, %d)",
			cacheRead, input, expectedCacheRead, expectedInput)
	}
}

// Test_UsageTracker_RecordAccumulates verifies Record accumulates across roles correctly: the overall merge
// equals the sum of all roles, with each role independent.
func Test_UsageTracker_RecordAccumulates(t *testing.T) {
	tk := NewUsageTracker(nil, nil) // modelSet=nil → 走 provider Cost 兜底，不影响累计逻辑

	tk.Record("writer", "", makeUsageMsg(1000, 800, 0, 200))
	tk.Record("writer", "", makeUsageMsg(1500, 1200, 100, 300))
	tk.Record("editor", "", makeUsageMsg(500, 0, 0, 100))

	cost, in, out, cr, cw := tk.Totals()
	if in != 3000 || out != 600 || cr != 2000 || cw != 100 {
		t.Fatalf("totals = (in=%d out=%d cr=%d cw=%d), want (3000 600 2000 100)", in, out, cr, cw)
	}
	if cost != 0 {
		t.Errorf("cost should be 0 when modelSet=nil and no provider Cost, got %v", cost)
	}

	per := tk.PerAgent()
	if len(per) != 2 {
		t.Fatalf("per-agent len=%d want 2", len(per))
	}
	// PerAgent is ordered by descending CacheRead: writer (2000) should precede editor (0)
	if per[0].Role != "writer" || per[1].Role != "editor" {
		t.Fatalf("per-agent order = %s,%s want writer,editor", per[0].Role, per[1].Role)
	}
	if per[0].Input != 2500 || per[0].CacheRead != 2000 {
		t.Errorf("writer totals = (in=%d cr=%d), want (2500 2000)", per[0].Input, per[0].CacheRead)
	}
}

// Test_UsageTracker_ArchitectAliasNormalized verifies architect_short/mid/long all normalise to the single key
// "architect" (avoiding a split into several rows by a /model-switched sub-role).
func Test_UsageTracker_ArchitectAliasNormalized(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("architect_short", "", makeUsageMsg(100, 50, 0, 20))
	tk.Record("architect_mid", "", makeUsageMsg(200, 100, 0, 40))
	tk.Record("architect_long", "", makeUsageMsg(300, 150, 0, 60))

	per := tk.PerAgent()
	if len(per) != 1 {
		t.Fatalf("aliases must merge to single role, got %d entries: %+v", len(per), per)
	}
	if per[0].Role != "architect" {
		t.Fatalf("merged role name = %q, want architect", per[0].Role)
	}
	if per[0].Input != 600 || per[0].CacheRead != 300 {
		t.Errorf("merged totals = (in=%d cr=%d), want (600 300)", per[0].Input, per[0].CacheRead)
	}
}

func Test_UsageTracker_PerModelAccumulates(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.accumulate("writer", "openrouter", "model-a", agentcore.Usage{Input: 1000, Output: 200, CacheRead: 700})
	tk.accumulate("editor", "openrouter", "model-b", agentcore.Usage{Input: 500, Output: 100})
	tk.accumulate("writer", "openrouter", "model-a", agentcore.Usage{Input: 300, Output: 80, CacheRead: 200})

	perModel := tk.PerModel()
	if len(perModel) != 2 {
		t.Fatalf("per-model len=%d want 2", len(perModel))
	}
	seen := map[string]AgentUsage{}
	for _, m := range perModel {
		seen[m.Model] = m
	}
	if seen["openrouter/model-a"].Input != 1300 || seen["openrouter/model-a"].CacheRead != 900 {
		t.Errorf("model-a totals = %+v", seen["openrouter/model-a"])
	}
	if seen["openrouter/model-b"].Output != 100 {
		t.Errorf("model-b totals = %+v", seen["openrouter/model-b"])
	}

	snap := tk.Snapshot()
	restored := NewUsageTracker(nil, nil)
	restored.applyState(snap)
	if got := restored.PerModel(); len(got) != 2 {
		t.Fatalf("restored per-model len=%d want 2: %+v", len(got), got)
	}
}

func Test_UsageTracker_RecordUsesActualUsageModel(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("writer", "", agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Provider: "openrouter",
			Model:    "google/gemini-2.5-pro",
			Input:    1000,
			Output:   200,
		},
	})

	perModel := tk.PerModel()
	if len(perModel) != 1 {
		t.Fatalf("per-model len=%d want 1: %+v", len(perModel), perModel)
	}
	if perModel[0].Model != "openrouter/google/gemini-2.5-pro" {
		t.Fatalf("model key = %q, want openrouter/google/gemini-2.5-pro", perModel[0].Model)
	}
	if perModel[0].Input != 1000 || perModel[0].Output != 200 {
		t.Fatalf("model totals = %+v", perModel[0])
	}
}

func Test_UsageTracker_ProviderOnlyDoesNotInventModelKey(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("writer", "", agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Provider: "openrouter",
			Input:    1000,
			Output:   200,
		},
	})

	if got := tk.PerModel(); len(got) != 0 {
		t.Fatalf("provider-only usage must not create model stats without a model, got %+v", got)
	}
}

// Test_UsageTracker_RecentWindowReflectsLatest verifies the sliding window reflects "the last N" without being
// dragged down by an early low hit rate — exactly the "early drag vs steady low hit rate" problem P1 set out to
// solve.
func Test_UsageTracker_RecentWindowReflectsLatest(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// The first 5 with very low hits (the first-chapter scenario)
	for i := 0; i < 5; i++ {
		tk.Record("writer", "", makeUsageMsg(1000, 0, 0, 200))
	}
	// The next 8 (over 5) with high hits (the steady-state scenario)
	for i := 0; i < 8; i++ {
		tk.Record("writer", "", makeUsageMsg(1000, 900, 0, 200))
	}

	per := tk.PerAgent()
	if len(per) != 1 {
		t.Fatalf("len=%d want 1", len(per))
	}
	w := per[0]

	// Cumulative: 8 hits out of 13 → 7200/13000 ≈ 55.4%
	cumulativeRate := float64(w.CacheRead) / float64(w.Input) * 100
	if cumulativeRate < 50 || cumulativeRate > 60 {
		t.Errorf("cumulative hit rate = %.1f%%, want ~55%%", cumulativeRate)
	}

	// Sliding window: of the last 10, 8 high hits plus 2 zeros → 7200/10000 = 72%
	if w.RecentSamples != recentSampleCap {
		t.Errorf("recent samples = %d, want %d (window full)", w.RecentSamples, recentSampleCap)
	}
	recentRate := float64(w.RecentCacheRead) / float64(w.RecentInput) * 100
	if recentRate < 70 || recentRate > 75 {
		t.Errorf("recent hit rate = %.1f%%, want ~72%% (proves window dropped early misses)", recentRate)
	}
	// Key: the last-N figure is clearly above the cumulative one, proving the early zeros have fallen out of the window
	if recentRate <= cumulativeRate {
		t.Errorf("recent (%.1f%%) must exceed cumulative (%.1f%%) once window slides past early misses",
			recentRate, cumulativeRate)
	}
}

// Test_computeSaved verifies the saved algorithm: CacheRead × (input price − CacheRead price); it returns 0
// when the difference is ≤ 0 or InputCost ≤ 0 (the CacheWrite premium is not offset).
func Test_computeSaved(t *testing.T) {
	cases := []struct {
		name  string
		usage agentcore.Usage
		entry models.ModelEntry
		want  float64
	}{
		{
			name:  "anthropic 5m 命中节省 90%",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 80_000},
			entry: models.ModelEntry{InputCostPer1M: 3.0, CacheReadCostPer1M: 0.3},
			want:  80_000 * (3.0 - 0.3) / 1_000_000, // 0.216
		},
		{
			name:  "无命中 saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 0},
			entry: models.ModelEntry{InputCostPer1M: 3.0, CacheReadCostPer1M: 0.3},
			want:  0,
		},
		{
			name:  "模型未标价 saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 50_000},
			entry: models.ModelEntry{InputCostPer1M: 0, CacheReadCostPer1M: 0},
			want:  0,
		},
		{
			name:  "异常价差 saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 50_000},
			entry: models.ModelEntry{InputCostPer1M: 1.0, CacheReadCostPer1M: 2.0}, // 缓存反而更贵
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSaved(tc.usage, tc.entry)
			if got != tc.want {
				t.Errorf("computeSaved=%v want %v", got, tc.want)
			}
		})
	}
}

// Test_UsageTracker_CacheCapableSticky verifies CacheCapable never falls back once true. Having run a
// cache-capable model historically makes the accumulated hit data meaningful, and a mid-run switch to an
// unsupported model must not roll the flag back.
//
// Simulated by assigning perAgent directly (the resolveCost path needs ModelSet+Registry, covered by the integration layer).
func Test_UsageTracker_CacheCapableSticky(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// Simulate "ran a cache-capable model before with hits"
	tk.perAgent["writer"] = &agentTotals{
		Input: 1000, CacheRead: 500, Output: 200, CacheCapable: true,
	}
	// Then append one "call on a model without cache support"
	tk.Record("writer", "", makeUsageMsg(500, 0, 0, 100))

	per := tk.PerAgent()
	if len(per) != 1 || per[0].Role != "writer" {
		t.Fatalf("expected single writer entry, got %+v", per)
	}
	if !per[0].CacheCapable {
		t.Errorf("CacheCapable must remain true after switching to non-capable model")
	}
	if per[0].CacheRead != 500 || per[0].Input != 1500 {
		t.Errorf("totals after merge = (in=%d cr=%d), want (1500 500)",
			per[0].Input, per[0].CacheRead)
	}
}

// Test_UsageTracker_PerAgentSkipsZero verifies a role that consumed no tokens does not appear in PerAgent.
func Test_UsageTracker_PerAgentSkipsZero(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	// Build a role that consumes no tokens (an extreme case)
	tk.perAgent["ghost"] = &agentTotals{}
	tk.Record("writer", "", makeUsageMsg(100, 50, 0, 20))

	per := tk.PerAgent()
	if len(per) != 1 || per[0].Role != "writer" {
		t.Fatalf("ghost role with zero tokens must be skipped, got %+v", per)
	}
}

// Test_UsageTracker_MissingAssistantUsageCounted verifies the decision boundary of the missingAssistantUsage
// counter:
//   - The accumulation path only asks whether Usage != nil (not tied to Role).
//   - The diagnostic path requires Role=Assistant with non-empty Content — that is what looks like "a genuine
//     LLM response that carried no usage", matching the typical face of an upstream stream never sending
//     OpenAI's final include_usage chunk. Other cases (user/tool messages, an assistant with empty content)
//     do not count as missing.
func Test_UsageTracker_MissingAssistantUsageCounted(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	withContent := func(text string) agentcore.Message {
		return agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
		}
	}

	// assistant + Content present + nil Usage → looks like a genuine response missing usage, counted diagnostically
	tk.Record("writer", "", withContent("hi"))
	tk.Record("writer", "", withContent("again"))
	// assistant with empty Content → an error-recovery path or placeholder message, not counted as missing
	tk.Record("writer", "", agentcore.Message{Role: agentcore.RoleAssistant})
	// user/tool messages never carry usage, so they do not count as missing regardless of Content
	tk.Record("writer", "", agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("u")}})
	tk.Record("writer", "", agentcore.Message{Role: agentcore.RoleTool, Content: []agentcore.ContentBlock{agentcore.TextBlock("t")}})
	// A normal message with usage → takes the accumulation path and is not counted diagnostically
	tk.Record("writer", "", makeUsageMsg(100, 50, 0, 20))

	if got := tk.MissingAssistantUsage(); got != 2 {
		t.Errorf("MissingAssistantUsage=%d, want 2", got)
	}
	_, in, _, _, _ := tk.Totals()
	if in != 100 {
		t.Errorf("正常路径累计被破坏，input=%d want 100", in)
	}
}

// Test_UsageTracker_CacheCapableFromFacts verifies CacheCapable can still be marked true from facts when the
// registry has no entry for the model: self-hosted and proxy-backend models are often absent from the
// BerriAI/litellm pricing index and resolveCost returns capable=false, but as long as the backend genuinely
// returned CacheRead or CacheWrite > 0 the model objectively supports prompt caching and the per-role row must
// not read "not enabled".
func Test_UsageTracker_CacheCapableFromFacts(t *testing.T) {
	tk := NewUsageTracker(nil, nil) // modelSet=nil → resolveCost 永远 capable=false

	// One call with CacheWrite (simulating a first cache write where the registry says not capable but the facts prove support)
	tk.Record("writer", "", makeUsageMsg(1000, 0, 200, 100))
	per := tk.PerAgent()
	if len(per) != 1 || !per[0].CacheCapable {
		t.Fatalf("CacheWrite > 0 应立即标记 CacheCapable=true，got %+v", per)
	}
	if !tk.OverallCacheCapable() {
		t.Errorf("overall CacheCapable 也应同步置 true")
	}

	// Converse: a role with no cache activity at all must keep CacheCapable false
	tk.Record("editor", "", makeUsageMsg(500, 0, 0, 100))
	per = tk.PerAgent()
	for _, a := range per {
		if a.Role == "editor" && a.CacheCapable {
			t.Errorf("editor 全程无 CacheRead/Write，CacheCapable 不应被错误标记为 true")
		}
	}
}

// Test_UsageTracker_AccumulatesAnyRoleWithUsage verifies the accumulation path is decoupled from Role: even if a
// future adapter attaches usage to a non-assistant role's message it still accumulates correctly, upholding the
// "assembly rules decoupled from accumulation rules" contract.
func Test_UsageTracker_AccumulatesAnyRoleWithUsage(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	// Build a theoretically uncommon non-assistant message carrying Usage
	hypothetical := agentcore.Message{
		Role:  agentcore.RoleSystem,
		Usage: &agentcore.Usage{Input: 200, Output: 50, CacheRead: 100},
	}
	tk.Record("writer", "", hypothetical)

	_, in, out, cr, _ := tk.Totals()
	if in != 200 || out != 50 || cr != 100 {
		t.Errorf("未按 Usage 字段累加，got (in=%d out=%d cr=%d) want (200 50 100)", in, out, cr)
	}
	if tk.MissingAssistantUsage() != 0 {
		t.Errorf("有 Usage 不应计入 missing")
	}
}

// Test_UsageTracker_OnCostCallback verifies the budget sentinel's wiring point: after each accounting entry an
// out-of-lock callback carries the latest cumulative cost (including the provider-reported cost path).
func Test_UsageTracker_OnCostCallback(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	var got []float64
	tk.SetOnCost(func(total float64) { got = append(got, total) })

	msg := func(cost float64) agentcore.AgentMessage {
		return agentcore.Message{
			Role:  agentcore.RoleAssistant,
			Usage: &agentcore.Usage{Input: 100, Output: 10, Cost: &agentcore.Cost{Total: cost}},
		}
	}
	tk.Record("writer", "", msg(0.5))
	tk.Record("writer", "", msg(0.25))

	if len(got) != 2 || got[0] != 0.5 || got[1] != 0.75 {
		t.Fatalf("onCost should carry growing totals, got %v", got)
	}
}

// Test_UsageTracker_OnMissingUsageOnce verifies the blind-spot callback fires only the first time.
func Test_UsageTracker_OnMissingUsageOnce(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	fired := 0
	tk.SetOnMissingUsage(func() { fired++ })

	noUsage := agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("正文")}}
	tk.Record("writer", "", noUsage)
	tk.Record("writer", "", noUsage)
	tk.Record("editor", "", noUsage)

	if fired != 1 {
		t.Fatalf("onMissingUsage should fire exactly once, got %d", fired)
	}
}

// TestCacheBreakDetection verifies the four trajectories of cache-chain break detection: a growing prefix with
// collapsing hits within one session → a break; a new task (a new spawn) → a new baseline without comparison; a
// shrinking prefix (in-session compaction) → reset the baseline without warning; a drop failing the dual
// thresholds (5% relative and 2000 absolute) → no warning.
func TestCacheBreakDetection(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// Establish the baseline: prefix 30k, hits 28k.
	tk.Record("writer", "写第1章", makeUsageMsg(30000, 28000, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 0 {
		t.Fatalf("首条消息不应判断裂，got %d", got)
	}

	// Within one session the prefix grows while hits collapse (28k→4k) → a break.
	tk.Record("writer", "写第1章", makeUsageMsg(34000, 4096, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("前缀增长+命中骤降应判 1 次断裂，got %d", got)
	}

	// Within one session the prefix shrinks (context compaction, 4.4k < 34k) → reset the baseline without warning.
	tk.Record("writer", "写第1章", makeUsageMsg(4400, 0, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("前缀缩短应视为压缩重置，got %d", got)
	}

	// A small decline on the new baseline (under the 2000 absolute threshold) → no warning.
	tk.Record("writer", "写第1章", makeUsageMsg(36000, 30000, 0, 100))
	tk.Record("writer", "写第1章", makeUsageMsg(38000, 28500, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("降幅 1.5k 未过绝对阈值不应告警，got %d", got)
	}

	// A new task = a new spawn = a new cache lineage: even when the first request's prefix is no shorter than the
	// previous session's last (38k → 40k) with hits collapsing (28.5k→0), it neither compares nor warns. This is the
	// "consecutive short sessions false-alarm" regression case: the detection dimension must align with the session
	// granularity of prompt_cache_key.
	tk.Record("writer", "写第2章", makeUsageMsg(40000, 0, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("换 task 换基线，跨会话不应比较，got %d", got)
	}

	// A further break within the new session → a normal warning (proving the new baseline took effect).
	tk.Record("writer", "写第2章", makeUsageMsg(45000, 38000, 0, 100))
	tk.Record("writer", "写第2章", makeUsageMsg(48000, 5000, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 2 {
		t.Fatalf("新会话内断裂应正常检测，got %d", got)
	}

	// A relative drop under 5% (100k→96k, a 4% fall) → no warning (even though the absolute fall of 4k exceeds 2000).
	tk.Record("editor", "审阅第一弧", makeUsageMsg(120000, 100000, 0, 100))
	tk.Record("editor", "审阅第一弧", makeUsageMsg(125000, 96000, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 2 {
		t.Fatalf("相对降幅 4%% 未过 5%% 阈值不应告警，got %d", got)
	}

	// Per-role attribution: the break is recorded under writer and enters the Snapshot.
	snap := tk.Snapshot()
	if snap.Overall.CacheBreaks != 2 || snap.PerAgent["writer"].CacheBreaks != 2 {
		t.Fatalf("断裂计数应进快照：overall=%d writer=%d", snap.Overall.CacheBreaks, snap.PerAgent["writer"].CacheBreaks)
	}
}
