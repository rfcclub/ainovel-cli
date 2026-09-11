package imp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/logger"
	"github.com/voocel/ainovel-cli/internal/store"
)

// The prompt/schema version is part of each stage's InputDigest; bump it when the prompt contract changes so downstream artifacts invalidate naturally.
const (
	segmentPromptVersion = "seg-v2" // v2: boundaries land only on real separators, titles copied verbatim (paired with title echo-back validation)
	analyzePromptVersion = "analyze-v1"
	confirmMethodAuto    = "auto_authorized"
	confirmMethodUser    = "user_confirmed" // explicit human confirmation via y after the TUI preview
)

// Prompts holds the system prompts of the semantic functions. Synthesis has two stages: Synthesize
// produces the whole book's BookSynthesis and Range produces RangeDigest for a contiguous range of a long
// book; their output shapes differ, so each needs its own prompt.
type Prompts struct {
	Segment    string
	Analyze    string
	Synthesize string
	Range      string
}

// RunBudgets holds the semantic functions' input/output budgets. The first version uses conservative
// constants; these should eventually be derived from the current architect model's context window /
// completion cap so batches scale naturally with capability (RFC §9.2/§21).
type RunBudgets struct {
	MaxUnitBytes         int
	SegmentChunkBytes    int
	SegmentContextMargin int
	SegmentMaxTokens     int
	Analyze              AnalyzeBudget
	SynthesizeRangeBytes int
	SynthesizeMaxTokens  int
}

// DefaultRunBudgets returns conservative default budgets, a fallback for when model capability is unknown (probing failed).
func DefaultRunBudgets() RunBudgets {
	return RunBudgets{
		MaxUnitBytes:         8000,
		SegmentChunkBytes:    24000,
		SegmentContextMargin: 20,
		SegmentMaxTokens:     8192,
		Analyze:              AnalyzeBudget{ContextBytes: 24000, MaxOutputTokens: 8000, PerChapterOutput: 900, PromptOverhead: 2000},
		SynthesizeRangeBytes: 16000,
		SynthesizeMaxTokens:  8192,
	}
}

// ModelRuntime carries the model capability facts imp's semantic calls need, injected by the Host after
// probing at the boundary (RFC §13/§17).
// It lets the dual budgets scale with context/completion and thinking follow capability; with all-zero
// values it falls back to conservative defaults, behaving exactly as before capabilities were wired in.
// Structured output does not send response_format based on provider capability (see the callProfile
// comment).
type ModelRuntime struct {
	ContextTokens   int                     // Input context limit (tokens)
	MaxOutputTokens int                     // Per-call visible output limit (tokens)
	Thinking        agentcore.ThinkingLevel // Resolved per capability; ThinkingAuto("") means do not send explicitly
}

// profile derives this runtime's call capability options (thinking).
func (rt ModelRuntime) profile() callProfile {
	return callProfile{thinking: rt.Thinking}
}

// Caller is one semantic function's model tier: the model plus that model's capability facts (RFC
// §13.1/§17).
// segment/analyze/synthesize each hold their own tier, with budgets and call options derived per tier, so
// a cheap tier's small window constrains only its own function and never drags the other stages down.
type Caller struct {
	Model   callModel
	Runtime ModelRuntime
}

// budgetsFromRuntime derives each semantic function's budget from the model's real context/completion
// caps (RFC §9.2/§21).
// This is what makes "a stronger model automatically widens batches and cuts call counts" hold; with
// unknown capability it falls back to conservative defaults.
func budgetsFromRuntime(rt ModelRuntime) RunBudgets {
	if rt.ContextTokens <= 0 || rt.MaxOutputTokens <= 0 {
		return DefaultRunBudgets()
	}
	const bytesPerToken = 3 // Conservative UTF-8 conversion for Latin text: token -> bytes (under-estimating capacity is safer)
	out := rt.MaxOutputTokens
	// Input budget: the context minus the visible output and a ~10% reasoning/system reserve, converted to bytes.
	reserve := rt.ContextTokens / 10
	inTokens := rt.ContextTokens - out - reserve
	if inTokens < 2000 {
		inTokens = 2000
	}
	inBytes := inTokens * bytesPerToken
	return RunBudgets{
		MaxUnitBytes:         min(inBytes/2, 32000),
		SegmentChunkBytes:    inBytes,
		SegmentContextMargin: 20,
		SegmentMaxTokens:     out,
		Analyze: AnalyzeBudget{
			ContextBytes:     inBytes,
			MaxOutputTokens:  out,
			PerChapterOutput: 900,
			PromptOverhead:   2000,
		},
		SynthesizeRangeBytes: inBytes,
		SynthesizeMaxTokens:  out,
	}
}

