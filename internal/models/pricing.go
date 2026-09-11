package models

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	openRouterURL = "https://openrouter.ai/api/v1/models"
	cacheTTL      = 24 * time.Hour
	fetchTimeout  = 10 * time.Second
	cacheFileName = "models-cache.json"
	// maxModelAgeDays stays consistent with gen_models.go: models older than this count as
	// obsolete and are dropped.
	maxModelAgeDays = 730
)

// providerMap normalises OpenRouter vendor prefixes into local provider names.
// Vendors not listed here are ignored, so unusable entries are never pulled in.
var providerMap = map[string]string{
	"anthropic":  "anthropic",
	"openai":     "openai",
	"google":     "gemini",
	"deepseek":   "deepseek",
	"qwen":       "qwen",
	"x-ai":       "grok",
	"z-ai":       "glm",
	"meta-llama": "meta-llama",
	"mistralai":  "mistral",
	"moonshotai": "moonshot",
	"xiaomi":     "mimo",
	"minimax":    "minimax",
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	ContextLength int                    `json:"context_length"`
	Created       int64                  `json:"created"`
	Pricing       *openRouterPricing     `json:"pricing"`
	TopProvider   *openRouterTopProvider `json:"top_provider"`
}

type openRouterPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read"`
	InputCacheWrite string `json:"input_cache_write"`
}

type openRouterTopProvider struct {
	MaxCompletionTokens int `json:"max_completion_tokens"`
}

type modelCache struct {
	FetchedAt time.Time    `json:"fetched_at"`
	Models    []ModelEntry `json:"models"`
}

// StartPricingRefresh launches a background goroutine to refresh model data.
// It first reads the disk cache (24h TTL) and, when that is stale or missing, fetches fresh
// data and writes it back. An empty cacheDir skips the disk cache but still attempts the
// network fetch.
func StartPricingRefresh(registry *ModelRegistry, cacheDir string) {
	go func() {
		models := loadCache(cacheDir)
		if models == nil {
			fetched, err := fetchModels()
			if err != nil {
				slog.Warn("làm mới metadata model thất bại", "module", "models", "err", err)
				return
			}
			models = fetched
			saveCache(models, cacheDir)
		}
		registry.MergeModels(models)
		slog.Info("metadata model đã sẵn sàng", "module", "models", "count", len(models))
	}()
}

func loadCache(cacheDir string) []ModelEntry {
	if cacheDir == "" {
		return nil
	}
	p := filepath.Join(cacheDir, cacheFileName)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var c modelCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if time.Since(c.FetchedAt) > cacheTTL {
		return nil
	}
	return c.Models
}

func saveCache(models []ModelEntry, cacheDir string) {
	if cacheDir == "" {
		return
	}
	p := filepath.Join(cacheDir, cacheFileName)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(modelCache{FetchedAt: time.Now(), Models: models})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(cacheDir, ".models-cache-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return
	}
	tmp.Close()
	if err := os.Rename(tmpPath, p); err != nil {
		os.Remove(tmpPath)
	}
}

func fetchModels() ([]ModelEntry, error) {
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(openRouterURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter API returned %d", resp.StatusCode)
	}

	var result openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	out := make([]ModelEntry, 0, len(result.Data))
	for _, m := range result.Data {
		if e, ok := convertModel(m); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func convertModel(m openRouterModel) (ModelEntry, bool) {
	parts := strings.SplitN(m.ID, "/", 2)
	if len(parts) != 2 {
		return ModelEntry{}, false
	}
	prov, ok := providerMap[parts[0]]
	if !ok {
		return ModelEntry{}, false
	}
	modelID := parts[1]
	// Ignore variant suffixes (such as :thinking / :free).
	if strings.Contains(modelID, ":") {
		return ModelEntry{}, false
	}
	if isStaleModel(m.Created) {
		return ModelEntry{}, false
	}

	entry := ModelEntry{
		Provider:      prov,
		ID:            modelID,
		Name:          cleanModelName(m.Name),
		ContextWindow: m.ContextLength,
	}
	if m.TopProvider != nil {
		entry.MaxTokens = m.TopProvider.MaxCompletionTokens
	}
	if m.Pricing != nil {
		entry.InputCostPer1M = tokenToMillion(m.Pricing.Prompt)
		entry.OutputCostPer1M = tokenToMillion(m.Pricing.Completion)
		entry.CacheReadCostPer1M = tokenToMillion(m.Pricing.InputCacheRead)
		entry.CacheWriteCostPer1M = tokenToMillion(m.Pricing.InputCacheWrite)
	}
	return entry, true
}

// isStaleModel filters obsolete models by maxModelAgeDays.
// Zero or a negative value counts as missing data and is dropped as an "old model".
func isStaleModel(created int64) bool {
	if created <= 0 {
		return true
	}
	age := time.Since(time.Unix(created, 0)).Hours() / 24
	return age > maxModelAgeDays
}

// tokenToMillion converts the "USD per token" price OpenRouter returns into "USD per 1M tokens".
func tokenToMillion(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return math.Round(v*1e6*1e6) / 1e6
}

func cleanModelName(name string) string {
	if _, after, ok := strings.Cut(name, ": "); ok {
		return after
	}
	return name
}
