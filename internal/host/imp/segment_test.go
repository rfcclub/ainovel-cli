package imp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestUnitLessNumericNotLexical(t *testing.T) {
	// Lexicographic order would rank L900 > L1000 and L1257.2 > L1800; numeric order must be the reverse.
	if !unitLess(SourceUnit{Line: 900}, SourceUnit{Line: 1000}) {
		t.Fatal("L900 应 < L1000（数值序）")
	}
	if !unitLess(SourceUnit{Line: 1257, Part: 2}, SourceUnit{Line: 1800}) {
		t.Fatal("L1257.2 应 < L1800")
	}
	if !unitLess(SourceUnit{Line: 1257, Part: 1}, SourceUnit{Line: 1257, Part: 2}) {
		t.Fatal("同行 part 应按数值序")
	}
	if unitLess(SourceUnit{Line: 5}, SourceUnit{Line: 5}) {
		t.Fatal("相等不应 less")
	}
}

func TestBuildSourceUnitsRoundtrip(t *testing.T) {
	norm := []byte("第一章\n正文一\n\n第二章\n正文二")
	units := buildSourceUnits(norm, 0)
	// Reassembled: each unit's text plus '\n' between lines should restore the normalised text.
	var b strings.Builder
	for i, u := range units {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(u.Text)
		if u.Text != string(norm[u.StartByte:u.EndByte]) {
			t.Fatalf("unit %s 字节范围与文本不符", u.ID)
		}
	}
	if b.String() != string(norm) {
		t.Fatalf("拼回不符：%q", b.String())
	}
	if units[0].ID != "L1" || units[3].ID != "L4" {
		t.Fatalf("ID 不符：%s %s", units[0].ID, units[3].ID)
	}
}

func TestBuildSourceUnitsVirtualShard(t *testing.T) {
	// One line far over budget → split into several virtual units, with boundaries at UTF-8 character boundaries.
	long := strings.Repeat("字", 100) // 每字 3 字节 = 300 字节
	units := buildSourceUnits([]byte(long), 30)
	if len(units) < 2 {
		t.Fatalf("超预算行应分片，得到 %d", len(units))
	}
	var b strings.Builder
	for _, u := range units {
		if u.Line != 1 || u.Part == 0 {
			t.Fatalf("虚拟分片应同 Line、Part>=1：%+v", u)
		}
		b.WriteString(u.Text) // 分片同一行，无换行分隔
	}
	if b.String() != long {
		t.Fatal("虚拟分片拼回丢字")
	}
}

func TestResolveBoundaryByteAnchor(t *testing.T) {
	units := []SourceUnit{{ID: "L1", Line: 1, StartByte: 0, EndByte: 10, Text: "楔子风起楔"}}
	m := map[string]SourceUnit{"L1": units[0]}
	if _, err := resolveBoundaryByte(m, "L1", "风起"); err != nil {
		t.Fatalf("唯一锚点应成功：%v", err)
	}
	if _, err := resolveBoundaryByte(m, "L1", "楔"); err == nil {
		t.Fatal("重复锚点应失败")
	}
	if _, err := resolveBoundaryByte(m, "L1", "缺失"); err == nil {
		t.Fatal("不存在锚点应失败")
	}
	if _, err := resolveBoundaryByte(m, "L9", ""); err == nil {
		t.Fatal("不存在 unit 应失败")
	}
}

func TestPlanChunksCoversWithoutGap(t *testing.T) {
	units := buildSourceUnits([]byte(strings.Repeat("行内容\n", 50)), 0)
	chunks := planChunks(units, 40)
	if len(chunks) < 2 {
		t.Fatalf("应分多块，得 %d", len(chunks))
	}
	// Seamless, non-overlapping and fully covering.
	if chunks[0][0] != 0 || chunks[len(chunks)-1][1] != len(units) {
		t.Fatal("未完整覆盖")
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i][0] != chunks[i-1][1] {
			t.Fatalf("块 %d 与前块不相接：%v", i, chunks)
		}
	}
}

func segFixture() ([]byte, []SourceUnit) {
	norm := []byte("前言\n感谢阅读\n第一章 风起\n正文一\n卷二\n第二章 云涌\n正文二")
	return norm, buildSourceUnits(norm, 0)
}

