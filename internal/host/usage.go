package host

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/models"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// recentSampleCap is the sliding-window size: only the last N calls' (cacheRead, input)
// samples are kept per role, so the left panel can compare cumulative vs recent-N hit
// rates and tell "an early drag" apart from "a steady low hit rate".
const recentSampleCap = 10

// Two thresholds gate cache-chain break detection (following the empirical findings of
// Claude Code): a break needs both a relative drop over 5% against the previous read and
// an absolute fall of at least 2000 tokens — a lone relative threshold drowns in
// small-prefix noise, while a lone absolute one misses significant degradation on a large
// prefix.
const (
	cacheBreakKeepRatio     = 0.95
	cacheBreakMinDropTokens = 2000
)

// UsageTracker accumulates LLM input/output tokens and dollar cost for every agent across
// the whole session.
//
// How it works:
//   - Record(agentName, msg) is called from each agent's OnMessage callback.
//   - agentName maps to a role (architect_* normalises to architect), which resolves to the
//     model ModelSet currently binds for that role.
//   - Prices come from models.DefaultRegistry and multiply across four buckets: uncached
//     input, output, cache read and cache write.
//   - When the registry has no such model it falls back to msg.Usage.Cost.Total (supplied by
//     the provider, possibly 0).
//   - After a hot model switch (/model) later messages price against the new model while
//     earlier ones keep their old cost.
//
// It also maintains per-role dimensions (writer/editor/architect):
//   - cumulative hit data -> overall optimisation effect.
//   - a sliding window of the last N calls -> distinguishing an early drag from a steady low
//     hit rate.
//   - the CacheCapable flag -> telling "not enabled" apart from "genuinely 0% hits".
//
// Thread-safe.
type UsageTracker struct {
	mu       sync.Mutex
	overall  agentTotals
	perAgent map[string]*agentTotals // key: role name after agentRoleName normalisation
	perModel map[string]*agentTotals // key: provider/model; falls back to model when the provider is unknown
	modelSet *bootstrap.ModelSet
	store    *storepkg.Store // May be nil (tests); every persistence method is then a silent no-op

	// cacheTrack holds the per-role cache-chain baseline (previous call's prefix length, hit
	// volume and time) for break detection. It updates only on the live Record path — replay
	// does not run detection, otherwise every startup would surface years-old breaks as false
	// alarms. Not persisted.
	cacheTrack map[string]*cacheTrackState

	// missingAssistantUsage counts "assistant message received but Usage was nil".
	// Measured cases are mostly a self-hosted OpenAI-compatible backend that never sends the
	// final usage chunk required by OpenAI's stream_options.include_usage — partial.Usage
	// stays nil and every accumulated field sits at 0. The counter lets the UI tell the user
	// "the upstream returns no usage, nothing is broken here" instead of chasing the cache
	// panel code.
	missingAssistantUsage int
	loggedMissingUsage    bool // Warn once per session, so tui.log is not flooded

	// saveCh is signalled non-blockingly by Record after accumulation; autoSaveLoop listens
	// and persists on a debounce. buffered=1 folds a burst of Record calls into one save
	// signal; a full channel drops the signal and the next tick writes anyway.
	saveCh       chan struct{}
	autoSaveMu   sync.Mutex
	autoSaveDone chan struct{}

	// onCost fires after each accounting entry, outside the lock, with the latest cumulative
	// cost (BudgetSentinel line-crossing detection). It must be set through SetOnCost before
	// concurrent Record calls begin, and is read-only afterwards.
	onCost func(total float64)

	// onMissingUsage fires once when the first assistant message without Usage is seen (at
	// the same moment as the slog warn). With a budget enabled this is a billing blind spot —
	// cost stays 0 and the budget never triggers — so it must shout.
	onMissingUsage func()
}

// usageSample is the hit sample for one OnMessage, recording only the numerator and denominator of the hit rate.
type usageSample struct {
	CacheRead int
	Input     int
}

