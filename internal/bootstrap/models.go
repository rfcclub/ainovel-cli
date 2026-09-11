package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// FailoverEvent represents one explicit provider switch.
// Reason is a short label (rate_limit / timeout / stream_idle / network) for structured logs.
type FailoverEvent struct {
	Role         string
	Reason       string
	FromProvider string
	FromModel    string
	ToProvider   string
	ToModel      string
	Err          error
}

// FailoverReporter is called whenever an explicit switch happens.
type FailoverReporter func(FailoverEvent)

type modelTarget struct {
	provider   string
	name       string
	model      agentcore.ChatModel
	jsonSchema *bool
}

// SwappableModel is a hot-swappable ChatModel wrapper.
// Requests already in flight keep using the old instance; later requests switch to the new one
// automatically.
type SwappableModel struct {
	*agentcore.SwappableModel
	mu       sync.RWMutex
	provider string
	name     string
	// jsonSchema is the config json_schema three-state declaration of the currently selected model,
	// switched atomically under the same lock as provider/name; llmcontract.Resolve reads it live on
	// every call through the structural matching interface.
	jsonSchema *bool
}

func NewSwappableModel(provider, name string, model agentcore.ChatModel, jsonSchema *bool) *SwappableModel {
	return &SwappableModel{
		SwappableModel: agentcore.NewSwappableModel(model),
		provider:       provider,
		name:           name,
		jsonSchema:     jsonSchema,
	}
}

func (m *SwappableModel) ProviderName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider
}

func (m *SwappableModel) Info() llm.ModelInfo {
	return m.StructuredOutputFacts().Info
}

// StructuredOutputFacts reads the model instance, identity and config override under one lock, so a
// single structured protocol choice observes only one complete version.
func (m *SwappableModel) StructuredOutputFacts() llmcontract.ModelFacts {
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.SwappableModel.Current()
	facts := llmcontract.ModelFacts{
		Info:               llm.ModelInfo{Name: m.name, Provider: m.provider},
		JSONSchemaOverride: cloneBoolPtr(m.jsonSchema),
	}
	if cp, ok := current.(llm.CapabilityProvider); ok {
		facts.Capabilities = cp.Capabilities()
	}
	if info, ok := current.(interface{ Info() llm.ModelInfo }); ok {
		modelInfo := info.Info()
		if modelInfo.Name == "" {
			modelInfo.Name = m.name
		}
		if modelInfo.Provider == "" {
			modelInfo.Provider = m.provider
		}
		facts.Info = modelInfo
	}
	return facts
}

func (m *SwappableModel) Capabilities() llm.Capabilities {
	return m.StructuredOutputFacts().Capabilities
}

func (m *SwappableModel) Swap(provider, name string, model agentcore.ChatModel, jsonSchema *bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SwappableModel.Swap(model)
	m.provider = provider
	m.name = name
	m.jsonSchema = jsonSchema
}

