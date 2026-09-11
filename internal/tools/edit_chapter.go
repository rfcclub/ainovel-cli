package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EditChapterTool performs targeted string replacement in a chapter draft, suited to polishing.
// It saves 10x+ tokens against a full-chapter rewrite with draft_chapter.
//
// Persistence contract: only drafts/{ch:02d}.draft.md is modified, and chapters/ may never be edited directly (the
// final draft belongs to commit_chapter alone).
// Seed semantics: no draft but a chapter exists → the chapter is copied into drafts as the starting point.
// Ownership check: only chapters already complete and present in the PendingRewrites queue may be edited.
//
// This tool is a thin wrapper over agentcore.EditTool; the find-replace logic (multi-level tolerant matching, diff
// output, line-ending/BOM preservation) is all reused from upstream.
type EditChapterTool struct {
	store *store.Store
	edit  *agentcoretools.EditTool
}

func NewEditChapterTool(s *store.Store) *EditChapterTool {
	return &EditChapterTool{
		store: s,
		edit:  agentcoretools.NewEdit(s.Dir(), nil),
	}
}

func (t *EditChapterTool) Name() string  { return "edit_chapter" }
func (t *EditChapterTool) Label() string { return "Sửa chương" }

// ReadOnly explicitly declares a write tool (together with ConcurrencySafeTool it prevents concurrent scheduling).
func (t *EditChapterTool) ReadOnly(_ json.RawMessage) bool { return false }

// ConcurrencySafe explicitly forbids concurrency: parallel edit_chapter calls on the same chapter race on
// read-modify-write, and even different chapters would interleave checkpoint ordering. Serialising everything is
// safest.
func (t *EditChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// ActivityDescription supplies the activity description of the current tool for the UI/logs.
func (t *EditChapterTool) ActivityDescription(_ json.RawMessage) string { return "Sửa bản nháp chương" }

func (t *EditChapterTool) Description() string {
	return "Chỉ thay thế chuỗi tại chỗ trên bản nháp của chương đã hoàn thành và đã vào hàng đợi PendingRewrites (lựa chọn ưu tiên khi gọt giũa, tiết kiệm token hơn viết lại cả chương bằng draft_chapter)." +
		"Bản thảo sơ khởi của chương mới không được dùng công cụ này; nếu bản thảo sơ khởi có lỗi nghiêm trọng hãy gọi draft_chapter(mode=\"write\") để ghi đè cả chương." +
		"Tìm old_string và thay bằng new_string, yêu cầu khớp chính xác và duy nhất (nhiều chỗ khớp thì phải dùng replace_all=true)." +
		"old_string bắt buộc phải sao chép từng chữ từ kết quả read_chapter(source=\"draft\") gần nhất, cấm dựng lại nguyên văn theo trí nhớ;" +
		"lưu ý giá trị trả về là chuỗi JSON, \\n phải khôi phục thành ký tự xuống dòng thật. Sau khi draft_chapter ghi lại bản nháp thì phải read_chapter lại trước khi sửa." +
		"Lỗi khớp thất bại sẽ đính kèm đoạn ứng viên gần nhất trong bản nháp, hãy sao chép từng chữ từ ứng viên rồi thử lại." +
		"Ghi vào drafts/{ch}.draft.md; khi drafts chưa tồn tại sẽ tự động gieo từ chapters." +
		"Từ chối thực thi khi chương đã hoàn thành và không nằm trong hàng đợi PendingRewrites. Mỗi lần gọi chỉ sửa một chỗ, cần sửa nhiều chỗ thì gọi nhiều lần."
}

func (t *EditChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương")).Required(),
		schema.Property("old_string", schema.String("Đoạn nguyên văn chính xác cần thay, nhiều dòng thì phải kèm ký tự xuống dòng; khi không bật replace_all thì phải xuất hiện duy nhất trong bản nháp")).Required(),
		schema.Property("new_string", schema.String("Văn bản mới sau khi thay")).Required(),
		schema.Property("replace_all", schema.Bool("Thay tất cả chỗ khớp (mặc định false)")),
	)
}

func (t *EditChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter    int    `json:"chapter"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.OldString == "" {
		return nil, fmt.Errorf("old_string không được để trống: %w", errs.ErrToolArgs)
	}
	if a.OldString == a.NewString {
		return nil, fmt.Errorf("old_string giống new_string, không cần sửa: %w", errs.ErrToolArgs)
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		return nil, err
	}

	// Ownership check: mechanically enforces the writer protocol. A first draft of a new chapter may only be overwritten
	// wholesale; relying on the model to follow the prompt while still exposing fragile precise edits as an executable
	// path is not acceptable.
	completed, err := t.store.Progress.IsChapterCompleted(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if !completed {
		return nil, fmt.Errorf("chương %d chưa hoàn thành, bản thảo sơ khởi không được dùng edit_chapter; nếu có lỗi nghiêm trọng hãy gọi draft_chapter(mode=\"write\", chapter=%d) để ghi đè cả chương: %w", a.Chapter, a.Chapter, errs.ErrToolPrecondition)
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil || !slices.Contains(progress.PendingRewrites, a.Chapter) {
		return nil, fmt.Errorf("chương %d đã hoàn thành và không nằm trong hàng đợi PendingRewrites, không thể sửa; muốn sửa thì phải để editor thẩm duyệt và kích hoạt viết lại/gọt giũa trước: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	if err := EnsureChapterExpanded(t.store, a.Chapter); err != nil {
		return nil, err
	}

	// Seed: when no draft exists, copy one from chapters as the starting point
	if err := t.ensureDraft(a.Chapter); err != nil {
		return nil, err
	}

	// Delegate the find-replace to agentcore.EditTool
	subArgs, _ := json.Marshal(map[string]any{
		"path":        fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"file_path":   fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"old_text":    a.OldString,
		"old_string":  a.OldString,
		"new_text":    a.NewString,
		"new_string":  a.NewString,
		"replace_all": a.ReplaceAll,
	})
	result, err := t.edit.Execute(ctx, subArgs)
	if err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "edit",
		fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint edit: %w: %w", errs.ErrStoreWrite, err)
	}

	// Extra guidance: tells the writer the following steps so check_consistency / commit_chapter are not forgotten
	var passthrough map[string]any
	if err := json.Unmarshal(result, &passthrough); err != nil {
		return result, nil
	}
	passthrough["chapter"] = a.Chapter
	passthrough["next_step"] = "edit đã ghi xuống đĩa. Nếu vẫn còn lỗi nghiêm trọng thì có thể edit_chapter tiếp; nếu không thì check_consistency rồi commit_chapter"
	return json.Marshal(passthrough)
}

// ensureDraft guarantees drafts/{ch}.draft.md exists:
//   - a draft already present → return directly
//   - no draft but a final chapter → copy the final chapter into drafts as the starting point (common when polishing)
//   - neither → error, suggesting draft_chapter be used to create a first draft
func (t *EditChapterTool) ensureDraft(chapter int) error {
	draft, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil {
		return fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if draft != "" {
		return nil
	}
	text, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return fmt.Errorf("load chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if text == "" {
		return fmt.Errorf("chương %d không có bản nháp cũng không có bản chung cuộc, hãy gọi draft_chapter(mode=write, chapter=%d) để tạo bản thảo sơ khởi trước: %w", chapter, chapter, errs.ErrToolPrecondition)
	}
	if err := t.store.Drafts.SaveDraft(chapter, text); err != nil {
		return fmt.Errorf("seed draft from chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