// cacheTrackState is one role's cache-chain baseline for the current session. task (the
// spawn task text) is the session identity: a new task means a new spawn and therefore a new
// cache lineage (prompt_cache_key carries #seq), where a low first-request hit rate is
// normal — so the baseline is simply replaced rather than compared. Otherwise "the previous
// session was very short while the new session's first request has a longer prefix" would
// report a false break. The Input semantics (including CacheRead, see the computeCost
// comment) are exactly "the prefix length the server processed", which distinguishes three
// trajectories: a shrinking prefix means in-session compaction (legitimate, reset the
// baseline); a growing prefix with rising hits means a healthy chain; a growing prefix with
// collapsing hits means a break.
type cacheTrackState struct {
	task          string
	lastPrefix    int
	lastCacheRead int
	lastAt        time.Time
}

// agentTotals is one agent's running counters.
//   - Saved is the difference back-derived from current hits as if billed at the non-cached rate.
//   - CacheCapable turns true only after the role has gone through at least one call on a
//     model known to support caching.
//   - samples is a fixed-size ring buffer: the first recentSampleCap entries append directly,
//     then sampleIdx cycles.
type agentTotals struct {
	Input        int
	Output       int
	CacheRead    int
	CacheWrite   int
	Cost         float64
	Saved        float64
	CacheCapable bool
	CacheBreaks  int // Cache-chain breaks detected live (replay does not count)
	samples      []usageSample
	sampleIdx    int
}

func NewUsageTracker(set *bootstrap.ModelSet, store *storepkg.Store) *UsageTracker {
	return &UsageTracker{
		modelSet:   set,
		store:      store,
		perAgent:   make(map[string]*agentTotals, 4),
		perModel:   make(map[string]*agentTotals, 4),
		cacheTrack: make(map[string]*cacheTrackState, 4),
		saveCh:     make(chan struct{}, 1),
	}
}

// Record routes one agent message down the accounting and diagnostic paths.
//
// Accounting only asks whether Usage exists — "which messages carry Usage" is an
// agentcore/litellm adapter assembly detail (upstream protocols put usage at the top level of
// the response), so a future change to that wiring does not touch this.
// Diagnostics require Role=Assistant with non-empty Content, so AbortMsg / error-recovery /
// tool / user messages cannot pollute the missingAssistantUsage counter.
func (t *UsageTracker) Record(agentName, task string, msg agentcore.AgentMessage) {
	if t == nil {
		return
	}
	m, ok := msg.(agentcore.Message)
	if !ok {
		return
	}
	if m.Usage == nil {
		if m.Role == agentcore.RoleAssistant && len(m.Content) > 0 {
			t.flagMissingUsage(agentName)
		}
		return
	}
	role := agentRoleName(agentName)
	t.noteCacheBreak(role, task, *m.Usage)
	provider, modelName := usageActualModel(m.Usage)
	t.accumulate(role, provider, modelName, *m.Usage)
}

