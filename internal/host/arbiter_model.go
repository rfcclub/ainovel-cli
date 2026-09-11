package host

import (
	"context"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// usageTrackedModel wires usage tracking onto model calls: tokens/cost must reach the budget and usage
// systems, or the budget cap goes blind to spend and the UI's usage is wrong. The identity recorded is
// the agentName passed in — imports count as architect and adjudication as arbiter (UsageTracker bills
// unknown roles at the Default price).
type usageTrackedModel struct {
	inner     agentcore.ChatModel
	agentName string
	record    func(agentName, task string, msg agentcore.AgentMessage)
}

func newUsageTrackedModel(inner agentcore.ChatModel, agentName string, record func(string, string, agentcore.AgentMessage)) agentcore.ChatModel {
	if record == nil {
		return inner
	}
	tracked := &usageTrackedModel{inner: inner, agentName: agentName, record: record}
	if capabilities, ok := inner.(llm.CapabilityProvider); ok {
		return &capabilityUsageTrackedModel{usageTrackedModel: tracked, capabilities: capabilities}
	}
	return tracked
}

// capabilityUsageTrackedModel preserves the wrapped model's optional capability interfaces. The wrapper
// must not blur "does not support thinking" into "capability unknown", or the layer above would generate
// parameters the provider rejects.
type capabilityUsageTrackedModel struct {
	*usageTrackedModel
	capabilities llm.CapabilityProvider
}

func (m *capabilityUsageTrackedModel) Capabilities() llm.Capabilities {
	return m.capabilities.Capabilities()
}

// JSONSchemaOverride passes through the wrapped model's three-state json_schema config declaration; it
// returns nil ("not configured") when the inner model does not carry it, faking no capability.
func (m *capabilityUsageTrackedModel) JSONSchemaOverride() *bool {
	if o, ok := m.usageTrackedModel.inner.(interface{ JSONSchemaOverride() *bool }); ok {
		return o.JSONSchemaOverride()
	}
	return nil
}

func (m *capabilityUsageTrackedModel) StructuredOutputFacts() llmcontract.ModelFacts {
	if provider, ok := m.usageTrackedModel.inner.(interface {
		StructuredOutputFacts() llmcontract.ModelFacts
	}); ok {
		return provider.StructuredOutputFacts()
	}
	return llmcontract.ModelFacts{
		Capabilities:       m.Capabilities(),
		JSONSchemaOverride: m.JSONSchemaOverride(),
	}
}

func (m *usageTrackedModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	resp, err := m.inner.Generate(ctx, msgs, tools, opts...)
	if err == nil && resp != nil {
		m.record(m.agentName, "", resp.Message)
	}
	return resp, err
}

func (m *usageTrackedModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	// The Arbiter only uses Generate; the streaming path is passed through (if it ever streams, usage is recorded by the consumer).
	return m.inner.GenerateStream(ctx, msgs, tools, opts...)
}

func (m *usageTrackedModel) SupportsTools() bool { return m.inner.SupportsTools() }