// Confirmation is the segmentation-confirmation artifact, bound to the current segmentation (RFC §8.4).
type Confirmation struct {
	Method   string `json:"method"`
	Chapters int    `json:"chapters"`
}

// StoryResolution is the user's ruling on an uncertain story status, bound to the current synthesis (RFC §10.4).
type StoryResolution struct {
	Choice string `json:"choice"` // open / closed
}

// Deps holds the runner's narrow dependencies (RFC §17). The three semantic functions each declare a
// model tier; the Host defaults all of them to architect, and the config layer may point the more
// mechanical functions at a cheaper tier (RFC §13.1).
type Deps struct {
	Store         *store.Store
	CommitChapter ChapterCommitter
	Segment       Caller
	Analyze       Caller
	Synthesize    Caller // range digest and book synthesis share a tier (same synthesis stage)
	Prompts       Prompts
	Budgets       RunBudgets
}

// budgetsFromDeps derives budgets from each semantic function's own tier capability (RFC §9.2/§13.1).
func budgetsFromDeps(d Deps) RunBudgets {
	seg := budgetsFromRuntime(d.Segment.Runtime)
	ana := budgetsFromRuntime(d.Analyze.Runtime)
	syn := budgetsFromRuntime(d.Synthesize.Runtime)
	return RunBudgets{
		MaxUnitBytes:         seg.MaxUnitBytes,
		SegmentChunkBytes:    seg.SegmentChunkBytes,
		SegmentContextMargin: seg.SegmentContextMargin,
		SegmentMaxTokens:     seg.SegmentMaxTokens,
		Analyze:              ana.Analyze,
		SynthesizeRangeBytes: syn.SynthesizeRangeBytes,
		SynthesizeMaxTokens:  syn.SynthesizeMaxTokens,
	}
}

// Run executes the full import pipeline: LoadState → NextAction → perform one action → re-read facts.
// It runs in its own goroutine, and this function closes the returned event channel.
func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.CommitChapter == nil ||
		deps.Segment.Model == nil || deps.Analyze.Model == nil || deps.Synthesize.Model == nil {
		return nil, fmt.Errorf("deps chưa đầy đủ")
	}
	if deps.Budgets == (RunBudgets{}) {
		deps.Budgets = budgetsFromDeps(deps)
	}
	// The import log is its own file: one import's complete transcript (events, retries, full error chains)
	// is not mixed into the engine/TUI log, so investigation looks at that one file. A creation failure must
	// surface — the panel points the user at logs/import.log, and a silent fallback would point them at a
	// file that does not exist (Debug-First).
	log, closeLog, logErr := logger.FileLogger(deps.Store.Dir(), "import.log")
	log.Info("thời gian chạy model nhập liệu của imp",
		"segment_ctx", deps.Segment.Runtime.ContextTokens,
		"analyze_ctx", deps.Analyze.Runtime.ContextTokens,
		"synthesize_ctx", deps.Synthesize.Runtime.ContextTokens,
		"analyze_max_output", deps.Analyze.Runtime.MaxOutputTokens,
		"analyze_context_bytes", deps.Budgets.Analyze.ContextBytes)
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer closeLog()
		r := &runner{deps: deps, opts: opts, events: events, ws: OpenWorkspace(deps.Store.Dir()), log: log}
		if logErr != nil {
			r.emit(StageIngesting, 0, 0, fmt.Sprintf("tạo file log nhập liệu thất bại (%v), lần ghi này chuyển sang log mặc định", logErr), nil)
		}
		r.run(ctx)
	}()
	return events, nil
}

type runner struct {
	deps   Deps
	opts   Options
	events chan Event
	ws     *Workspace
	act    Action       // Current action; labels the stage on failure artefacts
	log    *slog.Logger // Import-specific log (logs/import.log); falls back to the default logger when nil
}

