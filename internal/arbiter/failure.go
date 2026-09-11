package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// FailureFacts is the fact pack shared by the worker_failure / deadlock scenarios: the Engine has
// already done the deterministic classification (retries, bad arguments and so on never reach
// here), so what arrives at the Arbiter is only the residue "deterministic code cannot resolve".
type FailureFacts struct {
	Kind          string   `json:"kind"` // worker_failure | deadlock
	Agent         string   `json:"agent,omitempty"`
	Task          string   `json:"task,omitempty"`
	Error         string   `json:"error,omitempty"` // worker_failure: error text
	ErrorKind     string   `json:"error_kind,omitempty"`
	Repeats       int      `json:"repeats,omitempty"` // deadlock: how many times the same instruction was dispatched
	Phase         string   `json:"phase,omitempty"`
	NextChapter   int      `json:"next_chapter,omitempty"`
	PendingQueue  []int    `json:"pending_rewrites,omitempty"`
	FoundationGap []string `json:"foundation_missing,omitempty"`
	FactWarnings  []string `json:"fact_warnings,omitempty"`
}

// FailureDecision is the failure / deadlock decision.
type FailureDecision struct {
	Action   string      `json:"action"` // retry | reroute | abort
	Dispatch *DispatchOp `json:"dispatch,omitempty"`
	Reason   string      `json:"reason"`
}

func (d *FailureDecision) ValidateAgainst(f FailureFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason không được để trống")
	}
	switch d.Action {
	case "retry", "abort":
		return nil
	case "reroute":
		if d.Dispatch == nil {
			return fmt.Errorf("reroute bắt buộc phải kèm dispatch")
		}
		if err := d.Dispatch.validate(); err != nil {
			return err
		}
		return validateDispatchAgainst(d.Dispatch, f.Phase)
	default:
		return fmt.Errorf("action không hợp lệ: %q (chọn retry / reroute / abort)", d.Action)
	}
}

// failureContract sits next to FailureDecision: action is a closed enum and dispatch a nullable
// object (non-null only for reroute); cross-field combinations are still fact-checked by
// ValidateAgainst.
var failureContract = llmcontract.Contract{
	Name:        "arbiter_failure",
	Description: "Phán định thất bại/bế tắc: đưa ra lối thoát",
	Schema: schema.Object(
		schema.Property("action", schema.Enum("Lối thoát", "retry", "reroute", "abort")).Required(),
		schema.Property("dispatch", dispatchSchema("Đích giao việc (chỉ đưa khi reroute, nếu không thì null)")).Required(),
		schema.Property("reason", schema.String("Lý do phán định")).Required(),
	),
}

// DecideFailure is the failure / deadlock consultation. Failure semantics: returning an error
// makes the Engine take the most conservative path (pause plus notify); it never consults
// indefinitely.
func DecideFailure(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts FailureFacts) (FailureDecision, error) {
	payload, err := marshalPayload(facts)
	if err != nil {
		return FailureDecision{}, err
	}
	return decide(ctx, model, failureContract, systemPrompt, payload, func(d *FailureDecision) error {
		return d.ValidateAgainst(facts)
	})
}