func TestResolveSegmentationHappy(t *testing.T) {
	norm, units := segFixture()
	// L1 front matter / L3 chapter one / L5 volume two (group) / L6 chapter two
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L5", Kind: kindGroup, Title: "卷二"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	}
	seg, err := resolveSegmentation(norm, units, decisions)
	if err != nil {
		t.Fatalf("覆盖校验应通过：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("章节数应为 2（group 不计），得 %d", len(seg.Chapters))
	}
	if seg.Chapters[0].Number != 1 || seg.Chapters[1].Number != 2 {
		t.Fatal("章节号应连续")
	}
	if !strings.Contains(seg.Content(norm, 0), "正文一") {
		t.Fatalf("章一正文不符：%q", seg.Content(norm, 0))
	}
	// Coverage: the leading front_matter starts at 0 and the last chapter reaches the end of the text.
	if len(seg.Matter) == 0 || seg.Matter[0].Kind != kindFrontMatter || seg.Matter[0].Start != 0 {
		t.Fatalf("首段应为从 0 起的 front_matter：%+v", seg.Matter)
	}
	if seg.Chapters[len(seg.Chapters)-1].End != len(norm) {
		t.Fatal("末章应覆盖到文本尾")
	}
}

func TestResolveSegmentationRejections(t *testing.T) {
	norm, units := segFixture()
	cases := []struct {
		name string
		ds   []BoundaryDecision
	}{
		{"无章节", []BoundaryDecision{
			{UnitID: "L1", Kind: kindFrontMatter},
		}},
		{"非法kind", []BoundaryDecision{
			{UnitID: "L1", Kind: "verse"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := resolveSegmentation(norm, units, c.ds); err == nil {
				t.Fatalf("应被拒绝：%s", c.name)
			}
		})
	}
}

// TestResolveSegmentationReordersAndDedups guards the final fallback's coordinate discipline: occasional
// intra-block disorder is restored deterministically by byte sort (measured: 319 boundaries once failed on
// a single inversion, and the block cache made that failure reproduce deterministically); a duplicate at
// the same byte keeps the earlier one and records Notes for the confirmation preview.
func TestResolveSegmentationReordersAndDedups(t *testing.T) {
	norm, units := segFixture()
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L1", Kind: kindChapter, Title: "开篇"}, // 乱序：位置在 L3 之前
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 重复"}, // 同字节重复
	})
	if err != nil {
		t.Fatalf("乱序/重复应被确定性修复而非拒绝：%v", err)
	}
	if len(seg.Chapters) != 3 {
		t.Fatalf("应得 3 章，得 %d：%+v", len(seg.Chapters), seg.Chapters)
	}
	if seg.Chapters[0].Title != "开篇" || seg.Chapters[0].Start != 0 {
		t.Fatalf("排序后首章应为位置最前的边界：%+v", seg.Chapters[0])
	}
	if seg.Chapters[2].Title != "第二章 云涌" {
		t.Fatalf("同字节重复应保留先出现者：%+v", seg.Chapters[2])
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "trùng") {
		t.Fatalf("重复边界应记入 Notes：%v", seg.Notes)
	}
}

// TestResolveSegmentationAbsorbsLeadingText guards the deterministic repair of a missed start: when the
// model misses the boundary for non-empty leading text (a book-opening blurb or advert), it must not be
// vetoed at the end — the miss is already in the block cache, and vetoing would make a rerun reproduce
// the failure deterministically with zero calls. Go adds a front_matter covering [0, first) and records
// Notes for the confirmation preview.
func TestResolveSegmentationAbsorbsLeadingText(t *testing.T) {
	norm, units := segFixture()
	// Only chapters from L3 were reported: the non-empty text at L1/L2 has no owner.
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	})
	if err != nil {
		t.Fatalf("起始未归属文本应被收为 front_matter 而非拒绝：%v", err)
	}
	if len(seg.Matter) != 1 || seg.Matter[0].Kind != kindFrontMatter || seg.Matter[0].Start != 0 {
		t.Fatalf("应补出从 0 起的 front_matter：%+v", seg.Matter)
	}
	if len(seg.Chapters) != 2 || seg.Chapters[0].Start == 0 {
		t.Fatalf("章节不应吞掉头部文本：%+v", seg.Chapters)
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "không được model gán chủ") {
		t.Fatalf("应记录人工核对说明：%v", seg.Notes)
	}
}

