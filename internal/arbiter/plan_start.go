package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// PlanStartDecision is the plan-start decision: choose a planner and produce the (possibly expanded)
// task text.
type PlanStartDecision struct {
	Planner string `json:"planner"` // architect_long | architect_short
	Task    string `json:"task"`    // Full task handed to the planner (including the expanded requirement)
	Reason  string `json:"reason"`
}

func (d *PlanStartDecision) Validate() error {
	if d.Planner != "architect_long" && d.Planner != "architect_short" {
		return fmt.Errorf("planner không hợp lệ: %q (chọn architect_long / architect_short)", d.Planner)
	}
	if strings.TrimSpace(d.Task) == "" {
		return fmt.Errorf("task không được để trống")
	}
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason không được để trống")
	}
	return nil
}

// planStartContract sits next to PlanStartDecision: every field required, planner a closed enum.
var planStartContract = llmcontract.Contract{
	Name:        "arbiter_plan_start",
	Description: "Phán định khởi động: chọn kiến trúc sư và sinh văn bản nhiệm vụ đầy đủ",
	Schema: schema.Object(
		schema.Property("planner", schema.Enum("Kiến trúc sư", "architect_long", "architect_short")).Required(),
		schema.Property("task", schema.String("Nhiệm vụ đầy đủ giao cho kiến trúc sư (gồm cả nhu cầu đã mở rộng)")).Required(),
		schema.Property("reason", schema.String("Lý do lựa chọn")).Required(),
	),
}

// planStartPayload is the plan_start user payload (the fact is the input; there is no store state —
// this is a new book).
type planStartPayload struct {
	Requirement string `json:"requirement"`
	Style       string `json:"style,omitempty"`
}

// DecidePlanStart is the plan-start decision: choose a planner from the user requirement. When the
// requirement is too short (<20 characters) it independently fills in a differentiation direction,
// target reader and core selling point, and at least one unconventional hook in the task.
// Failure semantics: returning an error makes the caller abort startup with an explicit error (the
// user is present at startup, so an error beats a guess).
func DecidePlanStart(ctx context.Context, model agentcore.ChatModel, systemPrompt, requirement, style string) (PlanStartDecision, error) {
	payload, err := marshalPayload(planStartPayload{Requirement: requirement, Style: style})
	if err != nil {
		return PlanStartDecision{}, err
	}
	return decide(ctx, model, planStartContract, systemPrompt, payload, (*PlanStartDecision).Validate)
}