func (r *runner) emit(stage Stage, current, total int, msg string, err error) {
	r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err})
}

func (r *runner) send(ev Event) {
	r.logEvent(ev)
	// Terminal and stop-point events carry the only success/failure and action-required signals (lose the
	// confirmation preview or the --story prompt and the user no longer knows what to do), so they must be
	// delivered reliably; only intermediate progress events may be dropped when backed up.
	if ev.Stage == StageError || ev.Stage == StageDone ||
		ev.Stage == StageAwaitingConfirmation || ev.Stage == StageAwaitingStoryStatus {
		r.events <- ev
		return
	}
	select {
	case r.events <- ev:
	default: // Drop progress on a full channel; never block the pipeline
	}
}

// logEvent transcribes every progress event into the import-specific log (<book root>/logs/import.log):
// the panel overwrites retry rows in place and vanishes on Esc, so the log is the only complete flow record
// available for later investigation (§14.1).
func (r *runner) logEvent(ev Event) {
	log := r.log
	if log == nil {
		log = slog.Default()
	}
	args := []any{"stage", string(ev.Stage)}
	if ev.Total > 0 {
		args = append(args, "progress", fmt.Sprintf("%d/%d", ev.Current, ev.Total))
	}
	if ev.Err != nil {
		args = append(args, "err", ev.Err)
	}
	level := slog.LevelInfo
	switch {
	case ev.Stage == StageError:
		level = slog.LevelError // A terminal failure is the one line worth filtering for; it must not land as INFO
	case ev.Level == "warn":
		level = slog.LevelWarn
	}
	log.Log(context.Background(), level, ev.Message, args...)
}

func (r *runner) fail(msg string, err error) {
	r.saveFailure(err)
	r.emit(StageError, 0, 0, msg, err)
}

// saveFailure uniformly lands failures carrying a raw response into failures/ (RFC §14.2's third landing
// point); every semantic function such as segment/synthesize shares this fallback, while the analysis
// truncation-salvage path already writes finer metadata in place. Failures with no raw response (IO,
// cancellation, precondition) have no model output to save and write nothing.
func (r *runner) saveFailure(err error) {
	var se *errSemantic
	var tr *errTruncated
	switch {
	case errors.As(err, &se):
		r.ws.writeFailure(FailureMeta{Stage: string(r.act), Detail: err.Error()}, se.Raw)
	case errors.As(err, &tr):
		r.ws.writeFailure(FailureMeta{Stage: string(r.act), Detail: err.Error(), StopReason: "length"}, tr.Raw)
	}
}

// facts combines workspace facts with official-publication reconciliation.
func (r *runner) facts() (Facts, error) {
	return CollectFacts(r.deps.Store, r.ws)
}

// profileFor derives one tier's call options and echoes request backoff / validation re-queries into the
// corresponding stage's event stream — retry backoff can silently accumulate for over 2 minutes, and
// without an echo the user would think it hung (§14.1).
// The Key is given only to request backoff (with a deadline): that is transient state within one call, and
// the UI updates one row in place (the "attempt N" ticking). A validation re-query is a cross-call
// semantic event — segmentation calls per block and each block re-queries independently, so sharing a Key
// would let a later block overwrite an earlier one and eat the investigation trail (measured: the panel was
// left with one row whose unit_id kept changing), hence each gets its own row and history.
func (r *runner) profileFor(c Caller, stage Stage) callProfile {
	prof := c.Runtime.profile()
	prof.log = r.log
	prof.notify = func(msg string, retryAt time.Time) {
		ev := Event{Time: time.Now(), Stage: stage, Message: msg, Level: "warn", RetryAt: retryAt}
		if !retryAt.IsZero() {
			ev.Key = "retry:" + string(stage)
		}
		r.send(ev)
	}
	prof.progress = func(current, total int, msg string) {
		r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg})
	}
	return prof
}