// TestResolveSegmentationNotesDuplicateTitles guards the visibility of duplicate chapter names: a source
// with a title convention should not repeat chapter names, and a repeat is a deterministic signal of
// "one chapter split twice" — it only records Notes (which block --yes and appear in the preview) for
// human review, since whether to merge is not Go's call.
func TestResolveSegmentationNotesDuplicateTitles(t *testing.T) {
	norm, units := segFixture()
	seg, err := resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L6", Kind: kindChapter, Title: "第一章风起"}, // 同名（空白差异忽略）
	})
	if err != nil {
		t.Fatalf("同名章应放行并记 Notes：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应得 2 章，得 %d", len(seg.Chapters))
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "trùng tiêu đề") {
		t.Fatalf("应记一条同名核对说明：%v", seg.Notes)
	}
}

// TestChunkValidatorOwnedDiscipline guards the coverage of call-time validation: an invalid kind, a bad
// anchor, a semantic conflict at the same position or an unowned first-block start inside an owned range
// must all be re-asked with feedback at call time — letting them through puts them in the block cache, and
// discovering them only at the final resolve means a rerun reads the same bad data back with zero calls;
// context-region boundaries are bound to be cut and are never re-queried; an exact duplicate at the same
// position is mechanical redundancy that resolve silently deduplicates once let through.
func TestChunkValidatorOwnedDiscipline(t *testing.T) {
	norm, units := segFixture()
	unitByID := map[string]SourceUnit{}
	proj, owned := map[string]bool{}, map[string]bool{}
	for _, u := range units {
		unitByID[u.ID] = u
		proj[u.ID] = true
	}
	owned["L1"], owned["L2"], owned["L3"] = true, true, true
	v := chunkValidator{projIDs: proj, ownedIDs: owned, unitByID: unitByID, normalized: norm}

	cases := []struct {
		name    string
		bs      []BoundaryDecision
		wantErr bool
	}{
		{"owned 非法 kind", []BoundaryDecision{{UnitID: "L1", Kind: "volume"}}, true},
		{"owned 坏 anchor", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Anchor: "不存在的锚"}}, true},
		{"owned 合法 anchor", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Anchor: "第一章"}}, false},
		{"上下文区非法 kind 不重问", []BoundaryDecision{{UnitID: "L6", Kind: "volume"}}, false},
		{"投影外幻觉 ID", []BoundaryDecision{{UnitID: "L99", Kind: kindChapter}}, true},
		{"同位语义冲突重问", []BoundaryDecision{
			{UnitID: "L1", Kind: kindChapter, Title: "前言"},
			{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		}, true},
		{"同位完全重复放行", []BoundaryDecision{
			{UnitID: "L1", Kind: kindChapter, Title: "前言"},
			{UnitID: "L1", Kind: kindChapter, Title: "前言"},
		}, false},
		// Title echo-back: a chapter/volume name must genuinely exist in the boundary unit's original text — a fabricated title on a phantom boundary is stopped here.
		{"编造章节标题重问", []BoundaryDecision{{UnitID: "L2", Kind: kindChapter, Title: "第某章 我编的"}}, true},
		{"归纳标题须 uncertain 放行", []BoundaryDecision{{UnitID: "L2", Kind: kindChapter, Title: "第某章 我编的", Uncertain: true}}, false},
		{"回显容忍空白差异", []BoundaryDecision{{UnitID: "L3", Kind: kindChapter, Title: "第一章风起"}}, false},
		{"编造卷名重问", []BoundaryDecision{{UnitID: "L2", Kind: kindGroup, Title: "卷九"}}, true},
		{"附属描述性标题不核对", []BoundaryDecision{{UnitID: "L2", Kind: kindFrontMatter, Title: "引言"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := v.validate(c.bs); (err != nil) != c.wantErr {
				t.Fatalf("wantErr=%v，得 %v", c.wantErr, err)
			}
		})
	}

	// First-block start coverage: L1/L2 are non-empty with no boundary owning them → re-ask; it passes once the start boundary is supplied.
	vs := v
	vs.coverStart = true
	if err := vs.validate([]BoundaryDecision{{UnitID: "L3", Kind: kindChapter}}); err == nil {
		t.Fatal("首块起始未归属应重问")
	}
	if err := vs.validate([]BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter}, {UnitID: "L3", Kind: kindChapter},
	}); err != nil {
		t.Fatalf("起点已覆盖应通过：%v", err)
	}
	if err := vs.validate(nil); err == nil {
		t.Fatal("首块零边界应重问（全部起始文本未归属）")
	}
}