// JSONSchemaOverride returns the config json_schema three-state declaration of the currently selected model.
func (m *SwappableModel) JSONSchemaOverride() *bool {
	return m.StructuredOutputFacts().JSONSchemaOverride
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (m *SwappableModel) Current() (provider, name string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider, m.name
}

// ModelSet holds the model instances assigned per role; an unconfigured role falls back to the default model.
type ModelSet struct {
	mu        sync.RWMutex
	Default   *SwappableModel
	models    map[string]*SwappableModel
	fallbacks map[string][]modelTarget
	config    Config
}

// ForRole returns the model for a role, falling back to the default when unconfigured.
func (ms *ModelSet) ForRole(role string) agentcore.ChatModel {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if m, ok := ms.models[role]; ok {
		return m
	}
	return ms.Default
}

// ForRoleWithFailover returns the role's model with a per-request fallback.
// It applies only when the role explicitly configures fallbacks; otherwise it degrades to a plain
// model.
func (ms *ModelSet) ForRoleWithFailover(role string, report FailoverReporter) agentcore.ChatModel {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	primary, ok := ms.models[role]
	if !ok {
		return ms.Default
	}
	targets := ms.fallbacks[role]
	if len(targets) == 0 {
		return primary
	}
	return &failoverModel{
		role: role, primary: primary, set: ms, report: report,
	}
}

// Summary returns a model-assignment summary (for logs).
func (ms *ModelSet) Summary() string {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var parts []string
	for role, m := range ms.models {
		provider, name := m.Current()
		parts = append(parts, fmt.Sprintf("%s=%s/%s", role, provider, name))
	}
	if len(parts) == 0 {
		provider, name := ms.Default.Current()
		return fmt.Sprintf("default=%s/%s", provider, name)
	}
	provider, name := ms.Default.Current()
	return fmt.Sprintf("default=%s/%s %s", provider, name, strings.Join(parts, " "))
}

// CurrentSelection returns the provider/model currently in effect for a role.
// An empty or "default" role returns the default model.
func (ms *ModelSet) CurrentSelection(role string) (provider, model string, explicit bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if role == "" || role == "default" {
		provider, model = ms.Default.Current()
		return provider, model, true
	}
	if sw, ok := ms.models[role]; ok {
		provider, model = sw.Current()
		return provider, model, true
	}
	provider, model = ms.Default.Current()
	return provider, model, false
}

// Swap switches the default model or a specific role's model.
// An empty or "default" role switches the default model; any other role switches to an explicit
// override.
func (ms *ModelSet) Swap(role, provider, model string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	pc, ok := ms.config.Providers[provider]
	if !ok {
		return fmt.Errorf("provider %q is not configured: %w", provider, errs.ErrConfig)
	}
	next, err := createModelFromConfig(provider, model, pc, make(map[string]agentcore.ChatModel))
	if err != nil {
		return fmt.Errorf("chuyển model thất bại: %w", err)
	}

	jsonSchema := ms.config.ModelJSONSchema(provider, model)
	if role == "" || role == "default" {
		ms.Default.Swap(provider, model, next, jsonSchema)
		ms.config.Provider = provider
		ms.config.ModelName = model
		return nil
	}

	if !knownRoles[role] {
		return fmt.Errorf("unknown role %q: %w", role, errs.ErrConfig)
	}

	if existing, ok := ms.models[role]; ok {
		existing.Swap(provider, model, next, jsonSchema)
	} else {
		ms.models[role] = NewSwappableModel(provider, model, next, jsonSchema)
	}
	if ms.config.Roles == nil {
		ms.config.Roles = make(map[string]RoleConfig)
	}
	rc := ms.config.Roles[role]
	rc.Provider = provider
	rc.Model = model
	ms.config.Roles[role] = rc
	return nil
}

// ResolveContextWindow resolves the window from ModelSet's latest configuration, for a
// ContextManagerFactory after a runtime hot swap, avoiding a captured copy of the startup Config.
func (ms *ModelSet) ResolveContextWindow(provider, model string) (int, ContextWindowSource) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.config.ResolveContextWindow(provider, model)
}

// ApplyPrepared commits a successfully built candidate ModelSet. The addresses of existing
// SwappableModels stay unchanged, so assembled Workers/Arbiters pick up the new client
// automatically on their next request.
func (ms *ModelSet) ApplyPrepared(candidate *ModelSet) {
	if candidate == nil {
		return
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()

	defaultProvider, defaultName := candidate.Default.Current()
	ms.Default.Swap(defaultProvider, defaultName, candidate.Default.SwappableModel.Current(), candidate.Default.JSONSchemaOverride())

	nextModels := make(map[string]*SwappableModel, len(candidate.models))
	for role, next := range candidate.models {
		provider, name := next.Current()
		if existing, ok := ms.models[role]; ok {
			existing.Swap(provider, name, next.SwappableModel.Current(), next.JSONSchemaOverride())
			nextModels[role] = existing
		} else {
			nextModels[role] = next
		}
	}
	ms.models = nextModels
	ms.fallbacks = candidate.fallbacks
	ms.config = CloneConfig(candidate.config)
}

func (ms *ModelSet) fallbackTargets(role string) []modelTarget {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return append([]modelTarget(nil), ms.fallbacks[role]...)
}

// ModelName extracts the current model name from a ChatModel, returning an empty string on failure.
// It supports SwappableModel hot swaps: the call always returns the latest value.
func ModelName(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Name
	}
	return ""
}

// ModelProvider extracts the current provider name from a ChatModel, returning an empty string on
// failure.
func ModelProvider(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Provider
	}
	if provider, ok := m.(interface{ ProviderName() string }); ok {
		return provider.ProviderName()
	}
	return ""
}