// applyGuidance persists this run's explicit --guide guidance as a workspace semantic input (RFC §18.3).
// Guidance is one of the inputs to segmentation's InputDigest: a content change naturally invalidates the
// old segmentation and everything downstream and redoes it, with no manual invalidation rules. It is
// skipped while the workspace does not exist and written on the next loop after ingest.
func (r *runner) applyGuidance() error {
	g := strings.TrimSpace(r.opts.Guidance)
	if g == "" || !r.ws.Active() {
		return nil
	}
	existing, err := r.ws.LoadGuidance()
	if err != nil {
		return fmt.Errorf("đọc hướng dẫn phân tách sẵn có: %w", err)
	}
	if existing == g {
		return nil
	}
	// Official artifacts cannot be overwritten once publication begins (§12.2): re-segmenting would then hit
	// publish's "refuse to overwrite" wall, after first re-paying the whole segmentation/analysis/synthesis
	// chain of model calls — so the failure is brought forward to where it costs nothing.
	// book is the first write of publication, so its existence means publication has begun (the import
	// precondition guarantees the book started empty).
	book, err := r.deps.Store.Book.Load()
	if err != nil {
		return fmt.Errorf("đọc book chính thức: %w", err)
	}
	if book != nil {
		return fmt.Errorf("Foundation chính thức đã bắt đầu phát hành; phân tách lại bằng --guide sẽ xung đột với nội dung đã phát hành và bị từ chối ghi đè, nên không nhận hướng dẫn phân tách nữa")
	}
	return r.ws.writeAtomic(fileGuidance, []byte(g))
}

// checkSourceIdentity stops "a different source file passed while a workspace is in progress": ingest runs
// only with no workspace, so without a comparison /import B.txt would silently continue from A's breakpoint,
// publish all of A, and never read a single byte of B (RFC §12.1/§18.2).
// Re-passing the path of the same file is a common habit (/import on the same path to resume), so the
// comparison is by content digest rather than rejecting every path.
func (r *runner) checkSourceIdentity() error {
	if r.opts.SourcePath == "" || !r.ws.Active() {
		return nil
	}
	m, err := r.ws.LoadManifest()
	if err != nil {
		return nil // An unreadable identity triple is left to ingest corrupt-detection; do not report it twice here
	}
	raw, err := os.ReadFile(r.opts.SourcePath)
	if err != nil {
		return fmt.Errorf("đọc file nguồn %s: %w", r.opts.SourcePath, err)
	}
	if Digest(raw) != m.RawSHA256 {
		return fmt.Errorf("đang có một lần nhập %q chạy dở, file nguồn lần này khác nội dung với nó: hãy hoàn tất hoặc bỏ lần nhập cũ (xóa meta/import/) rồi mới nhập sách mới", m.SourceName)
	}
	return nil
}

func (r *runner) run(ctx context.Context) {
	if err := r.checkSourceIdentity(); err != nil {
		r.fail("kiểm tra danh tính file nguồn", err)
		return
	}
	var previous *Facts
	for {
		if ctx.Err() != nil {
			r.fail("người dùng hủy", ctx.Err())
			return
		}
		if err := r.applyGuidance(); err != nil {
			r.fail("ghi hướng dẫn phân tách", err)
			return
		}
		facts, err := r.facts()
		if err != nil {
			r.fail("đọc trạng thái nhập liệu", err)
			return
		}
		if previous != nil && facts == *previous {
			r.fail("nhập liệu đình trệ", fmt.Errorf("sau khi thực hiện hành động, sự thật không thay đổi, hành động kế tiếp vẫn là %q", NextAction(facts)))
			return
		}
		snapshot := facts
		previous = &snapshot
		act := NextAction(facts)
		r.act = act
		err = nil
		switch act {
		case ActionIngest:
			err = r.ingest(ctx)
		case ActionSegment:
			err = r.segment(ctx)
		case ActionAwaitConfirmation:
			if !r.confirm() {
				return // Interactive mode: stop here and wait for user confirmation.
			}
		case ActionAnalyze:
			err = r.analyze(ctx)
		case ActionSynthesize:
			err = r.synthesize(ctx)
		case ActionAwaitStoryResolution:
			if !r.resolveStoryStatus() {
				return // No explicit decision: stop here and wait for --story=open|closed.
			}
		case ActionPublish:
			err = r.publish(ctx)
		case ActionDone:
			r.emit(StageDone, 0, 0, "nhập liệu hoàn tất, chờ nghiệm thu để viết tiếp", nil)
			return
		default:
			err = fmt.Errorf("hành động không xác định %q", act)
		}
		if err != nil {
			r.fail("nhập liệu thất bại", err)
			return
		}
	}
}

