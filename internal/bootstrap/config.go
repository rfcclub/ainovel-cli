package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// DefaultContextWindow is the fallback window size when a model is not registered in the registry.
const DefaultContextWindow = 200000

// CompactRatio is the relative threshold that triggers context compaction: compact when
// tokens >= window * CompactRatio.
// 0.85 is an empirical value: it leaves 15% headroom for "the next prompt plus a large tool
// result" while still letting a large-window model compact proactively at 85%, rather than
// waiting to fill a nominal 1M window (where attention degrades).
//
// The compaction ratio is not exposed to user configuration; users configure only each model's real
// context_window.
const CompactRatio = 0.85

// MinCompactReserve is the lower bound of ReserveTokens. For a small-window model (such as a local
// 32k qwen3:8b) 0.15 works out to only 4800, while one commit_chapter tool response can take 5-8k
// and a chapter of prose 8-15k — producing "compacted and immediately over again". The 8000 floor
// guarantees half a round of buffer even in the worst case.
const MinCompactReserve = 8000

// CompactReserveTokens derives ReserveTokens from CompactRatio and applies the MinCompactReserve floor:
//
//	threshold = window - reserve = window * CompactRatio
//	reserve   = max(MinCompactReserve, window * (1 - CompactRatio))
//
// It feeds agentcore.context.Engine's EngineConfig.ReserveTokens.
func CompactReserveTokens(window int) int {
	if window <= 0 {
		return 0
	}
	reserve := window - int(float64(window)*CompactRatio)
	if reserve < MinCompactReserve {
		return MinCompactReserve
	}
	return reserve
}

// ProviderConfig defines the credentials of one LLM provider.
type ProviderConfig struct {
	Type    string        `json:"type,omitempty"`     // API protocol type (openai/anthropic/gemini); set it for custom proxies
	API     string        `json:"api,omitempty"`      // OpenAI protocol endpoint: chat (default) / responses
	APIKey  string        `json:"api_key,omitempty"`  // API Key
	BaseURL string        `json:"base_url,omitempty"` // API Base URL
	Models  []ModelConfig `json:"models,omitempty"`   // Optional model list shown when switching in the TUI
	// ExtraBody passes extra parameters on every request to this provider (such as temperature/top_p/
	// min_p/presence_penalty, or vendor-specific keys such as nvidia's chat_template_kwargs to enable
	// thinking).
	// An OpenAI-compatible endpoint merges it verbatim into the request body (the extra_body convention);
	// the value is the user's own responsibility.
	ExtraBody map[string]any `json:"extra_body,omitempty"`
	// Extra passes through to the provider-level config (litellm.ProviderConfig.Extra) for
	// client/transport options such as HTTP headers, user_agent and anthropic_beta.
	Extra map[string]any `json:"extra,omitempty"`
	// StreamIdleTimeout is the streaming idle watchdog: the stream is cut when no chunk arrives for
	// longer than this (a Go duration string such as "900s" / "15m"). Empty defaults to 5m, a
	// reasonable upper bound for cloud services; self-hosted slow inference such as LocalAI/ollama can
	// take far longer than 5 minutes for the first chunk, so loosen it per provider without dragging
	// down hang detection on the other channels (#79).
	StreamIdleTimeout string `json:"stream_idle_timeout,omitempty"`
}

// ModelConfig describes a switchable model under one provider and its optional context window.
// For backward compatibility it can be read from either a JSON string ("model-name") or an object;
// it is always normalised to the object form when written back.
type ModelConfig struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
	// JSONSchema is the three-state declaration of native structured output (response_format
	// json_schema): unset = judge from the provider adapter per-model capability; true = the user
	// declares that this endpoint/model
	// supported (a rejected request is exposed as-is, never silently degraded); false = force the prompt contract.
	// The capability of a custom proxy or aggregation gateway is taken from the user declaration; the
	// program does not probe for it.
	JSONSchema *bool `json:"json_schema,omitempty"`
}