// NewModelSet builds the multi-model set from the configuration.
// The same provider+model pair reuses one instance.
func NewModelSet(cfg Config) (*ModelSet, error) {
	cache := make(map[string]agentcore.ChatModel)

	// Create the default model.
	defaultPC := cfg.DefaultProviderConfig()
	defaultModel, err := createModelFromConfig(cfg.Provider, cfg.ModelName, defaultPC, cache)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}

	ms := &ModelSet{
		Default:   NewSwappableModel(cfg.Provider, cfg.ModelName, defaultModel, cfg.ModelJSONSchema(cfg.Provider, cfg.ModelName)),
		models:    make(map[string]*SwappableModel),
		fallbacks: make(map[string][]modelTarget),
		config:    cfg,
	}

	// Create the role override models.
	for role, rc := range cfg.Roles {
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return nil, fmt.Errorf("role %s references unknown provider %q: %w", role, rc.Provider, errs.ErrConfig)
		}
		m, err := createModelFromConfig(rc.Provider, rc.Model, pc, cache)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		ms.models[role] = NewSwappableModel(rc.Provider, rc.Model, m, cfg.ModelJSONSchema(rc.Provider, rc.Model))
		slog.Info("phân bổ model theo vai trò", "module", "config", "role", role, "provider", rc.Provider, "model", rc.Model)
		if len(rc.Fallbacks) == 0 {
			continue
		}

		targets := make([]modelTarget, 0, len(rc.Fallbacks))
		for _, fallback := range rc.Fallbacks {
			fpc, ok := cfg.Providers[fallback.Provider]
			if !ok {
				return nil, fmt.Errorf("role %s fallback references unknown provider %q: %w", role, fallback.Provider, errs.ErrConfig)
			}
			fm, err := createModelFromConfig(fallback.Provider, fallback.Model, fpc, cache)
			if err != nil {
				return nil, fmt.Errorf("role %s fallback %s/%s: %w", role, fallback.Provider, fallback.Model, err)
			}
			targets = append(targets, modelTarget{
				provider:   fallback.Provider,
				name:       fallback.Model,
				model:      fm,
				jsonSchema: cfg.ModelJSONSchema(fallback.Provider, fallback.Model),
			})
		}
		ms.fallbacks[role] = targets
	}

	return ms, nil
}

// createModelFromConfig creates or reuses a ChatModel instance.
func createModelFromConfig(providerKey, model string, pc ProviderConfig, cache map[string]agentcore.ChatModel) (agentcore.ChatModel, error) {
	cacheKey := providerKey + "|" + model
	if m, ok := cache[cacheKey]; ok {
		return m, nil
	}

	providerType, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, fmt.Errorf("phân tích loại provider thất bại: %w", err)
	}
	providerExtra := cloneMap(pc.Extra)
	if pc.API != "" {
		if providerExtra == nil {
			providerExtra = make(map[string]any, 1)
		}
		providerExtra["api"] = pc.API
	}

	streamIdle, err := pc.StreamIdleTimeoutValue()
	if err != nil {
		return nil, fmt.Errorf("provider %s stream_idle_timeout: %w: %w", providerKey, errs.ErrConfig, err)
	}

	m, err := llm.NewModel(providerType, model,
		llm.WithAPIKey(pc.APIKey),
		llm.WithBaseURL(pc.BaseURL),
		llm.WithStreamIdleTimeout(streamIdle),
		llm.WithProviderExtra(providerExtra),
		llm.WithExtra(pc.ExtraBody),
	)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w: %w", providerKey, providerType, errs.ErrProvider, err)
	}
	cache[cacheKey] = m
	return m, nil
}

type failoverModel struct {
	role    string
	primary *SwappableModel
	set     *ModelSet
	report  FailoverReporter
}

func (m *failoverModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	current := m.currentTarget()
	resp, err := current.model.Generate(ctx, messages, tools, opts...)
	if err == nil {
		return resp, nil
	}

	next, reason, ok := m.pickFallback(current, err, requestsJSONSchema(opts))
	if !ok {
		return nil, err
	}
	m.reportFailover(current, next, reason, err)
	return next.model.Generate(ctx, messages, tools, opts...)
}