// noteCacheBreak is the cache-chain break detector (pure observation, no repair; called only
// on the live Record path).
//
// The verdict: within one session (role+task) the prefix (Input, including CacheRead) has not
// shrunk while hits dropped over 5% against the previous call by at least 2000 tokens. A task
// change means a new spawn and a new cache lineage, so the baseline is replaced without
// comparison; a shrinking prefix indicates context compaction, a legitimate drop that only
// resets the baseline without warning. The hint is attributed by priority: a gap longer than
// the TTL suggests expiry; a very short gap where the client bytes should have been stable
// suggests server-side eviction / routing drift (a relay polling upstream accounts is the
// common cause).
func (t *UsageTracker) noteCacheBreak(role, task string, u agentcore.Usage) {
	now := time.Now()
	prefix := u.Input // litellm guarantees Input includes CacheRead for every provider

	t.mu.Lock()
	st := t.cacheTrack[role]
	if st == nil || st.task != task {
		t.cacheTrack[role] = &cacheTrackState{task: task, lastPrefix: prefix, lastCacheRead: u.CacheRead, lastAt: now}
		t.mu.Unlock()
		return
	}
	prevPrefix, prevRead, prevAt := st.lastPrefix, st.lastCacheRead, st.lastAt
	st.lastPrefix, st.lastCacheRead, st.lastAt = prefix, u.CacheRead, now

	broke := prevPrefix > 0 && prefix >= prevPrefix &&
		float64(u.CacheRead) < float64(prevRead)*cacheBreakKeepRatio &&
		prevRead-u.CacheRead >= cacheBreakMinDropTokens
	if broke {
		t.overall.CacheBreaks++
		per := t.perAgent[role]
		if per == nil {
			per = &agentTotals{}
			t.perAgent[role] = per
		}
		per.CacheBreaks++
	}
	t.mu.Unlock()

	if !broke {
		return
	}
	gap := now.Sub(prevAt).Round(time.Second)
	hint := "nghi ngờ máy chủ trục xuất/định tuyến trôi dạt (trung chuyển luân phiên thượng nguồn là nguyên nhân phổ biến)"
	if gap > time.Hour {
		hint = "nghi ngờ TTL 1 giờ đã hết hạn"
	} else if gap > 5*time.Minute {
		hint = "nghi ngờ TTL 5 phút đã hết hạn"
	}
	slog.Warn("đứt chuỗi cache: tiền tố không ngắn đi mà lượng trúng sụt mạnh",
		"module", "usage", "role", role,
		"cache_read", fmt.Sprintf("%d→%d", prevRead, u.CacheRead),
		"prefix", fmt.Sprintf("%d→%d", prevPrefix, prefix),
		"gap", gap.String(), "hint", hint)
	t.notifyDirty()
}

func usageActualModel(u *agentcore.Usage) (provider, modelName string) {
	if u == nil {
		return "", ""
	}
	return strings.TrimSpace(u.Provider), strings.TrimSpace(u.Model)
}

// flagMissingUsage records one "looks like a real LLM response yet no usage" event, warning
// only once per session so tui.log is not flooded.
func (t *UsageTracker) flagMissingUsage(agentName string) {
	t.mu.Lock()
	t.missingAssistantUsage++
	shouldLog := !t.loggedMissingUsage
	t.loggedMissingUsage = true
	t.mu.Unlock()
	if shouldLog {
		slog.Warn("phản hồi LLM không mang dữ liệu usage, bảng cache/chi phí sẽ không tích lũy — thường là do streaming thượng nguồn không gửi chunk usage cuối theo giao thức include_usage của OpenAI",
			"module", "usage", "agent", agentName)
		if t.onMissingUsage != nil {
			t.onMissingUsage()
		}
	}
	t.notifyDirty()
}

// SetOnMissingUsage registers the one-shot callback for "usage missing for the first time".
// It must be called once during Host construction, before concurrent Record calls start.
func (t *UsageTracker) SetOnMissingUsage(cb func()) {
	if t == nil {
		return
	}
	t.onMissingUsage = cb
}

// notifyDirty triggers one save signal without blocking; autoSaveLoop performs the actual
// write on a debounce. The channel is buffered=1, since folding a burst of Record calls into
// one save request is sufficient.
func (t *UsageTracker) notifyDirty() {
	if t == nil || t.saveCh == nil {
		return
	}
	select {
	case t.saveCh <- struct{}{}:
	default:
	}
}

