package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ResolveOutlineFeedbackTool persists the "the current plan still applies" audit verdict and consumes the feedback.
type ResolveOutlineFeedbackTool struct{ store *store.Store }

func NewResolveOutlineFeedbackTool(store *store.Store) *ResolveOutlineFeedbackTool {
	return &ResolveOutlineFeedbackTool{store: store}
}

func (t *ResolveOutlineFeedbackTool) Name() string  { return "resolve_outline_feedback" }
func (t *ResolveOutlineFeedbackTool) Label() string { return "Xác nhận đại cương không cần chỉnh" }
func (t *ResolveOutlineFeedbackTool) Description() string {
	return "Xác nhận đã duyệt hết writer_feedback và kế hoạch tiếp theo hiện có vẫn còn phù hợp. Chỉ gọi khi đại cương không cần sửa; khi cần sửa thì dùng revise_outline hoặc các công cụ cấu trúc."
}
func (t *ResolveOutlineFeedbackTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *ResolveOutlineFeedbackTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *ResolveOutlineFeedbackTool) StrictSchema() bool                   { return true }
func (t *ResolveOutlineFeedbackTool) Schema() map[string]any {
	return schema.Object(schema.Property("reason", schema.String("Lý do kế hoạch hiện có vẫn còn phù hợp")).Required())
}

func (t *ResolveOutlineFeedbackTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		return nil, fmt.Errorf("reason is required: %w", errs.ErrToolArgs)
	}
	feedback, err := t.store.Outline.LoadPendingOutlineFeedback()
	if err != nil {
		return nil, fmt.Errorf("load outline feedback: %w: %w", errs.ErrStoreRead, err)
	}
	if len(feedback) == 0 {
		return nil, fmt.Errorf("không có phản hồi đại cương nào đang chờ xử lý: %w", errs.ErrToolPrecondition)
	}
	if err := t.store.Outline.SaveOutlineFeedbackResolution(input.Reason, len(feedback)); err != nil {
		return nil, fmt.Errorf("save outline feedback resolution: %w: %w", errs.ErrStoreWrite, err)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "resolve_outline_feedback", "meta/outline_feedback_resolution.json"); err != nil {
		return nil, fmt.Errorf("checkpoint outline feedback resolution: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Outline.ClearOutlineFeedback(); err != nil {
		return nil, fmt.Errorf("clear outline feedback: %w: %w", errs.ErrStoreWrite, err)
	}
	return json.Marshal(map[string]any{"resolved": len(feedback), "outline_changed": false, "reason": input.Reason})
}
