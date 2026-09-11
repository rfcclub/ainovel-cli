package imp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// BoundaryDecision is the model's boundary judgement for one owned range (RFC §8.2).
type BoundaryDecision struct {
	UnitID    string `json:"unit_id"`
	Anchor    string `json:"anchor,omitempty"`
	Kind      string `json:"kind"` // chapter / group / front_matter / back_matter
	Title     string `json:"title,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const (
	kindChapter     = "chapter"
	kindGroup       = "group"
	kindFrontMatter = "front_matter"
	kindBackMatter  = "back_matter"
)

// boundaryBatch is the structured return of one segmentation call.
type boundaryBatch struct {
	Boundaries []BoundaryDecision `json:"boundaries"`
}

// ChapterSpan is one committable chapter after segmentation is confirmed: title + normalised-text byte range (including the title line).
type ChapterSpan struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Start  int    `json:"start_byte"`
	End    int    `json:"end_byte"`
}

// MatterSpan is a volume/part heading or an explicit auxiliary region.
type MatterSpan struct {
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Start int    `json:"start_byte"`
	End   int    `json:"end_byte"`
}

// Segmentation is a segmentation result that passed full-text coverage validation (upstream of confirmation and per-chapter analysis).
type Segmentation struct {
	Chapters  []ChapterSpan `json:"chapters"`
	Matter    []MatterSpan  `json:"matter,omitempty"`    // group / front / back
	Uncertain []int         `json:"uncertain,omitempty"` // Chapter numbers flagged uncertain, for the preview hint
	Notes     []string      `json:"notes,omitempty"`     // Notes needing manual review during segmentation (e.g. a title with empty prose merged into the previous section)
}

// Content returns the i-th chapter's normalised prose (including the title line).
func (s *Segmentation) Content(normalized []byte, i int) string {
	c := s.Chapters[i]
	return string(normalized[c.Start:c.End])
}

// resolveSegmentation maps ordered boundary decisions into a Segmentation that passed full-text
// coverage validation (RFC §8.3).
// A pure function: model output and code validation are separated, so "is this line a chapter heading"
// is never re-judged by Go, while the coverage invariants must hold.
func resolveSegmentation(normalized []byte, units []SourceUnit, decisions []BoundaryDecision) (*Segmentation, error) {
	if len(decisions) == 0 {
		return nil, fmt.Errorf("không nhận diện được ranh giới nào")
	}
	// Precondition: units must be ordered by numeric (Line,Part), never by lexicographic ID order.
	for i := 1; i < len(units); i++ {
		if !unitLess(units[i-1], units[i]) {
			return nil, fmt.Errorf("SourceUnit không xếp theo thứ tự số (Line,Part): %s đứng sau %s", units[i-1].ID, units[i].ID)
		}
	}
	unitByID := make(map[string]SourceUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}

	type point struct {
		byte int
		d    BoundaryDecision
	}
	points := make([]point, 0, len(decisions))
	for i, d := range decisions {
		switch d.Kind {
		case kindChapter, kindGroup, kindFrontMatter, kindBackMatter:
		default:
			return nil, fmt.Errorf("ranh giới[%d] kind không hợp lệ: %q", i, d.Kind)
		}
		b, err := resolveBoundaryByte(unitByID, d.UnitID, d.Anchor)
		if err != nil {
			return nil, err
		}
		points = append(points, point{byte: b, d: d})
	}
	// Occasional out-of-order or duplicate boundaries are a coordinate-discipline problem that Go fixes
	// deterministically rather than vetoing outright — discarding the whole segmentation stage after every
	// block succeeded, just because two boundaries are swapped, costs far too much (measured: 319
	// boundaries failed on one intra-block inversion, and the block cache makes that failure reproduce
	// deterministically). Inter-block order is guaranteed by non-overlapping owned ranges, so disorder can
	// only occur within a block: a stable byte sort restores the true order with zero information loss,
	// while duplicates at the same byte keep the earlier one and record Notes for human review in the
	// confirmation preview.
	sort.SliceStable(points, func(i, j int) bool { return points[i].byte < points[j].byte })
	var notes []string
	uniq := points[:0]
	for _, p := range points {
		if n := len(uniq); n > 0 && uniq[n-1].byte == p.byte {
			// An exact duplicate is mechanical redundancy and is silently deduplicated; a semantic conflict
			// at the same position (differing kind/title) was already re-queried at call time, so reaching here
			// can only mean a stale pre-fix cache — keep the earlier one and record Notes for human review.
			if prev := uniq[n-1].d; prev.Kind != p.d.Kind || boundaryLabel(prev) != boundaryLabel(p.d) {
				notes = append(notes, fmt.Sprintf("ranh giới %q trùng với %q (byte %d), đã giữ cái trước",
					boundaryLabel(prev), boundaryLabel(p.d), p.byte))
			}
			continue
		}
		uniq = append(uniq, p)
	}
	points = uniq
	// Non-empty text before the first boundary (a book-opening blurb or advert where the model missed the
	// start boundary) is not vetoed outright: Go deterministically adds a front_matter covering [0, first)
	// and records Notes for human review in the confirmation preview — the miss is already in the block
	// cache, and vetoing would make a rerun reproduce the same failure with zero calls (the same philosophy
	// as absorbing empty-prose chapters, RFC §8.3.5). The semantic judgement itself was already handed back
	// to the model at call time (chunkValidator.coverStart re-queries), so this fallback only heals stale
	// caches.
	if head := points[0].byte; head != 0 && strings.TrimSpace(string(normalized[:head])) != "" {
		notes = append(notes, fmt.Sprintf("%d byte văn bản đầu không được model gán chủ (…%s), đã thu thành front_matter, hãy kiểm tra xem có sót chương không",
			head, snippet(string(normalized[:min(head, 48)]), 24)))
		points = append([]point{{byte: 0, d: BoundaryDecision{UnitID: units[0].ID, Kind: kindFrontMatter}}}, points...)
	}

	seg := &Segmentation{Notes: notes}
	chapterNo := 0
	// absorb merges a range into the most recent span (chapter or auxiliary region alike), returning false when there is nothing to merge into.
	absorb := func(end int) bool {
		ci, mi := len(seg.Chapters)-1, len(seg.Matter)-1
		switch {
		case ci >= 0 && (mi < 0 || seg.Chapters[ci].Start > seg.Matter[mi].Start):
			seg.Chapters[ci].End = end
		case mi >= 0:
			seg.Matter[mi].End = end
		default:
			return false
		}
		return true
	}
	for i, p := range points {
		start := p.byte
		if i == 0 {
			start = 0 // The first section absorbs leading whitespace.
		}
		end := len(normalized)
		if i+1 < len(points) {
			end = points[i+1].byte
		}
		title := strings.TrimSpace(p.d.Title)
		if title == "" {
			title = firstLine(normalized, p.byte, end)
		}
		switch p.d.Kind {
		case kindChapter:
			if strings.TrimSpace(bodyAfterTitle(normalized, p.byte, end)) == "" {
				// Real web-novel sources commonly carry "locked / paid chapter" placeholders where the title
				// exists but the prose does not. This is not a total failure — an outright veto would waste every
				// model call of the segmentation stage; instead the title line merges into the preceding span
				// (not one character of text is lost) and Notes records it for the confirmation preview, and a
				// human who disagrees can rule with --guide (exactly why RFC §8.4's stop point exists).
				seg.Notes = append(seg.Notes,
					fmt.Sprintf("Tiêu đề chương %q không có chính văn (byte %d..%d), đã gộp vào đoạn trước (thường gặp ở chương khóa/trả phí chỉ có chỗ giữ chỗ)", title, start, end))
				if !absorb(end) {
					seg.Matter = append(seg.Matter, MatterSpan{Kind: kindFrontMatter, Title: title, Start: start, End: end})
				}
				continue
			}
			chapterNo++
			seg.Chapters = append(seg.Chapters, ChapterSpan{Number: chapterNo, Title: title, Start: start, End: end})
			if p.d.Uncertain {
				seg.Uncertain = append(seg.Uncertain, chapterNo)
			}
		default:
			seg.Matter = append(seg.Matter, MatterSpan{Kind: p.d.Kind, Title: title, Start: start, End: end})
		}
	}
	if chapterNo == 0 {
		return nil, fmt.Errorf("phân tách không sinh ra chương nào (group không tính là chương)")
	}
	// Duplicate chapter names are a deterministic signal of "one chapter split twice" (a source with a title
	// convention should not repeat chapter names); they are only recorded in Notes for human review in the
	// confirmation preview (non-empty Notes blocks --yes) — whether to merge is not Go's call.
	titleAt := make(map[string]int, len(seg.Chapters))
	for _, c := range seg.Chapters {
		key := squashSpace(c.Title)
		if first, ok := titleAt[key]; ok && key != "" {
			seg.Notes = append(seg.Notes, fmt.Sprintf("Chương %d và chương %d trùng tiêu đề (%q), nghi ngờ cùng một chương bị cắt nhầm, hãy kiểm tra",
				c.Number, first, snippet(c.Title, 24)))
		} else {
			titleAt[key] = c.Number
		}
	}
	return seg, nil
}

// squashSpace strips all whitespace for title echoing and duplicate-name comparison — differences in whitespace or decoration are not semantic differences.
func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// firstLine returns the first line within [start,end) with whitespace stripped.
func firstLine(normalized []byte, start, end int) string {
	s := string(normalized[start:end])
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// bodyAfterTitle returns the prose within [start,end) after the first line (the title) is removed.
// A multi-line chapter title occupies the first line with the prose after it; in a single-line segment
// with no newline (the anchor-split case) the whole segment is the prose, so the full range is returned
// rather than an empty string — otherwise a legitimate single-line, or single-line multi-chapter, novel
// would be rejected as "empty prose" (RFC §8.3).
func bodyAfterTitle(normalized []byte, start, end int) string {
	s := string(normalized[start:end])
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// planChunks splits units by byte budget into non-overlapping, fully covering owned index ranges
// [start,end).
// Chunk size is computed from the context budget, not from a fixed line or chapter count (RFC §8.1).
func planChunks(units []SourceUnit, budgetBytes int) [][2]int {
	if len(units) == 0 {
		return nil
	}
	if budgetBytes <= 0 {
		return [][2]int{{0, len(units)}}
	}
	var chunks [][2]int
	start := 0
	acc := 0
	for i, u := range units {
		size := u.EndByte - u.StartByte
		if acc > 0 && acc+size > budgetBytes {
			chunks = append(chunks, [2]int{start, i})
			start = i
			acc = 0
		}
		acc += size
	}
	chunks = append(chunks, [2]int{start, len(units)})
	return chunks
}

// buildProjection assembles the structural projection payload for one owned range (with a little
// context), and the model returns boundaries for owned units only.
// It also returns every unit_id in the projection (owned + context region) so output validation can tell
// hallucination from out-of-range.
func buildProjection(units []SourceUnit, owned [2]int, contextMargin, ctxBudget int, guidance string) (string, map[string]bool) {
	// The context region shrinks under both a unit-count and a byte cap (the unit count alone when
	// ctxBudget<=0): margin units are usually ordinary lines, but an over-long line's virtual fragments can
	// reach MaxUnitBytes and a few of them swallow the whole input budget — context is reference material
	// and not worth that price.
	lo, budget := owned[0], ctxBudget
	for lo > 0 && owned[0]-lo < contextMargin {
		if n := len(units[lo-1].Text); ctxBudget > 0 {
			if n > budget {
				break
			}
			budget -= n
		}
		lo--
	}
	hi, budget := owned[1], ctxBudget
	for hi < len(units) && hi-owned[1] < contextMargin {
		if n := len(units[hi].Text); ctxBudget > 0 {
			if n > budget {
				break
			}
			budget -= n
		}
		hi++
	}
	type projUnit struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	proj := struct {
		OwnedStart   string     `json:"owned_start"`
		OwnedEnd     string     `json:"owned_end"`
		Units        []projUnit `json:"units"`
		UserGuidance string     `json:"user_guidance,omitempty"`
	}{
		OwnedStart:   units[owned[0]].ID,
		OwnedEnd:     units[owned[1]-1].ID,
		UserGuidance: guidance,
	}
	ids := make(map[string]bool, hi-lo)
	for i := lo; i < hi; i++ {
		proj.Units = append(proj.Units, projUnit{ID: units[i].ID, Text: units[i].Text})
		ids[units[i].ID] = true
	}
	data, _ := json.MarshalIndent(proj, "", "  ")
	return string(data), ids
}

// segmentInputDigest covers the semantic inputs the segmentation action actually consumes: the normalised source, user guidance and prompt version (RFC §6.3).
func segmentInputDigest(normalizedDigest, guidance, promptVersion string) string {
	return Digest([]byte(strings.Join([]string{"segment", promptVersion, normalizedDigest, guidance}, "\x00")))
}

// segmentChunkPath / segmentChunkDigest: the artifact path and identity of the block-level boundary
// cache.
// The identity binds the segmentation identity (source + guidance + prompt version) to the block's owned
// unit range — any upstream change makes the cache miss naturally.
func segmentChunkPath(owned [2]int) string {
	return fmt.Sprintf("%s/chunk-%06d-%06d.json", dirSegmentChunks, owned[0], owned[1])
}

func segmentChunkDigest(identity, loID, hiID string) string {
	return Digest([]byte(strings.Join([]string{"segment-chunk", identity, loID, hiID}, "\x00")))
}

// Segment semantically segments the whole normalised text: it calls the model per owned range to
// identify boundaries, then validates full-text coverage.
// contextMargin is the context unit count, chunkBytes the owned range's byte budget and maxTokens the
// per-call output budget.
// A non-nil w persists a per-block boundary cache (identity = segmentInputDigest): one block can take
// minutes, and a failure on any block must not re-pay for the blocks already done — the same philosophy
// as per-chapter analyze and per-range synthesize, since segmentation used to be the only expensive stage
// with no intra-stage persistence, where one failure redid everything.
func Segment(ctx context.Context, m callModel, systemPrompt string, normalized []byte, units []SourceUnit, guidance string, chunkBytes, contextMargin, maxTokens int, prof callProfile, w *Workspace, identity string) (*Segmentation, error) {
	chunks := planChunks(units, planningBudget(chunkBytes, systemPrompt, guidance))
	unitByID := make(map[string]SourceUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}
	var decisions []BoundaryDecision
	// chunk handles one owned range: a cache hit costs zero calls; when the output is cut by length and the
	// range can be split further, halve the block and retry recursively (the boundary JSON of many short
	// chapters overflows the visible output, the same philosophy as shrinking analyze batches) — the half
	// blocks have their own cache paths so a retry never re-pays; a unit-level truncation is the only real
	// capacity shortfall.
	var chunk func(owned [2]int, cur, total int) ([]BoundaryDecision, error)
	chunk = func(owned [2]int, cur, total int) ([]BoundaryDecision, error) {
		lo, hi := units[owned[0]], units[owned[1]-1]
		rel, want := segmentChunkPath(owned), segmentChunkDigest(identity, lo.ID, hi.ID)
		if w != nil {
			if art, err := readArtifact[boundaryBatch](w, rel); err == nil && art.InputDigest == want {
				return art.Payload.Boundaries, nil
			}
		}
		// One block's model call can take minutes, so echoing per-block progress plus a running boundary
		// count keeps the panel from going silent for the whole stretch and looking hung.
		prof.step(cur, total, "Phân tách khối %d/%d (%s..%s), đã nhận diện %d ranh giới...",
			cur, total, lo.ID, hi.ID, len(decisions))
		// The context region's byte cap is chunkBytes/8 with a floor of 4096: what it stops is an over-long
		// line's virtual fragments (a single fragment can reach MaxUnitBytes) swallowing the input budget,
		// since the margin cost of ordinary lines is harmless anyway.
		payload, projIDs := buildProjection(units, owned, contextMargin, max(chunkBytes/8, 4096), guidance)
		ownedIDs := make(map[string]bool, owned[1]-owned[0])
		for i := owned[0]; i < owned[1]; i++ {
			ownedIDs[units[i].ID] = true
		}
		v := chunkValidator{projIDs: projIDs, ownedIDs: ownedIDs, unitByID: unitByID,
			normalized: normalized, coverStart: owned[0] == 0}
		batch, err := callStructured[boundaryBatch](ctx, m, segmentContract, systemPrompt, payload, maxTokens, prof, func(b *boundaryBatch) error {
			return v.validate(b.Boundaries)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) && owned[1]-owned[0] > 1 {
				mid := (owned[0] + owned[1]) / 2
				prof.step(0, 0, "Đầu ra ranh giới của khối %s..%s bị cắt (chương quá dày), chia đôi khối rồi thử lại", lo.ID, hi.ID)
				prof.logger().Warn("đầu ra phân tách của imp bị cắt, chia đôi khối", "chunk", lo.ID+".."+hi.ID)
				left, lerr := chunk([2]int{owned[0], mid}, cur, total)
				if lerr != nil {
					return nil, lerr
				}
				right, rerr := chunk([2]int{mid, owned[1]}, cur, total)
				if rerr != nil {
					return nil, rerr
				}
				return append(left, right...), nil
			}
			return nil, fmt.Errorf("phân tách khoảng %s..%s: %w", lo.ID, hi.ID, err)
		}
		// A context-region boundary belongs to the neighbouring block (which reports it again within its own
		// owned range), so Go cuts it directly: coordinate discipline is enforced by code while semantic
		// retries are reserved for genuine semantic failures — the old behaviour re-queried on an
		// out-of-range report, and a weak model would often burn all three attempts and drag the whole block
		// down (RFC §8.1, "the model owns semantics, Go owns coordinates").
		kept := make([]BoundaryDecision, 0, len(batch.Boundaries))
		for _, bd := range batch.Boundaries {
			if ownedIDs[bd.UnitID] {
				kept = append(kept, bd)
			}
		}
		if n := len(batch.Boundaries) - len(kept); n > 0 {
			// Routine coordinate discipline rather than an anomaly, so it echoes as ordinary progress — a warning colour would make users think something went wrong.
			prof.step(0, 0, "Đã cắt bỏ %d ranh giới do vùng ngữ cảnh báo thừa (để khối kề tự báo, không phải lỗi)", n)
		}
		// Echoes the model's semantic judgement (the titles it recognised) so the user sees what the model understood, not just mechanical counts.
		if len(kept) > 0 {
			prof.step(0, 0, "Model nhận diện được: %s", previewBoundaries(kept))
		}
		if w != nil {
			if err := writeArtifact(w, rel, want, boundaryBatch{Boundaries: kept}); err != nil {
				return nil, fmt.Errorf("ghi khối phân tách %s..%s xuống đĩa: %w", lo.ID, hi.ID, err)
			}
		}
		return kept, nil
	}
	for ci, owned := range chunks {
		kept, err := chunk(owned, ci+1, len(chunks))
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, kept...)
	}
	seg, err := resolveSegmentation(normalized, units, decisions)
	if err != nil {
		// On a final integration failure the block cache is worthless: a digest that always matches would
		// make a rerun read the same boundaries back with zero calls and reproduce the same failure
		// deterministically. Clearing it buys a fresh model opportunity on the next segmentation; the decision
		// snapshot goes to failures/ through errSemantic for later investigation. A failed clear must be
		// reported truthfully — claiming it was cleared would make the user's rerun read the bad cache back
		// again (Debug-First).
		hint := "đã xóa cache khối, chạy lại sẽ phân tách lại từ đầu"
		if w != nil {
			if cerr := w.clearDir(dirSegmentChunks); cerr != nil {
				hint = fmt.Sprintf("xóa cache khối thất bại: %v, trước khi chạy lại hãy xóa thủ công meta/import/segment-chunks/", cerr)
			}
		}
		raw, _ := json.MarshalIndent(decisions, "", "  ")
		return nil, &errSemantic{Raw: string(raw), Err: fmt.Errorf("tích hợp phân tách toàn sách thất bại (%s): %w", hint, err)}
	}
	return seg, nil
}

// planningBudget deducts the request's structural overhead from the input budget: the system prompt and
// guidance are deducted at their actual length, and the remainder is scaled by 3/4 to account for the
// projection JSON's wrapping overhead (ids/quotes/escapes ≈ 1/3 of the prose) — the owned prose is only
// part of the request, so planning at full budget would exceed the real input budget with a long prompt
// or a large context region. The floor chunkBytes/4 keeps an over-long prompt from squeezing the budget
// negative; chunkBytes<=0 means no budget (a single block) and is passed through unchanged.
func planningBudget(chunkBytes int, systemPrompt, guidance string) int {
	if chunkBytes <= 0 {
		return chunkBytes
	}
	b := (chunkBytes - len(systemPrompt) - len(guidance)) * 3 / 4
	return max(b, chunkBytes/4)
}

// boundaryLabel gives a boundary decision a readable identifier: the title first, falling back to kind@unit_id when there is none.
func boundaryLabel(d BoundaryDecision) string {
	if t := strings.TrimSpace(d.Title); t != "" {
		return t
	}
	return d.Kind + "@" + d.UnitID
}

// previewBoundaries compresses a batch of boundary decisions into a one-line title preview (at most 3 plus a count) for the panel to echo.
func previewBoundaries(bs []BoundaryDecision) string {
	titles := make([]string, 0, 3)
	for _, b := range bs {
		titles = append(titles, snippet(boundaryLabel(b), 24))
		if len(titles) == 3 {
			break
		}
	}
	s := strings.Join(titles, " / ")
	if len(bs) > len(titles) {
		s += fmt.Sprintf(" (tổng %d chỗ)", len(bs))
	}
	return s
}

// chunkValidator carries the call-time validation context for one segmentation call: a unit_id outside
// the projection is a hallucination; an owned-range boundary must also have a valid kind, a resolvable
// anchor and no semantic conflict at the same position; the first block must carry a boundary covering the
// text start.
// Unstopped at call time these bad values would land in the block cache — a digest that always matches
// would make a rerun read the same bad data back with zero calls and reproduce the failure
// deterministically (RFC §8.3). Semantic judgements (which one to keep, what the opening is) go back to
// the model through a re-query and Go never answers for it; context-region boundaries are bound to be cut
// by coordinate discipline and are never re-queried.
type chunkValidator struct {
	projIDs, ownedIDs map[string]bool
	unitByID          map[string]SourceUnit
	normalized        []byte
	coverStart        bool // First chunk: non-empty text before the start of the text must have a boundary owner
}

func (v chunkValidator) validate(bs []BoundaryDecision) error {
	seen := make(map[int]BoundaryDecision)
	first := -1
	for _, b := range bs {
		if b.UnitID == "" {
			return fmt.Errorf("ranh giới thiếu unit_id")
		}
		if !v.projIDs[b.UnitID] {
			return fmt.Errorf("ranh giới có unit_id %q không tồn tại trong phép chiếu lần này", b.UnitID)
		}
		if !v.ownedIDs[b.UnitID] {
			continue
		}
		switch b.Kind {
		case kindChapter, kindGroup, kindFrontMatter, kindBackMatter:
		default:
			return fmt.Errorf("ranh giới %s có kind không hợp lệ: %q (chỉ được là chapter/group/front_matter/back_matter)", b.UnitID, b.Kind)
		}
		at, err := resolveBoundaryByte(v.unitByID, b.UnitID, b.Anchor)
		if err != nil {
			return err
		}
		// Title echo-back: a chapter/group title must genuinely exist in the boundary unit's original text
		// (whitespace differences ignored) — a fabricated title is stopped here by fact (measured: in one
		// source, 67 of 157 chapters had the model invent a boundary and a title on mid-chapter continuation
		// text). Semantic discretion still belongs to the model: a source with no title convention at all may
		// set uncertain and keep an inferred title, while descriptive front/back matter titles are low risk
		// and are not checked.
		if (b.Kind == kindChapter || b.Kind == kindGroup) && !b.Uncertain {
			if t := squashSpace(b.Title); t != "" && !strings.Contains(squashSpace(v.unitByID[b.UnitID].Text), t) {
				return fmt.Errorf("Không tìm thấy tiêu đề %q của ranh giới %s trong nguyên văn của unit đó: nếu đây là phần chính văn nối tiếp của chương trước thì đừng đặt ranh giới cho nó (nó thuộc ranh giới phía trước, boundaries có thể để rỗng); nếu nguyên văn ở đây thật sự không có dòng tiêu đề mà tiêu đề do bạn tự đặt, hãy đặt uncertain=true",
					b.UnitID, snippet(b.Title, 24))
			}
		}
		// A conflict at the same position (differing kind/title) is a semantic question and which to keep is
		// not Go's call; an exact duplicate is mechanical redundancy that resolve silently deduplicates after
		// it is let through.
		if prev, ok := seen[at]; ok {
			if prev.Kind != b.Kind || boundaryLabel(prev) != boundaryLabel(b) {
				return fmt.Errorf("Hai ranh giới %q và %q rơi vào cùng một vị trí (%s), xung đột ngữ nghĩa, hãy giữ lại đúng một cái",
					boundaryLabel(prev), boundaryLabel(b), b.UnitID)
			}
		} else {
			seen[at] = b
		}
		if first < 0 || at < first {
			first = at
		}
	}
	if v.coverStart {
		head := first
		if head < 0 {
			head = len(v.normalized) // The first chunk reported no owned boundary at all: all leading text is unowned.
		}
		if head > 0 && strings.TrimSpace(string(v.normalized[:head])) != "" {
			return fmt.Errorf("%d byte văn bản đầu (…%s) không thuộc ranh giới nào, hãy bổ sung ranh giới cho phần đầu văn bản (front_matter/chapter/group)",
				head, snippet(string(v.normalized[:min(head, 48)]), 24))
		}
	}
	return nil
}
