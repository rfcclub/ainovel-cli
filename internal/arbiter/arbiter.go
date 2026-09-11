// Package arbiter is the semantic decision layer: an LLM-as-function woken on demand.
//
// Two symmetric planes (docs/engine-arbiter.md §2):
//
//	deterministic plane:  flow.LoadState   -> flow.Route     -> Instruction
//	semantic plane:       arbiter.Collect* -> arbiter.Decide* -> XxxDecision
//
// Discipline: Collect centralises IO (reading every fact from the store); Decide does no IO
// beyond the model request managed by the shared executor, so historical facts can replay it
// offline; execution belongs to the Engine. Each scenario gets a function pair plus a dedicated
// Decision type, so an action that does not fit the scenario is unrepresentable in the type, and
// the remaining legality is rejected by each type's Validate — Arbiter output is as untrustworthy
// as any LLM output, and fact validation is the last gate.
package arbiter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// decideMaxTokens is the output cap for a single decision; the decision JSON is tiny, so the bulk
// is headroom for a reasoning model's thinking budget (the same reasoning as
// userrules.normalizeMaxTokens).
const decideMaxTokens = 8192

// decide hands the scenario contract and business validation to the shared structured executor.
// It performs no IO beyond the model call.
func decide[T any](ctx context.Context, model agentcore.ChatModel, contract llmcontract.Contract, systemPrompt, payload string, validate func(*T) error) (T, error) {
	out, err := llmcontract.Execute(ctx, model, llmcontract.Request[T]{
		Contract:     contract,
		SystemPrompt: systemPrompt,
		Payload:      payload,
		Options:      []agentcore.CallOption{agentcore.WithMaxTokens(decideMaxTokens)},
		Validate:     validate,
		Agent:        "arbiter",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("chọn giao thức phán định", "module", "arbiter",
					"contract", contract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider,
					"model", res.Model, "schema_fingerprint", contract.Fingerprint())
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("tự sửa đầu ra phán định", "module", "arbiter", "attempt", ev.Attempt,
					"layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return out, fmt.Errorf("arbiter: %w", err)
	}
	return out, nil
}

// DispatchOp is the dispatch action shared by every scenario.
type DispatchOp struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// workerNames are the legal dispatch targets (matching what agents.BuildWorkers registers). It is
// an ordered slice serving both as the schema enum (a fixed order keeps the fingerprint stable)
// and as the validation allowlist.
var workerNames = []string{"architect_long", "architect_short", "writer", "editor"}

func (d *DispatchOp) validate() error {
	if d == nil {
		return nil
	}
	if !slices.Contains(workerNames, d.Agent) {
		return fmt.Errorf("dispatch.agent không hợp lệ: %q", d.Agent)
	}
	if strings.TrimSpace(d.Task) == "" {
		return fmt.Errorf("dispatch.task không được để trống")
	}
	return nil
}

// dispatchSchema is DispatchOp's nullable schema slot: only actions that need a dispatch supply
// an object, everything else is null (strict mode requires every field, expressing optionality
// through null).
func dispatchSchema(desc string) map[string]any {
	return llmcontract.Nullable(schema.Object(
		schema.Property("agent", schema.Enum(desc, workerNames...)).Required(),
		schema.Property("task", schema.String("Mô tả nhiệm vụ đầy đủ giao cho worker đó")).Required(),
	))
}

// marshalPayload serialises the fact pack; a failure is a programming error and must be exposed —
// silently fabricating empty facts would make the model judge from false input.
func marshalPayload(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("arbiter: tuần tự hóa gói sự thật thất bại: %w", err)
	}
	return string(data), nil
}