func (r *runner) ingest(ctx context.Context) error {
	// Reaching ingest with the directory already present means the identity triple (manifest/source/intent)
	// is missing or corrupt: createWorkspace refuses with "already exists (a bare /import can recover)" while
	// a bare rerun lands back here because WorkspaceReady=false and asks for a source path — the two messages
	// contradict each other and the user is stuck.
	if r.ws.Active() {
		return fmt.Errorf("meta/import/ đã tồn tại nhưng danh tính workspace không dùng được (manifest/source/intent thiếu hoặc hỏng), hãy kiểm tra thủ công rồi xóa thư mục đó và nhập lại")
	}
	if err := checkImportPreconditions(r.deps.Store); err != nil {
		return err
	}
	if r.opts.SourcePath == "" {
		return fmt.Errorf("lần nhập mới cần đường dẫn file nguồn")
	}
	r.emit(StageIngesting, 0, 0, "Đọc, giải mã, chuẩn hóa và chụp nhanh file nguồn...", nil)
	_, m, err := Ingest(r.deps.Store.Dir(), r.opts.SourcePath, r.opts.intent())
	if err != nil {
		return err
	}
	r.emit(StageIngesting, 0, 0, fmt.Sprintf("Ảnh chụp nguồn đã sẵn sàng: %s (mã hóa %s, %d byte)", m.SourceName, m.Encoding, m.SizeBytes), nil)
	return nil
}

func (r *runner) segment(ctx context.Context) error {
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	units := buildSourceUnits(src, r.deps.Budgets.MaxUnitBytes)
	guidance, err := r.ws.LoadGuidance()
	if err != nil {
		return fmt.Errorf("đọc hướng dẫn phân tách: %w", err)
	}
	r.emit(StageSegmenting, 0, 0, fmt.Sprintf("Nhận diện ranh giới chương theo ngữ nghĩa (%d đơn vị tọa độ)...", len(units)), nil)
	digest := segmentInputDigest(Digest(src), guidance, segmentPromptVersion)
	// The block cache identity additionally binds MaxUnitBytes: the unit table is uniquely determined by
	// (normalised source, MaxUnitBytes), so changing MaxUnitBytes with a different model tier reshapes the
	// virtual fragments of over-long lines — the ID sequence (L1.1…) and block endpoints reproduce while the
	// byte ranges have moved, and matching on endpoint IDs alone would reuse misaligned old boundaries (an
	// anchor mismatch failing deterministically, or a silent mis-split).
	chunkIdentity := fmt.Sprintf("%s\x00units:%d", digest, r.deps.Budgets.MaxUnitBytes)
	seg, err := Segment(ctx, r.deps.Segment.Model, r.deps.Prompts.Segment, src, units, guidance,
		r.deps.Budgets.SegmentChunkBytes, r.deps.Budgets.SegmentContextMargin, r.deps.Budgets.SegmentMaxTokens,
		r.profileFor(r.deps.Segment, StageSegmenting), r.ws, chunkIdentity)
	if err != nil {
		return err
	}
	if err := writeArtifact(r.ws, fileSegmentation, digest, *seg); err != nil {
		return err
	}
	// The final segmentation is on disk so the block cache has served its purpose; a failed cleanup does not affect correctness (the digest still matches) but must be logged.
	if cerr := r.ws.clearDir(dirSegmentChunks); cerr != nil {
		r.emit(StageSegmenting, 0, 0, fmt.Sprintf("Dọn cache cấp khối thất bại (không ảnh hưởng kết quả phân tách): %v", cerr), nil)
	}
	r.emit(StageSegmenting, len(seg.Chapters), len(seg.Chapters),
		fmt.Sprintf("Phân tách xong: %d chương, %d vùng phụ trợ", len(seg.Chapters), len(seg.Matter)), nil)
	return nil
}