// accumulate adds a message carrying Usage to the overall / per-role / per-model counters.
// An empty provider/model means "resolve the model bound to the role from the current
// ModelSet" (the live path); a non-empty one means "force pricing against the named model"
// (the replay path, using the _meta in the session jsonl). resolveCost runs outside the lock
// (it only reads modelSet/Registry) while the lock covers the addition alone.
func (t *UsageTracker) accumulate(role, provider, modelName string, u agentcore.Usage) {
	provider, modelName = t.effectiveModel(role, provider, modelName)
	cost, saved, capable := t.resolveCost(modelName, u)

	t.mu.Lock()
	addUsage(&t.overall, u, cost, saved, capable)

	per := t.perAgent[role]
	if per == nil {
		per = &agentTotals{}
		t.perAgent[role] = per
	}
	addUsage(per, u, cost, saved, capable)

	if key := modelUsageKey(provider, modelName); key != "" {
		perModel := t.perModel[key]
		if perModel == nil {
			perModel = &agentTotals{}
			t.perModel[key] = perModel
		}
		addUsage(perModel, u, cost, saved, capable)
	}
	total := t.overall.Cost
	t.mu.Unlock()

	t.notifyDirty()
	if t.onCost != nil {
		t.onCost(total)
	}
}

// SetOnCost registers the accounting callback (invoked outside the lock with the latest
// cumulative cost). It must be called once during Host construction, before concurrent Record
// calls start.
func (t *UsageTracker) SetOnCost(cb func(total float64)) {
	if t == nil {
		return
	}
	t.onCost = cb
}

func (t *UsageTracker) effectiveModel(role, provider, modelName string) (string, string) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		if t != nil && t.modelSet != nil {
			p, m, _ := t.modelSet.CurrentSelection(role)
			return p, m
		}
		return "", ""
	}
	if provider == "" && t != nil && t.modelSet != nil {
		p, m, _ := t.modelSet.CurrentSelection(role)
		if m == modelName {
			provider = p
		}
	}
	return provider, modelName
}

func modelUsageKey(provider, modelName string) string {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	switch {
	case modelName == "":
		return ""
	case provider == "":
		return modelName
	default:
		return provider + "/" + modelName
	}
}

// addUsage adds one call's tokens and cost to a set of totals.
// It must be called while holding UsageTracker.mu.
//
// CacheCapable prefers a factual test: as long as CacheRead or CacheWrite > 0 has been seen,
// the upstream demonstrably did prompt caching. The registry's CacheReadCostPer1M serves only
// as a fallback,
// because self-hosted backend models (mimo-v2.5-pro, domestic proxies and the like) are
// usually absent from the BerriAI/litellm pricing index even though their Usage carries full
// cache data — the UI must not misread that as "not enabled".
func addUsage(t *agentTotals, u agentcore.Usage, cost, saved float64, capable bool) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Cost += cost
	t.Saved += saved
	if capable || u.CacheRead > 0 || u.CacheWrite > 0 {
		t.CacheCapable = true
	}
	pushSample(t, u.CacheRead, u.Input)
}

// pushSample pushes one sample into the ring buffer: plain appends for the first recentSampleCap, rotating overwrites after that.
func pushSample(t *agentTotals, cacheRead, input int) {
	s := usageSample{CacheRead: cacheRead, Input: input}
	if len(t.samples) < recentSampleCap {
		t.samples = append(t.samples, s)
		return
	}
	t.samples[t.sampleIdx] = s
	t.sampleIdx = (t.sampleIdx + 1) % recentSampleCap
}

// recentSums returns the totals of cacheRead and input within the sliding window, forming the
// numerator and denominator of the "recent-N hit rate". A sum/sum ratio is used rather than an
// average of per-call ratios so that small samples (input of a few hundred tokens) cannot
// amplify noise.
func recentSums(t *agentTotals) (cacheRead, input int) {
	for _, s := range t.samples {
		cacheRead += s.CacheRead
		input += s.Input
	}
	return cacheRead, input
}

// Totals returns a snapshot of the cumulative totals.
func (t *UsageTracker) Totals() (cost float64, input, output, cacheRead, cacheWrite int) {
	if t == nil {
		return 0, 0, 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.Cost, t.overall.Input, t.overall.Output, t.overall.CacheRead, t.overall.CacheWrite
}

// SavedUSD returns the cumulative dollars saved by cache hits.
func (t *UsageTracker) SavedUSD() float64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.Saved
}

