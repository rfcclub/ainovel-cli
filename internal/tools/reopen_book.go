package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReopenBookTool reopens a completed book into rework mode, called by the Engine at an intervention action boundary.
// After completion completePhaseGate hard-blocks every subagent dispatch, leaving the user unable to rework written
// chapters. This tool is not a subagent and is callable during complete: it atomically switches phase back to writing,
// puts the target chapters into PendingRewrites and sets flow=rewriting, after which the Flow Router dispatches the
// writer against the existing rework queue to rewrite chapter by chapter, and commit_chapter re-wraps up completion once
// the queue drains. None of the Gate / Router / edit / commit heavy logic needs changing.
type ReopenBookTool struct {
	store *store.Store
}

func NewReopenBookTool(s *store.Store) *ReopenBookTool {
	return &ReopenBookTool{store: s}
}

func (t *ReopenBookTool) Name() string  { return "reopen_book" }
func (t *ReopenBookTool) Label() string { return "Mở lại để làm lại" }

func (t *ReopenBookTool) Description() string {
	return "Mở lại toàn sách đã kết thúc (phase=complete) để vào trạng thái làm lại, dùng khi người dùng yêu cầu viết lại/gọt giũa vài chương sau khi đã hoàn thành sách." +
		"chapters là các số chương đã hoàn thành cần làm lại; sau khi gọi, những chương này vào hàng đợi viết lại, Host sẽ lần lượt giao writer viết lại, sửa xong hết thì tự động kết thúc sách trở lại." +
		"Chỉ dùng khi toàn sách đã kết thúc và người dùng yêu cầu rõ ràng về việc sửa chương đã viết; nếu người dùng muốn thêm tình tiết/mở rộng dung lượng thì không phải làm lại, đừng dùng công cụ này."
}

// Write tool; concurrency is forbidden.
func (t *ReopenBookTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *ReopenBookTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *ReopenBookTool) ActivityDescription(_ json.RawMessage) string {
	return "Mở lại toàn sách để làm lại"
}

func (t *ReopenBookTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapters", schema.Array("Danh sách số chương đã hoàn thành cần làm lại (ít nhất một chương)", schema.Int(""))).Required(),
		schema.Property("reason", schema.String("Lý do làm lại (tùy chọn, ví dụ \"dọn ký tự đặc biệt\")")),
	)
}

func (t *ReopenBookTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapters []int  `json:"chapters"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if len(a.Chapters) == 0 {
		return nil, fmt.Errorf("chapters không được để trống, cần chỉ rõ chương cần làm lại: %w", errs.ErrToolArgs)
	}

	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return nil, fmt.Errorf("progress chưa được khởi tạo: %w", errs.ErrToolPrecondition)
	}
	// Only written chapters can be reworked; a chapter number outside the completed set means continuation or out-of-range, so it is refused explicitly and the user is pointed at length adjustment.
	var invalid []int
	for _, ch := range a.Chapters {
		if !slices.Contains(progress.CompletedChapters, ch) {
			invalid = append(invalid, ch)
		}
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("chương %v chưa viết xong, reopen chỉ làm lại được các chương đã hoàn thành (muốn thêm/mở rộng tình tiết thì điều chỉnh dung lượng): %w", invalid, errs.ErrToolPrecondition)
	}

	// The phase precondition is backed up inside store.Reopen (callable only from complete).
	if err := t.store.Progress.Reopen(a.Chapters, a.Reason); err != nil {
		return nil, fmt.Errorf("reopen: %w: %w", errs.ErrStoreWrite, err)
	}

	// checkpoint: symmetric with complete_book (GlobalScope + meta/progress.json).
	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "reopen", "meta/progress.json"); err != nil {
		return nil, fmt.Errorf("checkpoint reopen: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"reopened":         true,
		"phase":            string(domain.PhaseWriting),
		"pending_rewrites": a.Chapters,
		"next_step":        "Đã mở lại và đưa các chương mục tiêu vào hàng đợi. Hãy chờ Host giao writer làm lại từng chương; sửa xong hết sẽ tự động kết thúc sách trở lại.",
	})
}