// confirm handles segmentation confirmation. --yes accepts automatically and writes the confirmation artifact; otherwise it shows the preview and stops.
func (r *runner) confirm() bool {
	seg, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		r.fail("đọc kết quả phân tách", err)
		return false
	}
	in, err := r.ws.LoadIntent()
	if err != nil {
		r.fail("đọc ý định nhập liệu", err)
		return false
	}
	accept := r.opts.AcceptSegmentation
	auto := r.opts.AutoConfirm || (in != nil && in.AutoConfirm)
	// --yes does not blindly let through a segmentation where semantic tolerance fired (non-empty Notes:
	// empty-chapter absorption, start fallback, coincidence dedup): the structure was deterministically
	// rewritten and needs human review — otherwise the tolerance notes go unseen under --yes, which amounts to
	// a silent rewrite.
	// Pressing y after the TUI preview goes through AcceptSegmentation (an explicit ruling made after seeing
	// the preview) and is exempt.
	blockedByNotes := auto && !accept && len(seg.Payload.Notes) > 0
	if blockedByNotes {
		auto = false
	}
	if !auto && !accept {
		msg := buildConfirmPreview(&seg.Payload)
		if blockedByNotes {
			msg += "  ! Có ghi chú dung sai khi phân tách, --yes không tự động cho qua, cần kiểm tra thủ công\n"
		}
		r.emit(StageAwaitingConfirmation, len(seg.Payload.Chapters), len(seg.Payload.Chapters), msg, nil)
		return false
	}
	raw, err := r.ws.readBytes(fileSegmentation)
	if err != nil {
		r.fail("đọc sản phẩm phân tách", err)
		return false
	}
	method, doneMsg := confirmMethodAuto, "đã tự động chấp nhận phân tách (--yes)"
	if accept {
		method, doneMsg = confirmMethodUser, "đã xác nhận phân tách (kiểm tra thủ công)"
	}
	conf := Confirmation{Method: method, Chapters: len(seg.Payload.Chapters)}
	if err := writeArtifact(r.ws, fileConfirmation, Digest(raw), conf); err != nil {
		r.fail("ghi sản phẩm xác nhận", err)
		return false
	}
	r.emit(StageAwaitingConfirmation, len(seg.Payload.Chapters), len(seg.Payload.Chapters), doneMsg, nil)
	return true
}

// buildConfirmPreview assembles the segmentation confirmation preview: chapter count, auxiliary regions, every chapter title and the uncertain flags (RFC §8.4).
// Everything is listed and the panel viewport scrolls; there is no truncation cap.
func buildConfirmPreview(seg *Segmentation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Đã phân tách %d chương", len(seg.Chapters))
	if len(seg.Matter) > 0 {
		fmt.Fprintf(&b, ", %d vùng phụ trợ", len(seg.Matter))
	}
	if len(seg.Uncertain) > 0 {
		fmt.Fprintf(&b, " (%d chương còn nghi ngờ)", len(seg.Uncertain))
	}
	b.WriteString(", hãy kiểm tra:\n")
	uncertain := make(map[int]bool, len(seg.Uncertain))
	for _, n := range seg.Uncertain {
		uncertain[n] = true
	}
	for _, c := range seg.Chapters {
		fmt.Fprintf(&b, "  Chương %d %s", c.Number, c.Title)
		if uncertain[c.Number] {
			b.WriteString("  [nghi ngờ]")
		}
		b.WriteByte('\n')
	}
	for _, mt := range seg.Matter {
		fmt.Fprintf(&b, "  [%s] %s\n", mt.Kind, mt.Title)
	}
	// Tolerance notes from segmentation (an empty-prose placeholder title merged into the preceding span, say) must appear at the human stop point, or the absorption becomes a silent rewrite.
	for _, n := range seg.Notes {
		fmt.Fprintf(&b, "  ! %s\n", n)
	}
	// The action hints (y to confirm / --guide to re-segment / Esc) are rendered uniformly by the TUI pause block; only facts stay here, avoiding two copies of the copy drifting apart.
	return b.String()
}

