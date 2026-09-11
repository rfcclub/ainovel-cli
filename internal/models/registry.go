// Package models provides an LLM model metadata registry (context window, output cap,
// pricing), sourced from the OpenRouter API with a compile-time baseline plus runtime
// refresh.
package models

//go:generate go run gen_models.go

import (
	"strings"
	"sync"
)

// ModelEntry describes one known LLM model.
type ModelEntry struct {
	Provider            string  `json:"provider"`               // Vendor name normalised by OpenRouter (anthropic/openai/gemini/...)
	ID                  string  `json:"id"`                     // Model ID (without the vendor prefix)
	Name                string  `json:"name"`                   // Display name
	ContextWindow       int     `json:"context_window"`         // Input window
	MaxTokens           int     `json:"max_tokens"`             // Per-call output cap
	InputCostPer1M      float64 `json:"input_cost_per_1m"`      // Input price (USD/1M tokens)
	OutputCostPer1M     float64 `json:"output_cost_per_1m"`     // Output price
	CacheReadCostPer1M  float64 `json:"cache_read_cost_per_1m"` // Cache-read price
	CacheWriteCostPer1M float64 `json:"cache_write_cost_per_1m"`
}

// ModelRegistry holds the known models and supports fuzzy resolution plus runtime merging.
type ModelRegistry struct {
	mu     sync.RWMutex
	models []ModelEntry
}

// NewModelRegistry returns a registry already loaded with the compile-time baseline.
func NewModelRegistry() *ModelRegistry {
	r := &ModelRegistry{}
	r.models = append(r.models, generatedModels...)
	return r
}

var (
	defaultRegistry     *ModelRegistry
	defaultRegistryOnce sync.Once
)

// DefaultRegistry returns the global registry (lazy-loaded, thread-safe).
// Calling StartPricingRefresh at startup refreshes pricing/window data in the background.
func DefaultRegistry() *ModelRegistry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewModelRegistry()
	})
	return defaultRegistry
}

// Resolve looks up an entry from a model identifier (which may be "provider/model", a full
// ID, or a partial name).
//
// Match order:
//  1. If it contains "/", look it up exactly as "provider/model".
//  2. Exact or date-suffixed match.
//  3. Substring match (ID or Name contains the pattern).
//
// When several match, the alias without a date suffix wins (e.g. claude-sonnet-4 beats
// claude-sonnet-4-20250514).
func (r *ModelRegistry) Resolve(pattern string) (*ModelEntry, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if idx := strings.Index(pattern, "/"); idx > 0 {
		prov := pattern[:idx]
		modelID := pattern[idx+1:]
		if entry, ok := lookupModelEntry(r.models, prov, modelID); ok {
			return &entry, true
		}
		// OpenRouter's vendor prefix (google/, x-ai/) does not necessarily equal the local
		// Provider name, so fall back to looking up by modelID alone, ensuring
		// "google/gemini-2.5-pro" hits the gemini entry.
		if entry, ok := lookupModelEntry(r.models, "", modelID); ok {
			return &entry, true
		}
	}

	if entry, ok := lookupModelEntry(r.models, "", pattern); ok {
		return &entry, true
	}

	lower := strings.ToLower(pattern)
	normalized := normalizeModelLookupID(pattern)
	var candidates []int
	for i := range r.models {
		if strings.Contains(normalizeModelLookupID(r.models[i].ID), normalized) ||
			strings.Contains(strings.ToLower(r.models[i].ID), lower) ||
			strings.Contains(strings.ToLower(r.models[i].Name), lower) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}

	best := candidates[0]
	for _, i := range candidates[1:] {
		if !hasDatedSuffix(r.models[i].ID) && hasDatedSuffix(r.models[best].ID) {
			best = i
		}
	}
	entry := r.models[best]
	return &entry, true
}

// ResolveContextWindow returns a model's context window, or 0 when nothing matches.
func (r *ModelRegistry) ResolveContextWindow(pattern string) int {
	if e, ok := r.Resolve(pattern); ok {
		return e.ContextWindow
	}
	return 0
}

// List returns every model (an optional filter; an empty string means all).
func (r *ModelRegistry) List(filter string) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if filter == "" {
		return append([]ModelEntry{}, r.models...)
	}
	lower := strings.ToLower(filter)
	normalized := normalizeModelLookupID(filter)
	var out []ModelEntry
	for _, m := range r.models {
		if strings.Contains(strings.ToLower(m.Provider), lower) ||
			strings.Contains(normalizeModelLookupID(m.ID), normalized) ||
			strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) {
			out = append(out, m)
		}
	}
	return out
}

// MergeModels merges case-insensitively by provider+id.
// Non-zero price/window/MaxTokens/Name override an existing entry; new entries are appended.
func (r *ModelRegistry) MergeModels(fetched []ModelEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := make(map[string]int, len(r.models))
	for i, m := range r.models {
		idx[strings.ToLower(m.Provider+"/"+m.ID)] = i
	}
	for _, f := range fetched {
		key := strings.ToLower(f.Provider + "/" + f.ID)
		if i, ok := idx[key]; ok {
			if f.InputCostPer1M > 0 || f.OutputCostPer1M > 0 {
				r.models[i].InputCostPer1M = f.InputCostPer1M
				r.models[i].OutputCostPer1M = f.OutputCostPer1M
				r.models[i].CacheReadCostPer1M = f.CacheReadCostPer1M
				r.models[i].CacheWriteCostPer1M = f.CacheWriteCostPer1M
			}
			if f.ContextWindow > 0 {
				r.models[i].ContextWindow = f.ContextWindow
			}
			if f.MaxTokens > 0 {
				r.models[i].MaxTokens = f.MaxTokens
			}
			if f.Name != "" {
				r.models[i].Name = f.Name
			}
		} else {
			r.models = append(r.models, f)
			idx[key] = len(r.models) - 1
		}
	}
}