func (m *ModelConfig) UnmarshalJSON(data []byte) error {
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		m.Name = legacy
		m.ContextWindow = 0
		m.JSONSchema = nil
		return nil
	}
	type modelConfigAlias ModelConfig
	var decoded modelConfigAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("model config must be a string or object: %w", err)
	}
	*m = ModelConfig(decoded)
	return nil
}

// ModelConfig returns the explicit configuration of the named model.
func (pc ProviderConfig) ModelConfig(name string) (ModelConfig, bool) {
	name = strings.TrimSpace(name)
	for _, model := range pc.Models {
		if strings.TrimSpace(model.Name) == name {
			return model, true
		}
	}
	return ModelConfig{}, false
}

// ModelJSONSchema returns the model's three-state json_schema declaration; when it is absent from
// models or unconfigured it returns nil (judge from the adapter capability).
func (c Config) ModelJSONSchema(provider, model string) *bool {
	if pc, ok := c.Providers[provider]; ok {
		if mc, ok := pc.ModelConfig(model); ok {
			return mc.JSONSchema
		}
	}
	return nil
}

// defaultStreamIdleTimeout: with long output and a long context, a reasoning-aware provider
// (mimo / deepseek-r1 and the like) can leave the SSE stream silent for the whole thinking phase
// when the server does not stream reasoning deltas. litellm's default watchdog is 2 minutes, which
// frequently kills an 8000-character writing chapter by mistake; 5 minutes covers the vast majority
// of measured cases (see the plan -> draft thinking-time statistics in tasks/todo.md).
const defaultStreamIdleTimeout = 5 * time.Minute

// StreamIdleTimeoutValue resolves this provider's streaming idle timeout, falling back to the default when empty.
func (pc ProviderConfig) StreamIdleTimeoutValue() (time.Duration, error) {
	s := strings.TrimSpace(pc.StreamIdleTimeout)
	if s == "" {
		return defaultStreamIdleTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use Go duration like \"900s\" / \"15m\")", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %q", s)
	}
	return d, nil
}

// RequiresAPIKey reports whether this provider must have an explicit api_key.
// Conventions:
// 1. ollama / bedrock may run without a key.
// 2. A configuration with an explicit Type counts as a custom proxy and may run without a key.
// 3. Every other provider requires a key, keeping conservative validation for official hosted APIs.
func (pc ProviderConfig) RequiresAPIKey(name string) bool {
	switch name {
	case "ollama", "bedrock":
		return false
	}
	return pc.Type == ""
}

// ProviderType returns the effective API protocol type.
// An explicit Type wins; otherwise the provider name itself must already be in litellm's registry.
func (pc ProviderConfig) ProviderType(name string) (string, error) {
	if pc.Type != "" {
		return pc.Type, nil
	}
	if llm.IsProviderRegistered(name) {
		return name, nil
	}
	return "", fmt.Errorf("provider %q thiếu type, và không nằm trong danh sách provider đã biết của litellm: %w", name, errs.ErrConfig)
}

// ModelRef is a provider/model pair.
type ModelRef struct {
	Provider string `json:"provider"` // Provider name (a key in the Providers map)
	Model    string `json:"model"`    // Model name (passed through verbatim, never parsed)
}