func (r *runner) analyze(ctx context.Context) error {
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	seg := &segArt.Payload
	total := len(seg.Chapters)
	// A per-chapter digest binds only that chapter's prose, not the batch context or the preceding ledger. So
	// when chapter K needs re-analysis because it is missing or mismatched, later old artifacts whose digest
	// still matches exactly would be reused with a stale ledger. Before analysis begins, the tail past the
	// fresh prefix is cleared, enforcing "re-analysing one chapter invalidates every analysis after it";
	// forward analysis then never produces a stale tail (RFC §9.6 / #4a).
	if err := discardAnalysesAfter(r.ws, analyzedChapters(r.ws, seg, src, segArt.InputDigest, analyzePromptVersion), total); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := analyzedChapters(r.ws, seg, src, segArt.InputDigest, analyzePromptVersion)
		if start >= total {
			break
		}
		r.emit(StageAnalyzing, start, total, fmt.Sprintf("Phân tích các lô liên tục bắt đầu từ chương %d...", start+1), nil)
		done, err := AnalyzeNext(ctx, r.deps.Analyze.Model, r.deps.Prompts.Analyze, r.ws, src, seg, segArt.InputDigest, analyzePromptVersion, r.deps.Budgets.Analyze, r.profileFor(r.deps.Analyze, StageAnalyzing))
		if err != nil {
			return err
		}
		if done == 0 {
			break
		}
	}
	r.emit(StageAnalyzing, total, total, "Trích xuất sự thật từng chương hoàn tất", nil)
	return nil
}

func (r *runner) synthesize(ctx context.Context) error {
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	total := len(segArt.Payload.Chapters)
	facts := loadPriorFacts(r.ws, total)
	if len(facts) != total {
		return fmt.Errorf("phân tích từng chương chưa đầy đủ: %d/%d", len(facts), total)
	}
	r.emit(StageSynthesizing, 0, total, "Khái quát ngữ nghĩa toàn sách theo tầng...", nil)
	syn, err := Synthesize(ctx, r.deps.Synthesize.Model, r.deps.Prompts.Synthesize, r.deps.Prompts.Range, r.ws, facts,
		r.deps.Budgets.SynthesizeRangeBytes, r.deps.Budgets.SynthesizeMaxTokens, r.profileFor(r.deps.Synthesize, StageSynthesizing))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.ws, fileSynthesis, synthesisInputDigest(facts), *syn); err != nil {
		return err
	}
	r.emit(StageSynthesizing, total, total, fmt.Sprintf("Tổng hợp xong: %d tập, trạng thái truyện %s", len(syn.Structure), syn.StoryStatus), nil)
	return nil
}

func (r *runner) publish(ctx context.Context) error {
	synArt, err := readArtifact[BookSynthesis](r.ws, fileSynthesis)
	if err != nil {
		return err
	}
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	seg := &segArt.Payload
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	total := len(seg.Chapters)
	facts := loadPriorFacts(r.ws, total)
	if len(facts) != total {
		return fmt.Errorf("phân tích chưa đầy đủ trước khi phát hành: %d/%d", len(facts), total)
	}
	closed, err := r.resolveStory(&synArt.Payload)
	if err != nil {
		return err
	}
	manifest, err := r.ws.LoadManifest()
	if err != nil {
		return err
	}
	f, err := AssembleFoundation(&synArt.Payload, facts, closed, manifest.SourceName)
	if err != nil {
		return err
	}
	r.emit(StageValidating, 0, total, "Kiểm tra lắp ghép Foundation đạt", nil)

	r.emit(StagePublishing, 0, total, "Phát hành Foundation chính thức...", nil)
	if err := publishFoundation(r.deps.Store, f); err != nil {
		return err
	}
	// The import-completion Hold must be persisted earlier than any chapter commit: a crash between "the last
	// chapter commits" and "the Hold is set" would leave isPublished=true after restart, so the import counts
	// as finished while the Hold was never set, and the Engine would mistake the imported book for an ordinary
	// stopped one and continue writing. Placing it after publishFoundation (RunMeta already initialised) and
	// before the chapter commits closes that window completely; a rerun sets it idempotently (--continue does
	// not set a Hold, leaving it to the automatic relay, RFC §12.4).
	if err := r.setCompletionHold(); err != nil {
		return fmt.Errorf("thiết lập Hold hoàn tất nhập liệu: %w", err)
	}
	for i, c := range seg.Chapters {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(StagePublishing, c.Number, total, fmt.Sprintf("Phát hành chương %d/%d: %s", c.Number, total, c.Title), nil)
		if err := publishChapter(ctx, r.deps.Store, r.deps.CommitChapter, c.Number, seg.Content(src, i), facts[i]); err != nil {
			return err
		}
	}
	return nil
}