// TestSegmentClearsChunksOnResolveFailure guards the master switch against "deterministic cache
// reproduction": on a final integration failure the block cache is worthless (the digest always matches,
// so a rerun reads the same boundaries back with zero calls and dies again), so it must be cleared to buy
// a fresh segmentation opportunity; the decision snapshot lands in failures/ through errSemantic.
func TestSegmentClearsChunksOnResolveFailure(t *testing.T) {
	norm, units := segFixture()
	// The model marks the whole book as front_matter: no chapters, which Go cannot repair deterministically, so it fails at the end.
	m := &mockModel{responses: []string{boundariesJSON(boundaryFixture("L1", "", kindFrontMatter, "前言"))}}
	w := &Workspace{dir: t.TempDir()}
	_, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, w, "id-1")
	if err == nil {
		t.Fatal("无章节应终局失败")
	}
	var se *errSemantic
	if !errors.As(err, &se) {
		t.Fatalf("终局失败应为 errSemantic（统一落 failures/），得 %T", err)
	}
	if _, statErr := os.Stat(filepath.Join(w.dir, dirSegmentChunks)); !os.IsNotExist(statErr) {
		t.Fatalf("终局失败后块缓存应被清除：%v", statErr)
	}
}

// mockModel returns preset responses in order, for typed-call contract tests.
// stops can set a stop reason per call; by default it uses stop or StopReasonStop.
type mockModel struct {
	responses []string
	stops     []agentcore.StopReason
	i         int
	stop      agentcore.StopReason
}

func (m *mockModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	idx := m.i
	r := m.responses[idx%len(m.responses)]
	sr := m.stop
	if idx < len(m.stops) {
		sr = m.stops[idx]
	}
	if sr == "" {
		sr = agentcore.StopReasonStop
	}
	m.i++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(r)},
		StopReason: sr,
	}}, nil
}

// TestResolveSegmentationSingleLineChapters guards #9: in a single-line segment with no newline (the
// anchor-split case) the whole segment is the prose, and a single-line or single-line multi-chapter novel
// must not be rejected as "empty prose".
func TestResolveSegmentationSingleLineChapters(t *testing.T) {
	normalized := []byte("第一章甲的故事第二章乙的故事") // 整篇一行，无换行
	units := buildSourceUnits(normalized, 0)
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindChapter, Title: "第一章"},                // 无锚点 → byte 0
		{UnitID: "L1", Anchor: "第二章", Kind: kindChapter, Title: "第二章"}, // 行内锚点切出第二章
	}
	seg, err := resolveSegmentation(normalized, units, decisions)
	if err != nil {
		t.Fatalf("单行多章应被接受：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应切出 2 章，得 %d", len(seg.Chapters))
	}
	if got := seg.Content(normalized, 0); got != "第一章甲的故事" {
		t.Fatalf("首章正文范围不对：%q", got)
	}
}

func TestSegmentWithMockModel(t *testing.T) {
	norm, units := segFixture()
	resp := boundariesJSON(
		boundaryFixture("L1", "", kindFrontMatter, "前言"),
		boundaryFixture("L3", "", kindChapter, "第一章 风起"),
		boundaryFixture("L5", "", kindGroup, "卷二"),
		boundaryFixture("L6", "", kindChapter, "第二章 云涌"),
	)
	m := &mockModel{responses: []string{resp}}
	seg, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, nil, "")
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应得 2 章，得 %d", len(seg.Chapters))
	}
}

// TestResolveSegmentationAbsorbsEmptyChapter guards tolerance for dirty sources: real web-novel sources
// commonly carry "locked / paid chapter" placeholder titles (the title exists, the prose does not). Such a
// boundary must not fail everything — an outright veto would waste every model call of the segmentation
// stage; the placeholder span merges into the preceding one (losing not a character) and Notes records it
// for human review in the confirmation preview.
func TestResolveSegmentationAbsorbsEmptyChapter(t *testing.T) {
	norm, units := segFixture()
	// The L5 "volume two" line was marked a chapter title by the model: its span [L5,L6) has no prose → merged into chapter one.
	decisions := []BoundaryDecision{
		{UnitID: "L1", Kind: kindFrontMatter, Title: "前言"},
		{UnitID: "L3", Kind: kindChapter, Title: "第一章 风起"},
		{UnitID: "L5", Kind: kindChapter, Title: "第五章 [本章节已锁定]"},
		{UnitID: "L6", Kind: kindChapter, Title: "第二章 云涌"},
	}
	seg, err := resolveSegmentation(norm, units, decisions)
	if err != nil {
		t.Fatalf("空正文占位章应被吸收而非整体失败：%v", err)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("应得 2 章（占位并入前段），得 %d", len(seg.Chapters))
	}
	if got := seg.Content(norm, 0); !strings.Contains(got, "卷二") {
		t.Fatalf("占位段应并入第一章（文本不丢）：%q", got)
	}
	if len(seg.Notes) != 1 || !strings.Contains(seg.Notes[0], "chương khóa/trả phí") {
		t.Fatalf("应记录一条人工核对说明：%v", seg.Notes)
	}
	// The first boundary is itself an empty-prose chapter with nothing before it to merge into → it becomes front_matter, likewise without failing.
	seg, err = resolveSegmentation(norm, units, []BoundaryDecision{
		{UnitID: "L1", Kind: kindChapter, Title: "占位"}, // [L1,L2) 单行标题无正文
		{UnitID: "L2", Kind: kindChapter, Title: "第一章"},
	})
	if err != nil {
		t.Fatalf("首点空正文应落为 front_matter：%v", err)
	}
	if len(seg.Matter) != 1 || seg.Matter[0].Kind != kindFrontMatter {
		t.Fatalf("首点空正文应为 front_matter：%+v", seg.Matter)
	}
}