// RoleConfig defines the model override for one role.
type RoleConfig struct {
	Provider  string     `json:"provider"`            // Primary provider name (a key in the Providers map)
	Model     string     `json:"model"`               // Primary model name (passed through verbatim, never parsed)
	Fallbacks []ModelRef `json:"fallbacks,omitempty"` // Explicit fallback provider/model list
	// ReasoningEffort is this role's reasoning effort (off/low/medium/high/xhigh/max); empty = inherit the top-level default.
	// It is validated by agents.ParseThinkingLevel before use; an out-of-range value counts as empty.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// knownRoles lists the configurable role names. The Arbiter currently exposes no role-level
// configuration and always uses the top-level default model (host.arbiterModel uses models.Default).
// import_* are the model-tier knobs for the import semantic functions
// (docs/import-pipeline.md §13.1): unset they land on architect, and setting them lets the more
// mechanical functions point at a cheaper tier.
var knownRoles = map[string]bool{
	"architect":         true,
	"writer":            true,
	"editor":            true,
	"import_segment":    true,
	"import_analyze":    true,
	"import_synthesize": true,
}

// Config is the application configuration.
type Config struct {
	// Runtime fields (not serialised to JSON)
	OutputDir string `json:"-"` // Output root directory

	// Default LLM configuration
	Provider  string `json:"provider"` // Default provider (a key in the Providers map)
	ModelName string `json:"model"`    // Default model name
	// ReasoningEffort is the top-level default reasoning effort (off/low/medium/high/xhigh/max);
	// empty = do not override (keep the model/provider default).
	// A role without its own reasoning_effort falls back to this value.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// Provider credential store
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// Role-level model overrides
	Roles map[string]RoleConfig `json:"roles,omitempty"`

	// Writing parameters
	Style string `json:"style,omitempty"`
	// ContentRating khai báo mức nội dung được phép miêu tả: "general" (mặc định, hướng
	// phổ thông), "mature" (chủ đề người lớn, miêu tả có chừng mực) hoặc "explicit"
	// (được miêu tả thẳng, gồm cả cảnh thân mật/bạo lực chi tiết). Đây là khai báo của
	// người lớn tự chịu trách nhiệm về tác phẩm của mình — hệ thống chỉ ghi lại mức đó
	// vào system prompt để model không tự đoán, không kiểm duyệt thay người dùng.
	ContentRating string `json:"content_rating,omitempty"`

	// ContextWindow là cửa sổ ngữ cảnh toàn cục phiên bản cũ, giữ lại để tương thích.
	ContextWindow int `json:"context_window,omitempty"`

	// Budget is the cost-budget policy for one book; enabled only when book_usd > 0.
	Budget BudgetConfig `json:"budget,omitzero"`

	// Notify is the unattended alerting configuration; enabled by default (with the system channel as fallback).
	Notify NotifyConfig `json:"notify,omitzero"`
}

// BudgetConfig is the user's policy declaration for one book's wallet. Stopping at the line is
// equivalent to the user manually Aborting at that moment — the Host only executes on their behalf
// and does not evaluate model behaviour (architecture §10, the constitutional boundary).
type BudgetConfig struct {
	BookUSD   float64 `json:"book_usd,omitempty"`   // Required to enable; 0/omitted = unlimited
	WarnRatio float64 `json:"warn_ratio,omitempty"` // Warning watermark, defaults to 0.8
	HardStop  bool    `json:"hard_stop,omitempty"`  // true = stop immediately when crossed; default waits for the current subagent task
}

// Enabled reports whether the budget policy is enabled.
func (b BudgetConfig) Enabled() bool { return b.BookUSD > 0 }

// NotifyConfig configures the unattended alerting channel.
type NotifyConfig struct {
	Enabled *bool    `json:"enabled,omitempty"` // Defaults to true (the system channel works with no configuration)
	Command string   `json:"command,omitempty"` // Optional; when set it replaces the system channel (phone push goes here)
	Events  []string `json:"events,omitempty"`  // Optional; filtered by notify.Kinds, all enabled by default
}

// IsEnabled reports whether alerting is enabled (default true).
func (n NotifyConfig) IsEnabled() bool { return n.Enabled == nil || *n.Enabled }

// ValidateBase validates the base configuration.
func (c *Config) ValidateBase() error {
	if err := validateConfigText("provider", c.Provider); err != nil {
		return err
	}
	if err := validateConfigText("model", c.ModelName); err != nil {
		return err
	}

	if c.Provider == "" {
		return fmt.Errorf("provider is required: %w", errs.ErrConfig)
	}
	if c.ModelName == "" {
		return fmt.Errorf("model is required: %w", errs.ErrConfig)
	}

	// The default provider must have credentials.
	pc, ok := c.Providers[c.Provider]
	if !ok {
		return fmt.Errorf("provider %q chưa được cấu hình thông tin xác thực trong providers; nếu đã ghi đè provider trong ./.ainovel/config.json thì phải khai báo đồng thời providers.%s (gồm api_key/base_url), không được chỉ đổi provider ở tầng trên cùng: %w", c.Provider, c.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(c.Provider) && pc.APIKey == "" {
		return fmt.Errorf("provider %q has no api_key configured: %w", c.Provider, errs.ErrConfig)
	}
	if err := validateProviderConfigText(c.Provider, pc); err != nil {
		return err
	}
	if err := c.validateProviderAPI("default", c.Provider, pc); err != nil {
		return err
	}
	for name, provider := range c.Providers {
		if err := validateConfigText("provider name", name); err != nil {
			return err
		}
		if err := validateProviderConfigText(name, provider); err != nil {
			return err
		}
		if err := c.validateProviderAPI(fmt.Sprintf("provider %q", name), name, provider); err != nil {
			return err
		}
	}

	// Validate role overrides.
	for role, rc := range c.Roles {
		if err := validateConfigText("role name", role); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q provider", role), rc.Provider); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q model", role), rc.Model); err != nil {
			return err
		}
		if !knownRoles[role] {
			return fmt.Errorf("unknown role %q in roles config (valid: architect/writer/editor/import_segment/import_analyze/import_synthesize): %w", role, errs.ErrConfig)
		}
		if rc.Provider == "" || rc.Model == "" {
			return fmt.Errorf("role %q must have both provider and model: %w", role, errs.ErrConfig)
		}
		if err := c.validateModelRef(
			fmt.Sprintf("role %q", role),
			ModelRef{Provider: rc.Provider, Model: rc.Model},
		); err != nil {
			return err
		}
		for i, fallback := range rc.Fallbacks {
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] provider", role, i), fallback.Provider); err != nil {
				return err
			}
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] model", role, i), fallback.Model); err != nil {
				return err
			}
			if err := c.validateModelRef(
				fmt.Sprintf("role %q fallback[%d]", role, i),
				fallback,
			); err != nil {
				return err
			}
		}
	}

	// Validate the budget policy.
	if c.Budget.BookUSD < 0 {
		return fmt.Errorf("budget.book_usd must be >= 0: %w", errs.ErrConfig)
	}
	if c.Budget.Enabled() && (c.Budget.WarnRatio <= 0 || c.Budget.WarnRatio >= 1) {
		return fmt.Errorf("budget.warn_ratio must be in (0, 1): %w", errs.ErrConfig)
	}

	// Validate the alerting configuration.
	if err := validateConfigText("notify.command", c.Notify.Command); err != nil {
		return err
	}
	for _, ev := range c.Notify.Events {
		if !notify.IsKnownKind(ev) {
			return fmt.Errorf("unknown notify event %q (valid: %s): %w", ev, strings.Join(notify.Kinds(), "/"), errs.ErrConfig)
		}
	}

	return nil
}