// OverallRecent returns the cacheRead total, input total and sample count within the sliding
// window (at most recentSampleCap calls).
func (t *UsageTracker) OverallRecent() (cacheRead, input, samples int) {
	if t == nil {
		return 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r, in := recentSums(&t.overall)
	return r, in, len(t.overall.samples)
}

// OverallCacheBreaks returns the total number of cache-chain breaks detected live.
func (t *UsageTracker) OverallCacheBreaks() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.CacheBreaks
}

// OverallCacheCapable reports whether any call went through a model known to support caching.
func (t *UsageTracker) OverallCacheCapable() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.CacheCapable
}

// MissingAssistantUsage returns how many assistant messages arrived with a nil Usage.
// A value above 0 usually means the upstream stream never sent OpenAI's final usage chunk,
// and the UI uses it to show a hint rather than assume the cache module itself is broken.
func (t *UsageTracker) MissingAssistantUsage() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.missingAssistantUsage
}

// ── Persistence ──

// Snapshot copies the current accumulated state into a serialisable domain.UsageState.
// The sliding-window samples stay out of it — that is a short-term diagnostic window of little
// value across processes.
func (t *UsageTracker) Snapshot() domain.UsageState {
	if t == nil {
		return domain.UsageState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := domain.UsageState{
		Schema:       domain.UsageSchemaVersion,
		UpdatedAt:    time.Now(),
		Overall:      totalsSnapshot(&t.overall),
		PerAgent:     make(map[string]domain.AgentUsageTotals, len(t.perAgent)),
		PerModel:     make(map[string]domain.AgentUsageTotals, len(t.perModel)),
		MissingUsage: t.missingAssistantUsage,
	}
	for role, v := range t.perAgent {
		state.PerAgent[role] = totalsSnapshot(v)
	}
	for model, v := range t.perModel {
		state.PerModel[model] = totalsSnapshot(v)
	}
	return state
}

// LoadFromStore reads the persisted snapshot from store.Usage and refills memory. A true
// result means a non-empty (schema-matching) state was loaded; false means no file or an
// unusable one, and the caller should fall through to a one-shot session replay refill.
func (t *UsageTracker) LoadFromStore() (bool, error) {
	if t == nil || t.store == nil {
		return false, nil
	}
	state, err := t.store.Usage.Load()
	if err != nil {
		return false, err
	}
	if state == nil {
		return false, nil
	}
	t.applyState(*state)
	return true, nil
}

// SaveNow persists the current snapshot immediately. Both autoSaveLoop and Close write through it.
func (t *UsageTracker) SaveNow() error {
	if t == nil || t.store == nil {
		return nil
	}
	return t.store.Usage.Save(t.Snapshot())
}

// StartAutoSave launches a goroutine that listens on saveCh and persists on a debounce.
// Before the context is done it flushes the last unsaved state; Close cancels the context to
// trigger that flush and exit.
func (t *UsageTracker) StartAutoSave(ctx context.Context) {
	if t == nil || t.store == nil {
		return
	}
	done := make(chan struct{})
	t.autoSaveMu.Lock()
	t.autoSaveDone = done
	t.autoSaveMu.Unlock()
	go func() {
		defer close(done)
		t.autoSaveLoop(ctx)
	}()
}

// WaitAutoSave waits for the final flush after cancellation. Host.Close cancels first and
// then waits here, so autoSaveLoop and a pre-exit SaveNow never write the same snapshot
// concurrently.
func (t *UsageTracker) WaitAutoSave() {
	if t == nil {
		return
	}
	t.autoSaveMu.Lock()
	done := t.autoSaveDone
	t.autoSaveMu.Unlock()
	if done != nil {
		<-done
	}
}

// autoSaveLoop throttles high-frequency dirty signals into one persist every 500ms.
//
// Rationale: 500ms is an empirical value — a chapter spans one or two LLM turns, so one or two
// persists are perfectly acceptable; and even if a manual ctrl+C never reaches the timer, the
// context-cancellation path flushes one last time. A genuine crash (OS kill -9) loses at most
// the last 0.5s of accumulation — the upstream session jsonl remains the complete record, and
// the next start replays from sessions/ to make up the difference.
func (t *UsageTracker) autoSaveLoop(ctx context.Context) {
	const debounce = 500 * time.Millisecond
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	var pending bool
	flush := func() {
		if err := t.SaveNow(); err != nil {
			slog.Warn("ghi usage xuống đĩa thất bại", "module", "usage", "err", err)
		}
		pending = false
	}
	for {
		select {
		case <-ctx.Done():
			if pending {
				flush()
			}
			return
		case <-t.saveCh:
			if pending {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			timer.Reset(debounce)
			pending = true
		case <-timer.C:
			flush()
		}
	}
}

// applyState writes a persisted snapshot back into memory. It is called only at startup
// (after LoadFromStore / replay), when autoSaveLoop has not started and Record cannot fire
// concurrently, so the lock is unnecessary; mu is kept anyway in case tests or a future call
// order introduce concurrency.
func (t *UsageTracker) applyState(state domain.UsageState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.overall = totalsFromState(state.Overall)
	if state.PerAgent == nil {
		t.perAgent = make(map[string]*agentTotals, 4)
	} else {
		t.perAgent = make(map[string]*agentTotals, len(state.PerAgent))
		for role, v := range state.PerAgent {
			tot := totalsFromState(v)
			t.perAgent[role] = &tot
		}
	}
	if state.PerModel == nil {
		t.perModel = make(map[string]*agentTotals, 4)
	} else {
		t.perModel = make(map[string]*agentTotals, len(state.PerModel))
		for model, v := range state.PerModel {
			tot := totalsFromState(v)
			t.perModel[model] = &tot
		}
	}
	t.missingAssistantUsage = state.MissingUsage
}

// totalsSnapshot copies in-memory agentTotals into a persistable domain.AgentUsageTotals.
// The samples ring buffer is deliberately left out — see the UsageState comment.
func totalsSnapshot(t *agentTotals) domain.AgentUsageTotals {
	if t == nil {
		return domain.AgentUsageTotals{}
	}
	return domain.AgentUsageTotals{
		Input:        t.Input,
		Output:       t.Output,
		CacheRead:    t.CacheRead,
		CacheWrite:   t.CacheWrite,
		Cost:         t.Cost,
		Saved:        t.Saved,
		CacheCapable: t.CacheCapable,
		CacheBreaks:  t.CacheBreaks,
	}
}

// totalsFromState restores a persisted shape into in-memory agentTotals. samples stays empty
// and accumulates from zero after a restart, recovering the "recent-N hit rate" semantics
// within a few Record calls.
func totalsFromState(s domain.AgentUsageTotals) agentTotals {
	return agentTotals{
		Input:        s.Input,
		Output:       s.Output,
		CacheRead:    s.CacheRead,
		CacheWrite:   s.CacheWrite,
		Cost:         s.Cost,
		Saved:        s.Saved,
		CacheCapable: s.CacheCapable,
		CacheBreaks:  s.CacheBreaks,
	}
}

// AgentUsage is one agent's accumulated-usage snapshot, exposed to the UI.
type AgentUsage struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// PerAgent returns accumulated usage per role, ordered by descending CacheRead count; roles that never consumed a token are skipped.
func (t *UsageTracker) PerAgent() []AgentUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AgentUsage, 0, len(t.perAgent))
	for role, v := range t.perAgent {
		if v.Input == 0 && v.Output == 0 {
			continue
		}
		recentRead, recentInput := recentSums(v)
		out = append(out, AgentUsage{
			Role:            role,
			Input:           v.Input,
			Output:          v.Output,
			CacheRead:       v.CacheRead,
			CacheWrite:      v.CacheWrite,
			Cost:            v.Cost,
			Saved:           v.Saved,
			CacheCapable:    v.CacheCapable,
			RecentCacheRead: recentRead,
			RecentInput:     recentInput,
			RecentSamples:   len(v.samples),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CacheRead != out[j].CacheRead {
			return out[i].CacheRead > out[j].CacheRead
		}
		return out[i].Input > out[j].Input
	})
	return out
}

// PerModel returns accumulated usage per model, ordered by descending cost and then by descending input volume.
func (t *UsageTracker) PerModel() []AgentUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AgentUsage, 0, len(t.perModel))
	for model, v := range t.perModel {
		if v.Input == 0 && v.Output == 0 {
			continue
		}
		out = append(out, AgentUsage{
			Model:        model,
			Input:        v.Input,
			Output:       v.Output,
			CacheRead:    v.CacheRead,
			CacheWrite:   v.CacheWrite,
			Cost:         v.Cost,
			Saved:        v.Saved,
			CacheCapable: v.CacheCapable,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Input > out[j].Input
	})
	return out
}

