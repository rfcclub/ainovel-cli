package imp

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/store"
)

// Action is the next deterministic step NextAction derives from workspace facts.
// Persistent state never stores a drifting stage enum; the next action comes only from artifacts (RFC §6.2).
type Action string

const (
	ActionIngest               Action = "ingest"
	ActionSegment              Action = "segment"
	ActionAwaitConfirmation    Action = "await_confirmation"
	ActionAnalyze              Action = "analyze"
	ActionSynthesize           Action = "synthesize"
	ActionAwaitStoryResolution Action = "await_story_resolution"
	ActionPublish              Action = "publish"
	ActionDone                 Action = "done"
)

// Facts is the minimal fact snapshot read from the workspace that decides the next action.
// It separates pure decision (NextAction) from IO (LoadState): NextAction is constant for the same Facts (RFC
// §20.1).
type Facts struct {
	WorkspaceReady   bool // The manifest + intent + source triple is complete
	Segmented        bool
	Confirmed        bool
	ExpectedChapters int // Total chapters confirmed by segmentation (filled from stage two on)
	AnalyzedChapters int // Count of analyses contiguous from chapter 1 with a matching InputDigest (filled from stage three on)
	Synthesized      bool
	StoryUncertain   bool
	StoryResolved    bool
	Published        bool // Formal artefacts match synthesis exactly (filled from stage five on)
}

// NextAction walks the fixed linear pipeline and returns the first missing or unsatisfied action. A pure function, no IO.
func NextAction(f Facts) Action {
	switch {
	case f.Published:
		// Publication is terminal: official-store reconciliation already agrees completely and the workspace is
		// only an audit archive. Stale upstream artifacts from a prompt-version or guidance upgrade are no longer
		// required to be redone — otherwise a version bump would retroactively push a published book back to
		// half-done and the Engine's cross-restart gate would lock it permanently.
		return ActionDone
	case !f.WorkspaceReady:
		return ActionIngest
	case !f.Segmented:
		return ActionSegment
	case !f.Confirmed:
		return ActionAwaitConfirmation
	case f.AnalyzedChapters < f.ExpectedChapters:
		return ActionAnalyze
	case !f.Synthesized:
		return ActionSynthesize
	case f.StoryUncertain && !f.StoryResolved:
		return ActionAwaitStoryResolution
	default:
		return ActionPublish
	}
}

// artifactFresh decides that an artifact exists and its InputDigest equals the want that should be rebuilt now;
// absent, unparseable, or a schema or digest mismatch all count as not fresh (must be redone).
func artifactFresh[T any](w *Workspace, rel, want string) (bool, error) {
	a, err := readArtifact[T](w, rel)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.InputDigest == want, nil
}

// LoadState reads the current fact snapshot from the workspace (the workspace only, not the official Store).
// Linear short-circuit: each step checks the artifact's InputDigest against the digest rebuildable from the current
// upstream, and a mismatch on any step means that step is unfinished, leaving downstream facts false for
// NextAction to redo from there — this is what makes "change the segmentation / prompt version / source"
// invalidate downstream naturally (RFC §6.2/§6.3 / invariant 1).
// Published is filled in by the caller from official-publication reconciliation (always via CollectFacts).
func LoadState(w *Workspace) (Facts, error) {
	var f Facts
	if !w.Active() {
		return f, nil
	}
	if !(w.has(fileManifest) && w.has(fileIntent) && w.has(fileSource)) {
		return f, nil
	}
	src, err := w.LoadSource()
	if err != nil {
		return f, fmt.Errorf("đọc ảnh chụp nguồn nhập liệu: %w", err)
	}
	f.WorkspaceReady = true
	guidance, err := w.LoadGuidance()
	if err != nil {
		return f, fmt.Errorf("đọc hướng dẫn phân tách: %w", err)
	}

	// segmentation: binds the normalised source + user guidance + segmentation prompt version. A guidance change (--guide re-identification) invalidates the old segmentation naturally.
	segArt, err := readArtifact[Segmentation](w, fileSegmentation)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("đọc sản phẩm phân tách: %w", err)
	}
	if segArt.InputDigest != segmentInputDigest(Digest(src), guidance, segmentPromptVersion) {
		return f, nil
	}
	f.Segmented = true
	seg := &segArt.Payload
	f.ExpectedChapters = len(seg.Chapters)

	// confirmation: binds the segmentation artifact's raw bytes.
	segRaw, err := w.readBytes(fileSegmentation)
	if err != nil {
		return f, fmt.Errorf("đọc nguyên văn sản phẩm phân tách: %w", err)
	}
	confirmed, err := artifactFresh[Confirmation](w, fileConfirmation, Digest(segRaw))
	if err != nil {
		return f, fmt.Errorf("đọc xác nhận phân tách: %w", err)
	}
	if !confirmed {
		return f, nil
	}
	f.Confirmed = true

	// Per-chapter analysis: the contiguous count whose per-chapter InputDigest matches the segmentation identity/version/prose.
	f.AnalyzedChapters, err = analyzedChaptersStrict(w, seg, src, segArt.InputDigest, analyzePromptVersion)
	if err != nil {
		return f, err
	}
	if f.AnalyzedChapters < f.ExpectedChapters {
		return f, nil
	}

	// synthesis: binds the ordered per-chapter facts.
	facts, err := loadPriorFactsStrict(w, f.ExpectedChapters)
	if err != nil {
		return f, err
	}
	synArt, err := readArtifact[BookSynthesis](w, fileSynthesis)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("đọc sản phẩm tổng hợp toàn sách: %w", err)
	}
	if synArt.InputDigest != synthesisInputDigest(facts) {
		return f, nil
	}
	f.Synthesized = true
	f.StoryUncertain = synArt.Payload.StoryStatus == storyUncertain

	// story resolution: binds the synthesis artifact's raw bytes when uncertain, or is pre-selected by intent.
	synRaw, err := w.readBytes(fileSynthesis)
	if err != nil {
		return f, fmt.Errorf("đọc nguyên văn sản phẩm tổng hợp toàn sách: %w", err)
	}
	resolved, err := artifactFresh[StoryResolution](w, fileStoryResolve, Digest(synRaw))
	if err != nil {
		return f, fmt.Errorf("đọc phán định trạng thái truyện: %w", err)
	}
	if resolved {
		f.StoryResolved = true
	} else if in, iErr := w.LoadIntent(); iErr != nil {
		return f, fmt.Errorf("đọc ý định nhập liệu: %w", iErr)
	} else if in.StoryResolution != "" {
		f.StoryResolved = true
	}
	return f, nil
}