func validateProviderConfigText(name string, pc ProviderConfig) error {
	fields := []struct {
		label string
		value string
	}{
		{label: fmt.Sprintf("provider %q type", name), value: pc.Type},
		{label: fmt.Sprintf("provider %q api", name), value: pc.API},
		{label: fmt.Sprintf("provider %q api_key", name), value: pc.APIKey},
		{label: fmt.Sprintf("provider %q base_url", name), value: pc.BaseURL},
	}
	for _, field := range fields {
		if err := validateConfigText(field.label, field.value); err != nil {
			return err
		}
	}
	seenModels := make(map[string]bool, len(pc.Models))
	for i, model := range pc.Models {
		modelName := strings.TrimSpace(model.Name)
		if err := validateConfigText(fmt.Sprintf("provider %q models[%d].name", name, i), model.Name); err != nil {
			return err
		}
		if modelName == "" {
			return fmt.Errorf("provider %q models[%d].name is required: %w", name, i, errs.ErrConfig)
		}
		if seenModels[modelName] {
			return fmt.Errorf("provider %q has duplicate model %q: %w", name, modelName, errs.ErrConfig)
		}
		seenModels[modelName] = true
		if model.ContextWindow < 0 {
			return fmt.Errorf("provider %q model %q context_window must be >= 0: %w", name, modelName, errs.ErrConfig)
		}
	}
	switch pc.API {
	case "", "chat", "responses":
	default:
		return fmt.Errorf("provider %q api must be chat or responses: %w", name, errs.ErrConfig)
	}
	if _, err := pc.StreamIdleTimeoutValue(); err != nil {
		return fmt.Errorf("provider %q stream_idle_timeout: %w: %w", name, err, errs.ErrConfig)
	}
	return nil
}

