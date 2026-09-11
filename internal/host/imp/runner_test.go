package imp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// testDeps builds the minimal Deps where all three semantic functions share one mock tier.
func testDeps(st *store.Store, m callModel) Deps {
	c := Caller{Model: m}
	return Deps{
		Store:         st,
		CommitChapter: tools.NewCommitChapterTool(st, tools.NewStyleStatsIndex(st)),
		Segment:       c,
		Analyze:       c,
		Synthesize:    c,
		Prompts:       Prompts{Segment: "seg", Analyze: "ana", Synthesize: "syn", Range: "range"},
	}
}

// TestRunEndToEnd drives the full pipeline ingest→segment→analyze→synthesize→publish with a mock
// model, persisting through the real commit_chapter, and verifies the official Foundation and every
// chapter are ready.
func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文一\n第二章\n正文二\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seg := boundariesJSON(
		boundaryFixture("L1", "", kindChapter, "第一章"),
		boundaryFixture("L3", "", kindChapter, "第二章"),
	)
	ana := `{"chapters":[` + factsJSON(1, "第一章") + `,` + factsJSON(2, "第二章") + `]}`
	syn := synthesisFixtureJSON(2, storyClosed)
	m := &mockModel{responses: []string{seg, ana, syn}}

	ch, err := Run(context.Background(), testDeps(st, m), Options{SourcePath: src, AutoConfirm: true, ContinueAfter: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var runErr error
	var doneSeen bool
	for ev := range ch {
		if ev.Stage == StageError {
			runErr = ev.Err
		}
		if ev.Stage == StageDone {
			doneSeen = true
		}
	}
	if runErr != nil {
		t.Fatalf("管线失败：%v", runErr)
	}
	if !doneSeen {
		t.Fatal("未收到 StageDone")
	}
	// Official state is ready: the work information, premise and a flat outline covering every chapter are on disk (world_rules is legitimately empty and not required).
	if book, _ := st.Book.Load(); book == nil || book.Synopsis == "" {
		t.Fatalf("作品信息未落盘: %+v", book)
	}
	if p, _ := st.Outline.LoadPremise(); p == "" {
		t.Fatal("premise 未落盘")
	}
	if o, _ := st.Outline.LoadOutline(); len(o) != 2 {
		t.Fatalf("扁平大纲应覆盖 2 章，得 %d", len(o))
	}
	prog, _ := st.Progress.Load()
	if prog == nil || len(prog.CompletedChapters) != 2 {
		t.Fatalf("应完成 2 章：%+v", prog)
	}
	if active, done, err := ResumeStatus(st); err != nil || !active || !done {
		t.Fatalf("ResumeStatus 应为 active&done，得 active=%v done=%v", active, done)
	}
	// --continue: no import-completion Hold is set (left to the host's automatic relay).
	if meta, _ := st.RunMeta.Load(); meta != nil && meta.AdvanceHold != nil {
		t.Fatalf("--continue 不应留下导入完成 Hold：%+v", meta.AdvanceHold)
	}
}

// TestRunSetsCompletionHold verifies a non---continue import sets a boundary Hold once complete
// (RFC §12.4). The Hold is the only guarantee against mistakenly continuing after an import and must be
// persisted on the publication path.
func TestRunSetsCompletionHold(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文一\n第二章\n正文二\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seg := boundariesJSON(
		boundaryFixture("L1", "", kindChapter, "第一章"),
		boundaryFixture("L3", "", kindChapter, "第二章"),
	)
	ana := `{"chapters":[` + factsJSON(1, "第一章") + `,` + factsJSON(2, "第二章") + `]}`
	syn := synthesisFixtureJSON(2, storyClosed)
	m := &mockModel{responses: []string{seg, ana, syn}}

	ch, err := Run(context.Background(), testDeps(st, m), Options{SourcePath: src, AutoConfirm: true}) // 无 --continue
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for ev := range ch {
		if ev.Stage == StageError {
			t.Fatalf("管线失败：%v", ev.Err)
		}
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatalf("load run meta: %v", err)
	}
	if meta == nil || meta.AdvanceHold == nil {
		t.Fatalf("导入完成应设置 boundary Hold，得 %+v", meta)
	}
}

// TestRunRejectsDifferentSource guards source-switch interception (RFC §12.1/§18.2): passing a source
// file with different content while a workspace is in progress must error explicitly — ingest runs only
// with no workspace, and without the comparison it would silently continue from the old book's breakpoint,
// publish that book in full and never read a byte of the new file. Re-passing the same file's path is a
// common recovery habit, so the comparison is by content digest.
func TestRunRejectsDifferentSource(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(a, []byte("第一章\n正文一\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Ingest(dir, a, Options{}.intent()); err != nil {
		t.Fatalf("建立工作区：%v", err)
	}
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(b, []byte("完全不同的另一本书\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := Run(context.Background(), testDeps(st, &mockModel{responses: []string{"{}"}}), Options{SourcePath: b})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var runErr error
	for ev := range ch {
		if ev.Stage == StageError {
			runErr = ev.Err
		}
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "khác nội dung") {
		t.Fatalf("不同源文件应被明确拒绝，得 %v", runErr)
	}
}

// TestConfirmNotesGate guards --yes's tolerance threshold: a segmentation whose structure went through
// semantic tolerance (non-empty Notes) was deterministically rewritten and is not blindly let through by
// a --yes that never saw the preview; pressing y after the TUI preview (AcceptSegmentation) does let it
// through, recording method=user_confirmed for provenance.
func TestConfirmNotesGate(t *testing.T) {
	newRunner := func(opts Options, notes []string) *runner {
		ws := &Workspace{dir: t.TempDir()}
		if err := ws.writeJSON(fileIntent, Intent{}); err != nil {
			t.Fatal(err)
		}
		seg := Segmentation{Chapters: []ChapterSpan{{Number: 1, Title: "第一章", End: 10}}, Notes: notes}
		if err := writeArtifact(ws, fileSegmentation, "d", seg); err != nil {
			t.Fatal(err)
		}
		return &runner{opts: opts, events: make(chan Event, 8), ws: ws}
	}
	r := newRunner(Options{AutoConfirm: true}, []string{"空正文占位并入前段"})
	if r.confirm() {
		t.Fatal("--yes 不应放行带容错说明的切分")
	}
	if ev := <-r.events; !strings.Contains(ev.Message, "không tự động cho qua") {
		t.Fatalf("预览应说明未放行原因：%q", ev.Message)
	}
	if !newRunner(Options{AutoConfirm: true}, nil).confirm() {
		t.Fatal("--yes 应放行无容错说明的切分")
	}
	r = newRunner(Options{AcceptSegmentation: true}, []string{"空正文占位并入前段"})
	if !r.confirm() {
		t.Fatal("预览后的人工 y 应放行带容错说明的切分")
	}
	conf, err := readArtifact[Confirmation](r.ws, fileConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Payload.Method != confirmMethodUser {
		t.Fatalf("人工确认应记 user_confirmed，得 %q", conf.Payload.Method)
	}
}

// TestStoryChoiceIgnoresStaleResolution guards #5: after a re-synthesis the old story ruling is void,
// and storyChoice must not silently apply an old open/closed to the new synthesis (or the user would
// never be consulted again).
func TestStoryChoiceIgnoresStaleResolution(t *testing.T) {
	ws := OpenWorkspace(t.TempDir())
	if err := ws.writeJSON(fileIntent, Intent{}); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifact(ws, fileSynthesis, "d", BookSynthesis{Premise: "p1", StoryStatus: storyUncertain}); err != nil {
		t.Fatal(err)
	}
	raw, _ := ws.readBytes(fileSynthesis)
	if err := writeArtifact(ws, fileStoryResolve, Digest(raw), StoryResolution{Choice: storyClosed}); err != nil {
		t.Fatal(err)
	}
	r := &runner{ws: ws}
	if got, err := r.storyChoice(); err != nil || got != storyClosed {
		t.Fatalf("绑定当前 synthesis 的裁定应返回 closed，得 %q", got)
	}
	// Re-synthesis: rewrite the synthesis → the old ruling's InputDigest mismatches and is ignored, returning to "must ask again" (an empty result).
	if err := writeArtifact(ws, fileSynthesis, "d", BookSynthesis{Premise: "p2", StoryStatus: storyUncertain}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.storyChoice(); err != nil || got != "" {
		t.Fatalf("重新综合后旧裁定应失效返回空，得 %q", got)
	}
}

// TestBudgetsFromDepsPerTier guards the tier knob (RFC §13.1): each semantic function's budget derives
// from its own tier, so a cheap tier's small window constrains only its own function and never drags the
// other stages down.
func TestBudgetsFromDepsPerTier(t *testing.T) {
	small := ModelRuntime{ContextTokens: 32000, MaxOutputTokens: 4000}
	big := ModelRuntime{ContextTokens: 200000, MaxOutputTokens: 16000}
	b := budgetsFromDeps(Deps{
		Segment:    Caller{Runtime: small},
		Analyze:    Caller{Runtime: big},
		Synthesize: Caller{Runtime: big},
	})
	if b.SegmentChunkBytes >= b.Analyze.ContextBytes {
		t.Fatalf("segment 小档位窗口应只约束自身：seg=%d analyze=%d", b.SegmentChunkBytes, b.Analyze.ContextBytes)
	}
	if b.Analyze.MaxOutputTokens != 16000 || b.SegmentMaxTokens != 4000 {
		t.Fatalf("输出预算应各取自身档位上限：analyze=%d segment=%d", b.Analyze.MaxOutputTokens, b.SegmentMaxTokens)
	}
}

// TestRunSavesFailureOnContractViolation guards §14.2: a native Schema contract breach must surface
// immediately and land the raw response and metadata in failures/.
func TestRunSavesFailureOnContractViolation(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &nativeImportModel{mockModel: &mockModel{responses: []string{"这不是 JSON"}}}
	ch, err := Run(context.Background(), testDeps(st, m), Options{SourcePath: src, AutoConfirm: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var failed bool
	for ev := range ch {
		if ev.Stage == StageError {
			failed = true
		}
	}
	if !failed {
		t.Fatal("非法输出应以 StageError 结束")
	}
	ws := OpenWorkspace(dir)
	if !ws.has("failures/last-response.txt") {
		t.Fatal("应保存最后一次原始模型响应")
	}
	var meta FailureMeta
	if err := ws.readJSON("failures/last.json", &meta); err != nil {
		t.Fatalf("读失败元数据：%v", err)
	}
	if meta.Stage != string(ActionSegment) {
		t.Fatalf("失败元数据应标注 segment 阶段，得 %q", meta.Stage)
	}
}

// TestRunGuidanceResegments guards §18.3: carrying --guide on recovery makes the old segmentation miss
// naturally, re-identifies under the new guidance and stops at confirmation again; the new
// segmentation's InputDigest binds the guidance text.
func TestRunGuidanceResegments(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("第一章\n正文一\n第二章\n正文二\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drain := func(ch <-chan Event) (awaiting bool) {
		for ev := range ch {
			if ev.Stage == StageError {
				t.Fatalf("管线失败：%v", ev.Err)
			}
			if ev.Stage == StageAwaitingConfirmation {
				awaiting = true
			}
		}
		return awaiting
	}
	// First interactive import: the model splits the whole book into 1 chapter and stops at confirmation.
	one := boundariesJSON(boundaryFixture("L1", "", kindChapter, "第一章"))
	ch, err := Run(context.Background(), testDeps(st, &mockModel{responses: []string{one}}), Options{SourcePath: src})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !drain(ch) {
		t.Fatal("首次导入应停在切分确认")
	}
	// Recovery with guidance: the old segmentation misses → re-identified as 2 chapters, stopping at confirmation again.
	two := boundariesJSON(
		boundaryFixture("L1", "", kindChapter, "第一章"),
		boundaryFixture("L3", "", kindChapter, "第二章"),
	)
	guidance := "第二章也是独立章节"
	ch2, err := Run(context.Background(), testDeps(st, &mockModel{responses: []string{two}}), Options{Guidance: guidance})
	if err != nil {
		t.Fatalf("恢复 Run: %v", err)
	}
	if !drain(ch2) {
		t.Fatal("重识别后应再次停在切分确认")
	}
	ws := OpenWorkspace(dir)
	art, err := readArtifact[Segmentation](ws, fileSegmentation)
	if err != nil {
		t.Fatalf("读切分工件：%v", err)
	}
	if len(art.Payload.Chapters) != 2 {
		t.Fatalf("应按指导切成 2 章，得 %d", len(art.Payload.Chapters))
	}
	norm, _ := ws.LoadSource()
	if art.InputDigest != segmentInputDigest(Digest(norm), guidance, segmentPromptVersion) {
		t.Fatal("新切分 InputDigest 应绑定指导文本")
	}
}

// TestBudgetsFromRuntime verifies the dual budgets scale with the model's real capacity and fall back to conservative defaults when capability is unknown (RFC §9.2/§21).
func TestBudgetsFromRuntime(t *testing.T) {
	if got := budgetsFromRuntime(ModelRuntime{}); got != DefaultRunBudgets() {
		t.Fatal("能力未知应回退保守默认")
	}
	small := budgetsFromRuntime(ModelRuntime{ContextTokens: 32000, MaxOutputTokens: 4000})
	big := budgetsFromRuntime(ModelRuntime{ContextTokens: 200000, MaxOutputTokens: 16000})
	if big.Analyze.ContextBytes <= small.Analyze.ContextBytes {
		t.Fatalf("更大 context 应放大 analyze 输入预算：small=%d big=%d", small.Analyze.ContextBytes, big.Analyze.ContextBytes)
	}
	if big.Analyze.MaxOutputTokens != 16000 {
		t.Fatalf("输出预算应取模型 completion 上限，得 %d", big.Analyze.MaxOutputTokens)
	}
}

// TestProfileForKeyPolicy guards the event-merge scope: a request backoff (carrying a deadline) ticks in
// place under one Key, while a validation re-ask is a cross-call semantic event with no Key and gets its
// own row — segmentation calls per block, so sharing a Key would let a later block overwrite an earlier
// one and leave the panel with a single row whose unit_id keeps changing, losing every clue; step is an
// ordinary progress event (no warning level).
func TestProfileForKeyPolicy(t *testing.T) {
	r := &runner{events: make(chan Event, 3)}
	prof := r.profileFor(Caller{}, StageSegmenting)
	prof.notify("退避", time.Now().Add(time.Second))
	prof.notify("重问", time.Time{})
	prof.step(2, 12, "切分第 %d/%d 块...", 2, 12)
	backoff, reask, step := <-r.events, <-r.events, <-r.events
	if backoff.Key == "" || backoff.Level != "warn" || backoff.RetryAt.IsZero() {
		t.Fatalf("请求退避应为带 Key 与截止时刻的 warn 事件：%+v", backoff)
	}
	if reask.Key != "" || reask.Level != "warn" {
		t.Fatalf("校验重问应为不带 Key 的 warn 事件（独立成行）：%+v", reask)
	}
	if step.Level != "" || step.Current != 2 || step.Total != 12 {
		t.Fatalf("step 应为普通进度事件：%+v", step)
	}
}

// TestCallProfileOptions verifies callProfile owns only the output budget and thinking; response_format
// is chosen by callStructured from model facts and the Contract and must not be assembled again in the
// Profile.
func TestCallProfileOptions(t *testing.T) {
	if got := (callProfile{}).callOptions(100); len(got) != 1 {
		t.Fatalf("零值只应带 maxTokens，得 %d 个 option", len(got))
	}
	if got := (callProfile{thinking: "high"}).callOptions(100); len(got) != 2 {
		t.Fatalf("thinking 应带 2 个 option，得 %d", len(got))
	}
}