// TestSegmentClipsContextBoundaries guards Go-side enforcement of coordinate discipline: a boundary the
// model returns in a context region does not trigger a semantic re-ask (weak models often burn all three
// attempts and drag the block down) but is cut by code — that boundary belongs to the neighbouring block,
// which reports it within its own owned range, and keeping it would cause cross-block duplicates or
// disorder.
func TestSegmentClipsContextBoundaries(t *testing.T) {
	norm, units := segFixture()
	chunks := planChunks(units, planningBudget(40, "sys", "")) // 与 Segment 内部规划一致
	if len(chunks) < 2 {
		t.Fatalf("fixture 应分出至少 2 块，得 %d", len(chunks))
	}
	// Per-block response: one chapter boundary at the first owned unit (no title, taking the firstLine
	// fallback and sidestepping the title echo-back check — this tests coordinate discipline); the first
	// block additionally smuggles in a boundary for the next block's first unit (in the context region).
	responses := make([]string, len(chunks))
	for ci, owned := range chunks {
		boundaries := []map[string]any{boundaryFixture(units[owned[0]].ID, "", kindChapter, "")}
		if ci == 0 {
			boundaries = append(boundaries, boundaryFixture(units[chunks[1][0]].ID, "", kindChapter, ""))
		}
		responses[ci] = boundariesJSON(boundaries...)
	}
	// The clipping note echoes as ordinary progress (routine coordinate discipline, not a warning — a warn colour would make users think something went wrong).
	var clipNotes int
	prof := callProfile{progress: func(_, _ int, s string) {
		if strings.Contains(s, "cắt bỏ") {
			clipNotes++
		}
	}}
	seg, err := Segment(context.Background(), &mockModel{responses: responses}, "sys", norm, units, "", 40, 2, 4096, prof, nil, "")
	if err != nil {
		t.Fatalf("上下文区边界应被裁掉而非失败：%v", err)
	}
	if len(seg.Chapters) != len(chunks) {
		t.Fatalf("应得 %d 章（越界边界不重复计入），得 %d", len(chunks), len(seg.Chapters))
	}
	if clipNotes != 1 {
		t.Fatalf("应回显 1 条裁剪说明，得 %d", clipNotes)
	}
}