func validateConfigText(name, value string) error {
	if utils.ContainsControl(value) {
		return fmt.Errorf("%s contains control character: %w", name, errs.ErrConfig)
	}
	return nil
}

// DefaultProviderConfig returns the credential configuration of the default provider.
func (c *Config) DefaultProviderConfig() ProviderConfig {
	if c.Providers == nil {
		return ProviderConfig{}
	}
	return c.Providers[c.Provider]
}

// FillDefaults fills in default values.
func (c *Config) FillDefaults() {
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join("output", "novel")
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleConfig)
	}
	if c.Style == "" {
		c.Style = "default"
	}
	c.ContentRating = NormalizeContentRating(c.ContentRating)
	if c.Budget.Enabled() && c.Budget.WarnRatio == 0 {
		c.Budget.WarnRatio = 0.8
	}
}

// Content rating levels. The default is deliberately the most conservative one: a
// config that never mentions the field must not silently unlock adult output.
const (
	ContentRatingGeneral  = "general"
	ContentRatingMature   = "mature"
	ContentRatingExplicit = "explicit"
)

// NormalizeContentRating canonicalises the configured content rating. An empty or
// unrecognised value falls back to general; synonyms are accepted because users type
// "adult", "18+", "nguoi lon" and friends by hand.
func NormalizeContentRating(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "mature", "16+", "adult", "nguoi-lon", "nguoilon", "người lớn":
		return ContentRatingMature
	case "explicit", "18+", "nsfw", "uncensored", "khong-kiem-duyet", "không kiểm duyệt":
		return ContentRatingExplicit
	default:
		return ContentRatingGeneral
	}
}

// ContextWindowSource marks where the window value came from, for logs and diagnostics.
type ContextWindowSource string

const (
	CtxWindowModelConfig ContextWindowSource = "model_config" // Explicitly set on the provider model entry
	CtxWindowConfig      ContextWindowSource = "config"       // Explicitly set by the legacy top-level context_window
	CtxWindowRegistry    ContextWindowSource = "registry"     // Hit the OpenRouter baseline
	CtxWindowDefault     ContextWindowSource = "default"      // Fallback (custom proxy / unknown model)
)

// ResolveContextWindow resolves the effective window used for context compaction, by priority:
//  1. providers.<provider>.models[].context_window
//  2. The legacy top-level ContextWindow (backward compatibility).
//  3. Lookup by model name in models.DefaultRegistry (OpenRouter baseline + 24h refresh).
//  4. Fall back to DefaultContextWindow (custom proxy / unknown model).
//
// Note: the return value is used only for the compaction threshold; it never shrinks the length the
// LLM API can really accept.
func (c Config) ResolveContextWindow(provider, modelName string) (int, ContextWindowSource) {
	if pc, ok := c.Providers[strings.TrimSpace(provider)]; ok {
		if model, found := pc.ModelConfig(modelName); found && model.ContextWindow > 0 {
			return model.ContextWindow, CtxWindowModelConfig
		}
	}
	if c.ContextWindow > 0 {
		return c.ContextWindow, CtxWindowConfig
	}
	if rw := models.DefaultRegistry().ResolveContextWindow(modelName); rw > 0 {
		return rw, CtxWindowRegistry
	}
	return DefaultContextWindow, CtxWindowDefault
}

