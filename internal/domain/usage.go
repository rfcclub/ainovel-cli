package domain

import "time"

// UsageSchemaVersion is the compatibility version of meta/usage.json.
// If AgentUsageTotals field semantics ever change, bump this; UsageStore.Load sees a
// different version and ignores it, triggering a replay rebuild.
const UsageSchemaVersion = 2

// UsageState is the persistable snapshot of accumulated token / cost usage.
// It is maintained in memory by UsageTracker and written to meta/usage.json on a debounce
// timer.
//
// Note: UsageTracker's internal sliding-window samples (the "hit rate over the last N calls")
// are **not persisted** — they only serve short-term UI diagnostics, and semantics recover on
// a process restart after a few rounds of re-accumulation. MissingAssistantUsage is persisted,
// since accumulating it across restarts is far more useful diagnostically.
type UsageState struct {
	Schema       int                         `json:"schema"`
	UpdatedAt    time.Time                   `json:"updated_at"`
	Overall      AgentUsageTotals            `json:"overall"`
	PerAgent     map[string]AgentUsageTotals `json:"per_agent"`
	PerModel     map[string]AgentUsageTotals `json:"per_model,omitempty"`
	MissingUsage int                         `json:"missing_assistant_usage"`
}

// AgentUsageTotals is the persistable form of the running totals for one role (or overall).
type AgentUsageTotals struct {
	Input        int     `json:"input"`
	Output       int     `json:"output"`
	CacheRead    int     `json:"cache_read"`
	CacheWrite   int     `json:"cache_write"`
	Cost         float64 `json:"cost_usd"`
	Saved        float64 `json:"saved_usd"`
	CacheCapable bool    `json:"cache_capable"`
	// CacheBreaks counts cache-chain breaks detected live (the prefix does not shrink while the
	// hit rate collapses). It accumulates only on the live path; session replay does not re-run
	// the detection.
	CacheBreaks int `json:"cache_breaks,omitempty"`
}