// storyChoice returns the effective ruling for an uncertain status: the persisted ruling bound to the
// current synthesis first, then this run's opts, then the original intent.
// A persisted ruling must have its InputDigest checked against the current synthesis — after a
// re-synthesis the old ruling is void and an old open/closed must not be silently applied to a new result,
// or the user would never be consulted again (RFC §10.4). An explicit --story (intent) is the user's
// standing instruction across syntheses and may be kept.
func (r *runner) storyChoice() (string, error) {
	if raw, err := r.ws.readBytes(fileSynthesis); err == nil {
		if art, aerr := readArtifact[StoryResolution](r.ws, fileStoryResolve); aerr == nil && art.InputDigest == Digest(raw) {
			return art.Payload.Choice, nil
		} else if aerr != nil && !os.IsNotExist(aerr) {
			return "", fmt.Errorf("đọc phán định trạng thái truyện: %w", aerr)
		}
	} else {
		return "", fmt.Errorf("đọc sản phẩm tổng hợp: %w", err)
	}
	if r.opts.StoryResolution != "" {
		return r.opts.StoryResolution, nil
	}
	in, err := r.ws.LoadIntent()
	if err != nil {
		return "", fmt.Errorf("đọc ý định nhập liệu: %w", err)
	}
	return in.StoryResolution, nil
}

// resolveStoryStatus persists story-resolution.json when the status is uncertain and an explicit ruling
// exists (bound to the current synthesis), so downstream NextAction lets it through naturally; with no
// ruling it shows the wait and stops.
func (r *runner) resolveStoryStatus() bool {
	choice, err := r.storyChoice()
	if err != nil {
		r.fail("đọc phán định trạng thái truyện", err)
		return false
	}
	if choice != storyOpen && choice != storyClosed {
		r.emit(StageAwaitingStoryStatus, 0, 0, "Tổng hợp phán định trạng thái truyện là uncertain, hãy dùng --story=open|closed để nêu rõ rồi thử lại", nil)
		return false
	}
	raw, err := r.ws.readBytes(fileSynthesis)
	if err != nil {
		r.fail("đọc kết quả tổng hợp", err)
		return false
	}
	if err := writeArtifact(r.ws, fileStoryResolve, Digest(raw), StoryResolution{Choice: choice}); err != nil {
		r.fail("ghi phán định trạng thái truyện xuống đĩa", err)
		return false
	}
	return true
}

// resolveStory yields the story's closure status from the synthesis result and the user's explicit ruling (RFC §10.4).
func (r *runner) resolveStory(syn *BookSynthesis) (bool, error) {
	switch syn.StoryStatus {
	case storyClosed:
		return true, nil
	case storyOpen:
		return false, nil
	case storyUncertain:
		choice, err := r.storyChoice()
		if err != nil {
			return false, err
		}
		switch choice {
		case storyClosed:
			return true, nil
		case storyOpen:
			return false, nil
		default:
			return false, fmt.Errorf("trạng thái truyện là uncertain, cần --story=open|closed")
		}
	default:
		return false, fmt.Errorf("story_status không xác định: %q", syn.StoryStatus)
	}
}

// setCompletionHold sets the one-shot import-completion Hold; only --continue skips it (RFC §12.4).
// Errors must propagate — the Hold is the only guarantee that nothing is mistakenly continued after an
// import, and a silent failure means the protection is void.
func (r *runner) setCompletionHold() error {
	in, err := r.ws.LoadIntent()
	if err != nil {
		return fmt.Errorf("đọc ý định nhập liệu: %w", err)
	}
	if r.opts.ContinueAfter || (in != nil && in.ContinueAfterImport) {
		return nil
	}
	return r.deps.Store.RunMeta.SetAdvanceHold(domain.AdvanceHold{
		After:  domain.AdvanceHoldAtBoundary,
		Reason: "Nhập tiểu thuyết từ bên ngoài hoàn tất, chờ nghiệm thu để viết tiếp",
	})
}