// ResolveReasoningEffort returns the raw reasoning-effort string in effect for a role
// (off/low/medium/high/xhigh/max or empty).
// Priority: role-level Roles[role].ReasoningEffort -> top-level default ReasoningEffort -> ""
// (no override, keeping the model/provider default). An empty or "default" role takes the
// top-level default directly. agents.ParseThinkingLevel guards value validity.
func (c Config) ResolveReasoningEffort(role string) string {
	if role != "" && role != "default" {
		if rc, ok := c.Roles[role]; ok && rc.ReasoningEffort != "" {
			return rc.ReasoningEffort
		}
	}
	return c.ReasoningEffort
}

// LogContextWindowChoice logs a role's window decision. When source=default it warns that the
// model did not hit the registry (nor is it in OpenRouter), so context compaction will trigger
// against the fallback window — if the model's real window is larger, set context_window explicitly
// in the configuration to avoid premature compaction and lost history.
func LogContextWindowChoice(role, model string, window int, source ContextWindowSource) {
	attrs := []any{"module", "context", "role", role, "model", model, "window", window, "source", source}
	switch source {
	case CtxWindowModelConfig:
		slog.Info("cửa sổ ngữ cảnh (từ cấu hình model của provider)", attrs...)
	case CtxWindowDefault:
		slog.Warn("model không nhận diện được, dùng cửa sổ dự phòng (có thể chỉ định rõ qua providers.<name>.models[].context_window)", attrs...)
	case CtxWindowConfig:
		slog.Info("cửa sổ ngữ cảnh (từ context_window trong file cấu hình)", attrs...)
	default:
		slog.Info("cửa sổ ngữ cảnh", attrs...)
	}
}

// CandidateModels returns the switchable models under one provider.
// It prefers the provider's explicitly declared models and also adds any model of that provider that
// has appeared in the current configuration.
func (c Config) CandidateModels(provider string) []string {
	if provider == "" {
		return nil
	}

	seen := make(map[string]bool)
	models := make([]string, 0, 4)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}

	if pc, ok := c.Providers[provider]; ok {
		for _, model := range pc.Models {
			add(model.Name)
		}
	}
	if c.Provider == provider {
		add(c.ModelName)
	}
	for _, rc := range c.Roles {
		if rc.Provider == provider {
			add(rc.Model)
		}
		for _, fallback := range rc.Fallbacks {
			if fallback.Provider == provider {
				add(fallback.Model)
			}
		}
	}
	return models
}

func (c Config) validateModelRef(owner string, ref ModelRef) error {
	if ref.Provider == "" || ref.Model == "" {
		return fmt.Errorf("%s must have both provider and model: %w", owner, errs.ErrConfig)
	}

	pc, ok := c.Providers[ref.Provider]
	if !ok {
		return fmt.Errorf("%s references provider %q which is not configured: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(ref.Provider) && pc.APIKey == "" {
		return fmt.Errorf("%s references provider %q which has no api_key: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if err := c.validateProviderAPI(owner, ref.Provider, pc); err != nil {
		return err
	}
	return nil
}

func (c Config) validateProviderAPI(owner, providerName string, pc ProviderConfig) error {
	if pc.API == "" {
		return nil
	}
	providerType, err := pc.ProviderType(providerName)
	if err != nil {
		return fmt.Errorf("cấu hình api của %s provider %q không phân giải được loại giao thức: %w", owner, providerName, err)
	}
	if strings.ToLower(strings.TrimSpace(providerType)) != "openai" {
		return fmt.Errorf("api của %s provider %q chỉ hỗ trợ provider theo giao thức OpenAI: %w", owner, providerName, errs.ErrConfig)
	}
	return nil
}