// resolveCost returns this message's cost, saved and capable values together.
//   - cost: a registry hit multiplies across the four buckets; a miss falls back to the
//     provider-supplied cost.
//   - saved: above 0 only on a registry hit with CacheRead > 0 and InputCost > CacheReadCost.
//   - capable: a registry hit where the model's CacheReadCostPer1M > 0 means prompt caching is
//     known to be supported.
//
// modelName prefers the caller-supplied value (during replay, _meta.model from the session
// jsonl).
func (t *UsageTracker) resolveCost(modelName string, u agentcore.Usage) (cost, saved float64, capable bool) {
	if entry, ok := models.DefaultRegistry().Resolve(modelName); ok {
		c := computeCost(u, *entry)
		s := computeSaved(u, *entry)
		canCache := entry.CacheReadCostPer1M > 0
		if c > 0 {
			return c, s, canCache
		}
	}
	if u.Cost != nil {
		return u.Cost.Total, 0, false
	}
	return 0, 0, false
}

// agentRoleName normalises a subagent name to a role name.
// architect_short/mid/long all normalise to architect; anything else is returned unchanged.
func agentRoleName(agentName string) string {
	if strings.HasPrefix(agentName, "architect_") {
		return "architect"
	}
	return agentName
}

// computeCost computes one call's dollar cost from $/1M-token unit prices.
//
// Semantic premises, uniformly guaranteed by the litellm providers (see the Usage assembly
// points in anthropic.go / bedrock.go / openai.go / gemini.go / compat.go):
//
//	u.Input  = all input tokens, **including** CacheRead but excluding CacheWrite
//	u.Output = output tokens
//
// so nonCachedInput = u.Input - u.CacheRead holds for every provider. The fallback branch is
// kept so that a future provider returning dirty data cannot crash it.
func computeCost(u agentcore.Usage, e models.ModelEntry) float64 {
	nonCachedInput := u.Input - u.CacheRead
	if nonCachedInput < 0 {
		nonCachedInput = u.Input
	}
	c := 0.0
	c += float64(nonCachedInput) * e.InputCostPer1M / 1_000_000
	c += float64(u.Output) * e.OutputCostPer1M / 1_000_000
	c += float64(u.CacheRead) * e.CacheReadCostPer1M / 1_000_000
	c += float64(u.CacheWrite) * e.CacheWriteCostPer1M / 1_000_000
	return c
}

// computeSaved estimates the dollars a CacheRead hit saves against billing at the plain input
// price. Note that the CacheWrite premium is not offset — that is the necessary investment
// that paves the way for later hits, and the real return is recovered through subsequent
// CacheReads.
func computeSaved(u agentcore.Usage, e models.ModelEntry) float64 {
	if u.CacheRead <= 0 || e.InputCostPer1M <= 0 {
		return 0
	}
	delta := e.InputCostPer1M - e.CacheReadCostPer1M
	if delta <= 0 {
		return 0
	}
	return float64(u.CacheRead) * delta / 1_000_000
}