// TestSegmentReusesChunkArtifacts guards the block-level breakpoint: segmentation persists a boundary
// cache per block and a rerun reuses a digest-matching block directly at zero model calls — segmentation is
// the most expensive stage, and a failure on any block must not re-pay for finished ones (the same
// philosophy as analyze/synthesize).
func TestSegmentReusesChunkArtifacts(t *testing.T) {
	norm, units := segFixture()
	chunks := planChunks(units, planningBudget(40, "sys", "")) // 与 Segment 内部规划一致
	responses := make([]string, len(chunks))
	for ci, owned := range chunks {
		responses[ci] = boundariesJSON(boundaryFixture(units[owned[0]].ID, "", kindChapter, ""))
	}
	w := &Workspace{dir: t.TempDir()}
	m1 := &mockModel{responses: responses}
	seg1, err := Segment(context.Background(), m1, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-1")
	if err != nil {
		t.Fatalf("首跑：%v", err)
	}
	if m1.i != len(chunks) {
		t.Fatalf("首跑应调用 %d 次，得 %d", len(chunks), m1.i)
	}
	m2 := &mockModel{responses: responses}
	seg2, err := Segment(context.Background(), m2, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-1")
	if err != nil {
		t.Fatalf("重跑：%v", err)
	}
	if m2.i != 0 {
		t.Fatalf("digest 匹配的块应零调用复用，实际调用 %d 次", m2.i)
	}
	if len(seg2.Chapters) != len(seg1.Chapters) {
		t.Fatalf("复用结果应一致：%d != %d", len(seg2.Chapters), len(seg1.Chapters))
	}
	// An identity change (a new prompt version / guidance / source) → the cache misses naturally and everything is redone.
	m3 := &mockModel{responses: responses}
	if _, err := Segment(context.Background(), m3, "sys", norm, units, "", 40, 2, 4096, callProfile{}, w, "id-2"); err != nil {
		t.Fatalf("身份变化重跑：%v", err)
	}
	if m3.i != len(chunks) {
		t.Fatalf("身份变化应全部重做（%d 次调用），得 %d", len(chunks), m3.i)
	}
}

// TestSegmentShrinksChunkOnTruncation guards the output-budget feedback loop: many short chapters make a
// single block's boundary JSON exceed the visible output (stop=length), so the block must be halved and
// retried rather than failing wholesale — the same philosophy as shrinking analyze batches.
func TestSegmentShrinksChunkOnTruncation(t *testing.T) {
	norm, units := segFixture() // 7 个 unit，单块 [0,7)，mid=3
	left := boundariesJSON(boundaryFixture("L1", "", kindChapter, ""))
	right := boundariesJSON(boundaryFixture("L6", "", kindChapter, "第二章 云涌"))
	m := &mockModel{
		responses: []string{`{"boundaries":[]}`, left, right},
		stops:     []agentcore.StopReason{agentcore.StopReasonLength}, // 首调截断，两个半块正常
	}
	seg, err := Segment(context.Background(), m, "sys", norm, units, "", 0, 0, 4096, callProfile{}, nil, "")
	if err != nil {
		t.Fatalf("截断应缩块重试而非失败：%v", err)
	}
	if m.i != 3 {
		t.Fatalf("应为 1 次截断 + 2 次半块调用，得 %d", m.i)
	}
	if len(seg.Chapters) != 2 {
		t.Fatalf("缩块结果应完整覆盖（2 章），得 %d", len(seg.Chapters))
	}
}

// TestPlanningBudget guards the deduction of structural overhead from the segmentation planning budget: the owned prose is only part of the request.
func TestPlanningBudget(t *testing.T) {
	if got := planningBudget(0, "sys", "g"); got != 0 {
		t.Fatalf("无预算应透传，得 %d", got)
	}
	if got := planningBudget(1000, strings.Repeat("s", 100), strings.Repeat("g", 100)); got != 600 {
		t.Fatalf("(1000-200)*3/4 应为 600，得 %d", got)
	}
	if got := planningBudget(1000, strings.Repeat("s", 2000), ""); got != 250 {
		t.Fatalf("超长提示应触发下限 chunkBytes/4=250，得 %d", got)
	}
}

// TestBuildProjectionContextByteCap guards the context region's byte cap: an over-long line's virtual
// fragments (a single fragment can reach MaxUnitBytes) swallow the input budget, and since context is only
// reference material it shrinks to the byte cap rather than being taken wholesale.
func TestBuildProjectionContextByteCap(t *testing.T) {
	_, units := segFixture()
	if _, ids := buildProjection(units, [2]int{2, 3}, 2, 1, ""); len(ids) != 1 || !ids["L3"] {
		t.Fatalf("字节上限应裁掉上下文单元，只剩 owned：%v", ids)
	}
	if _, ids := buildProjection(units, [2]int{2, 3}, 2, 0, ""); len(ids) != 5 {
		t.Fatalf("无字节上限时应含前后各 2 个上下文单元（共 5），得 %v", ids)
	}
}

func TestCallStructuredTruncation(t *testing.T) {
	m := &mockModel{responses: []string{`{"boundaries":[]}`}, stop: agentcore.StopReasonLength}
	_, err := callStructured[boundaryBatch](context.Background(), m, segmentContract, "s", "p", 16, callProfile{}, nil)
	var trunc *errTruncated
	if err == nil || !asTruncated(err, &trunc) {
		t.Fatalf("长度截断应返回 *errTruncated，得 %v", err)
	}
}

func asTruncated(err error, target **errTruncated) bool {
	t, ok := err.(*errTruncated)
	if ok {
		*target = t
	}
	return ok
}