func (m *failoverModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	out := make(chan agentcore.StreamEvent, 100)

	go func() {
		defer close(out)

		current := m.currentTarget()
		fallbackUsed := false

	retry:
		source, resp, err := m.startAttempt(ctx, current, messages, tools, opts...)
		if err != nil {
			if !fallbackUsed {
				if next, reason, ok := m.pickFallback(current, err, requestsJSONSchema(opts)); ok {
					fallbackUsed = true
					m.reportFailover(current, next, reason, err)
					current = next
					goto retry
				}
			}
			out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}
		if resp != nil {
			out <- agentcore.StreamEvent{
				Type:       agentcore.StreamEventDone,
				Message:    resp.Message,
				StopReason: resp.Message.StopReason,
			}
			return
		}

		forwarded := false
		for ev := range source {
			switch ev.Type {
			case agentcore.StreamEventError:
				if ev.Err != nil && !forwarded && !fallbackUsed {
					if next, reason, ok := m.pickFallback(current, ev.Err, requestsJSONSchema(opts)); ok {
						fallbackUsed = true
						m.reportFailover(current, next, reason, ev.Err)
						current = next
						goto retry
					}
				}
				out <- ev
				return
			case agentcore.StreamEventDone:
				out <- ev
				return
			default:
				forwarded = true
				out <- ev
			}
		}
	}()

	return out, nil
}

func (m *failoverModel) SupportsTools() bool {
	return m.primary != nil && m.primary.SupportsTools()
}

func (m *failoverModel) ProviderName() string {
	if m.primary == nil {
		return ""
	}
	return m.primary.ProviderName()
}

func (m *failoverModel) Info() llm.ModelInfo {
	if m.primary == nil {
		return llm.ModelInfo{}
	}
	return m.primary.Info()
}

func (m *failoverModel) Capabilities() llm.Capabilities {
	return m.StructuredOutputFacts().Capabilities
}

func (m *failoverModel) JSONSchemaOverride() *bool {
	return m.StructuredOutputFacts().JSONSchemaOverride
}

func (m *failoverModel) StructuredOutputFacts() llmcontract.ModelFacts {
	if m.primary == nil {
		return llmcontract.ModelFacts{}
	}
	return m.primary.StructuredOutputFacts()
}

func (m *failoverModel) currentTarget() modelTarget {
	if m.primary == nil {
		return modelTarget{}
	}
	provider, name := m.primary.Current()
	return modelTarget{
		provider:   provider,
		name:       name,
		model:      m.primary,
		jsonSchema: m.primary.JSONSchemaOverride(),
	}
}

func (m *failoverModel) pickFallback(current modelTarget, err error, requireJSONSchema bool) (modelTarget, string, bool) {
	if err == nil || current.model == nil {
		return modelTarget{}, "", false
	}
	if errors.Is(err, context.Canceled) {
		return modelTarget{}, "", false
	}

	if !agentcore.IsFailoverEligible(err) {
		return modelTarget{}, agentcore.FailoverReason(err), false
	}
	reason := agentcore.FailoverReason(err)
	var targets []modelTarget
	if m.set != nil {
		targets = m.set.fallbackTargets(m.role)
	}
	for _, target := range targets {
		if target.provider == current.provider && target.name == current.name {
			continue
		}
		if target.model == nil {
			continue
		}
		if requireJSONSchema && !supportsJSONSchema(target) {
			continue
		}
		return target, reason, true
	}
	return modelTarget{}, reason, false
}

func requestsJSONSchema(opts []agentcore.CallOption) bool {
	format := agentcore.ResolveCallConfig(opts).ResponseFormat
	return format != nil && format.Type == agentcore.ResponseFormatJSONSchema
}

func supportsJSONSchema(target modelTarget) bool {
	if target.jsonSchema != nil {
		return *target.jsonSchema
	}
	cp, ok := target.model.(llm.CapabilityProvider)
	return ok && cp.Capabilities().Structured.JSONSchema == llm.SupportYes
}

func (m *failoverModel) reportFailover(from, to modelTarget, reason string, err error) {
	if m.report != nil {
		m.report(FailoverEvent{
			Role:         m.role,
			Reason:       reason,
			FromProvider: from.provider,
			FromModel:    from.name,
			ToProvider:   to.provider,
			ToModel:      to.name,
			Err:          err,
		})
	}
}

func (m *failoverModel) startAttempt(ctx context.Context, target modelTarget, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, *agentcore.LLMResponse, error) {
	if target.model == nil {
		return nil, nil, fmt.Errorf("no model configured")
	}

	streamCh, err := target.model.GenerateStream(ctx, messages, tools, opts...)
	if err == nil {
		return streamCh, nil, nil
	}

	resp, genErr := target.model.Generate(ctx, messages, tools, opts...)
	if genErr != nil {
		return nil, nil, genErr
	}
	return nil, resp, nil
}