// CollectFacts combines workspace facts with official-publication reconciliation and is the unified fact entry
// point for ResumeStatus/ResumeSummary/runner.
// The expected chapter count for publication reconciliation prefers a fresh segmentation; when segmentation has
// gone stale from a prompt-version or guidance upgrade it falls back to the chapter count confirmed at the time
// inside the artifact — a published book's official chapters were persisted from exactly that segmentation, so
// reconciling against a digest recomputed with the current version would match nothing.
func CollectFacts(st *store.Store, w *Workspace) (Facts, error) {
	f, err := LoadState(w)
	if err != nil {
		return f, err
	}
	expected := f.ExpectedChapters
	if expected == 0 {
		if segArt, err := readArtifact[Segmentation](w, fileSegmentation); err == nil {
			expected = len(segArt.Payload.Chapters)
		}
	}
	f.Published, err = isPublished(st, expected)
	return f, err
}

// ResumeStatus reports whether an active import workspace exists and whether it is completely finished (including
// official-publication reconciliation).
// It serves the cross-restart Engine gate (RFC §12.5): with active && !done the ordinary creation flow is barred
// from consuming half-published state.
func ResumeStatus(st *store.Store) (active, done bool, err error) {
	w := OpenWorkspace(st.Dir())
	if !w.Active() {
		return false, false, nil
	}
	f, err := CollectFacts(st, w)
	if err != nil {
		return true, false, err
	}
	return true, NextAction(f) == ActionDone, nil
}

// ResumeSummary generates a one-line hint about an unfinished import (RFC §18.2); it returns an empty string when
// there is none.
// It lets the host tell the user proactively on startup / the welcome screen, so they do not only discover a book
// stuck mid-import when the creation gate refuses them.
func ResumeSummary(st *store.Store) string {
	w := OpenWorkspace(st.Dir())
	if !w.Active() {
		return ""
	}
	f, err := CollectFacts(st, w)
	if err != nil {
		return "Phát hiện lỗi khi đọc trạng thái nhập liệu: " + err.Error() + "; hãy chạy /import để xem và sửa"
	}
	var state string
	switch NextAction(f) {
	case ActionDone:
		return ""
	case ActionIngest, ActionSegment:
		state = "chưa hoàn tất phân tách"
	case ActionAwaitConfirmation:
		state = fmt.Sprintf("đã phân tách %d chương, chờ kiểm tra xác nhận", f.ExpectedChapters)
	case ActionAnalyze:
		state = fmt.Sprintf("đã phân tích %d/%d chương", f.AnalyzedChapters, f.ExpectedChapters)
	case ActionSynthesize:
		state = "phân tích từng chương xong, chờ tổng hợp toàn sách"
	case ActionAwaitStoryResolution:
		state = "chờ xác định trạng thái truyện (--story=open|closed)"
	case ActionPublish:
		state = "tổng hợp xong, chờ phát hành trạng thái chính thức"
	}
	return "Phát hiện lần nhập liệu chưa hoàn tất (" + state + "), nhập /import để khôi phục từ điểm dừng"
}

// checkImportPreconditions validates the preconditions for a new import (RFC §12.1): no existing work information,
// no completed chapters and no in-flight PendingCommit. The merge semantics of an existing novel with new external
// text are unclear, so the first version refuses outright.
func checkImportPreconditions(st *store.Store) error {
	book, err := st.Book.Load()
	if err != nil {
		return fmt.Errorf("đọc thông tin tác phẩm: %w", err)
	}
	if book != nil {
		return fmt.Errorf("đã có tác phẩm 《%s》, từ chối gộp tiểu thuyết bên ngoài vào một cuốn sách không rỗng", book.Title)
	}
	prog, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("đọc tiến độ: %w", err)
	}
	if prog != nil && len(prog.CompletedChapters) > 0 {
		return fmt.Errorf("đã có %d chương hoàn thành, từ chối gộp tiểu thuyết bên ngoài vào một cuốn sách không rỗng", len(prog.CompletedChapters))
	}
	pending, err := st.Signals.LoadPendingCommit()
	if err != nil {
		return fmt.Errorf("đọc bản commit đang dang dở: %w", err)
	}
	if pending != nil {
		return fmt.Errorf("đang có bản commit chương dang dở, hãy hoàn tất hoặc dọn sạch rồi mới nhập")
	}
	return nil
}
