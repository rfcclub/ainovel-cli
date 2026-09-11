package imp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"maps"
	"os"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// analysisSchemaVersion is the per-chapter fact schema version, part of InputDigest.
const analysisSchemaVersion = 2

// ImportedCharacterFact / ImportedWorldFact are compact observations for whole-book synthesis, not written
// straight to official characters or world rules.
// Each carries at least a chapter number, giving synthesis a stable provenance (RFC §9.1).
type ImportedCharacterFact struct {
	Chapter int    `json:"chapter"`
	Name    string `json:"name"`
	Note    string `json:"note,omitempty"`
}

type ImportedWorldFact struct {
	Chapter  int    `json:"chapter"`
	Category string `json:"category,omitempty"`
	Fact     string `json:"fact"`
}

// ImportedChapterFacts is the structured product of reverse-engineering one chapter (RFC §9.1).
type ImportedChapterFacts struct {
	Chapter             int                        `json:"chapter"`
	Title               string                     `json:"title"`
	Summary             string                     `json:"summary"`
	KeyEvents           []string                   `json:"key_events"`
	CoreEvent           string                     `json:"core_event"`
	Hook                string                     `json:"hook,omitempty"`
	Scenes              []string                   `json:"scenes,omitempty"`
	Characters          []string                   `json:"characters,omitempty"`
	CharacterEvidence   []ImportedCharacterFact    `json:"character_evidence,omitempty"`
	WorldEvidence       []ImportedWorldFact        `json:"world_evidence,omitempty"`
	TimelineEvents      []domain.TimelineEvent     `json:"timeline_events,omitempty"`
	ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates,omitempty"`
	RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes,omitempty"`
	StateChanges        []domain.StateChange       `json:"state_changes,omitempty"`
	HookType            string                     `json:"hook_type"`
	DominantStrand      string                     `json:"dominant_strand"`
}

// AnalysisBatchResult is the structured return of one batch call, each element one chapter's facts.
type AnalysisBatchResult struct {
	Chapters []ImportedChapterFacts `json:"chapters"`
}

// ChapterAnalysisPayload is one chapter-analysis artifact payload; chapters from the same batch record the same BatchStart/BatchEnd.
type ChapterAnalysisPayload struct {
	BatchStart int                  `json:"batch_start"`
	BatchEnd   int                  `json:"batch_end"`
	Facts      ImportedChapterFacts `json:"facts"`
}

// AnalyzeBudget is per-chapter analysis's dual input/output budget (RFC §9.2).
// Input approximates the context window in bytes; output approximates the completion cap from a conservative per-chapter fact reserve.
type AnalyzeBudget struct {
	ContextBytes     int // Input budget (prose + ledger + overhead)
	MaxOutputTokens  int // Visible output budget (completion cap)
	PerChapterOutput int // Conservative per-chapter output reservation
	PromptOverhead   int // Fixed input overhead for system/ledger (bytes)
}

func analysisPath(chapter int) string {
	return fmt.Sprintf("%s/%06d.json", dirAnalyses, chapter)
}

// analyzedChapters returns the number of analysis artifacts that are contiguous from chapter 1 and whose
// InputDigest matches the current segmentation identity/version/prose (RFC §9.6).
// A miss, a parse failure or a digest mismatch truncates here, so an upstream change (re-segmentation, a
// prompt/schema version bump) invalidates downstream analysis naturally.
func analyzedChapters(w *Workspace, seg *Segmentation, normalized []byte, segIdentity, promptVersion string) int {
	n := 0
	for c := 1; c <= len(seg.Chapters); c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			break
		}
		if a.InputDigest != chapterInputDigest(segIdentity, promptVersion, seg, normalized, c-1) {
			break
		}
		n++
	}
	return n
}

// analyzedChaptersStrict has the same freshness semantics as analyzedChapters but surfaces corrupt or
// unreadable existing artifacts. State recovery uses the strict version so a genuine read error is not taken
// for "not analysed yet" and overwritten.
func analyzedChaptersStrict(w *Workspace, seg *Segmentation, normalized []byte, segIdentity, promptVersion string) (int, error) {
	n := 0
	for c := 1; c <= len(seg.Chapters); c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return n, fmt.Errorf("đọc sản phẩm phân tích chương %d: %w", c, err)
		}
		if a.InputDigest != chapterInputDigest(segIdentity, promptVersion, seg, normalized, c-1) {
			break
		}
		n++
	}
	return n, nil
}

// discardAnalysesAfter deletes per-chapter analysis artifacts with a chapter number > keep, making
// "re-analysing one chapter invalidates every analysis after it" hold (#4a).
// In ordinary forward analysis there are already no artifacts past keep, so it is an idempotent no-op; it
// only clears a stale tail during a mid-run re-analysis that crosses the fresh prefix.
// A failed delete must propagate: this is the only enforcement point for that invariant, and swallowing the
// error would let the stale tail (whose per-chapter digests still match) be reused as a fresh prefix,
// leaving synthesis to consume facts stitched from old and new with no error at all.
func discardAnalysesAfter(w *Workspace, keep, total int) error {
	for c := keep + 1; c <= total; c++ {
		if err := os.Remove(w.path(analysisPath(c))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("dọn sản phẩm phân tích cũ %s: %w", analysisPath(c), err)
		}
	}
	return nil
}

// loadPriorFacts reads the persisted facts for chapters 1..count for ledger construction.
func loadPriorFacts(w *Workspace, count int) []ImportedChapterFacts {
	var out []ImportedChapterFacts
	for c := 1; c <= count; c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			break
		}
		out = append(out, a.Payload.Facts)
	}
	return out
}

func loadPriorFactsStrict(w *Workspace, count int) ([]ImportedChapterFacts, error) {
	out := make([]ImportedChapterFacts, 0, count)
	for c := 1; c <= count; c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			return out, fmt.Errorf("đọc sự thật phân tích chương %d: %w", c, err)
		}
		out = append(out, a.Payload.Facts)
	}
	return out, nil
}

// buildLedger derives a compact continuity context from analysed chapters: character aliases + active foreshadow IDs + recent state.
func buildLedger(prior []ImportedChapterFacts) string {
	if len(prior) == 0 {
		return ""
	}
	names := map[string]bool{}
	active := map[string]string{} // foreshadow id -> desc
	var recent []string
	for _, f := range prior {
		for _, c := range f.Characters {
			names[c] = true
		}
		for _, fu := range f.ForeshadowUpdates {
			switch fu.Action {
			case "plant", "advance":
				if fu.Description != "" {
					active[fu.ID] = fu.Description
				} else if _, ok := active[fu.ID]; !ok {
					active[fu.ID] = ""
				}
			case "resolve":
				delete(active, fu.ID)
			}
		}
	}
	if len(prior) > 0 {
		last := prior[len(prior)-1]
		for _, sc := range last.StateChanges {
			recent = append(recent, fmt.Sprintf("%s.%s=%s", sc.Entity, sc.Field, sc.NewValue))
		}
	}
	var b strings.Builder
	if len(names) > 0 {
		b.WriteString("Nhân vật đã biết: ")
		b.WriteString(strings.Join(slices.Sorted(maps.Keys(names)), "、"))
		b.WriteString("\n")
	}
	if len(active) > 0 {
		b.WriteString("Phục bút đang hoạt động (tái sử dụng ID, đừng tạo mới):\n")
		for _, id := range slices.Sorted(maps.Keys(active)) {
			fmt.Fprintf(&b, "- %s：%s\n", id, active[id])
		}
	}
	if len(recent) > 0 {
		b.WriteString("Trạng thái gần nhất: ")
		b.WriteString(strings.Join(recent, "；"))
		b.WriteString("\n")
	}
	return b.String()
}

// planBatch returns the contiguous batch end from chapter start under the dual input/output budget
// ([start,end), chapter index from 0).
// At least one chapter: even an over-budget single chapter becomes its own batch, with the executor
// reporting insufficient capacity if it truncates (RFC §9.2).
func planBatch(chapters []ChapterSpan, start, ledgerBytes int, b AnalyzeBudget) int {
	end := start + 1
	if b.ContextBytes <= 0 || b.MaxOutputTokens <= 0 || b.PerChapterOutput <= 0 {
		return end // No budget configured: go chapter by chapter.
	}
	inAcc := ledgerBytes + b.PromptOverhead + chapterBytes(chapters, start)
	outAcc := b.PerChapterOutput
	for end < len(chapters) {
		cb := chapterBytes(chapters, end)
		if inAcc+cb > b.ContextBytes {
			break
		}
		if outAcc+b.PerChapterOutput > b.MaxOutputTokens {
			break
		}
		inAcc += cb
		outAcc += b.PerChapterOutput
		end++
	}
	return end
}

func chapterBytes(chapters []ChapterSpan, i int) int {
	return chapters[i].End - chapters[i].Start
}

// chapterInputDigest binds analysis artifact identity per chapter: segmentation identity + prompt/schema
// version + chapter number + that chapter's prose.
// Per-chapter rather than per-batch — batch division is an execution detail that moves with model capability
// and must not invalidate every analysed chapter when the model changes; binding segIdentity (the
// segmentation artifact's InputDigest) ensures every analysis misses naturally after re-segmentation (RFC
// §9.1/§6.3).
func chapterInputDigest(segIdentity, promptVersion string, seg *Segmentation, normalized []byte, i int) string {
	var b strings.Builder
	b.WriteString("analyze\x00")
	b.WriteString(promptVersion)
	fmt.Fprintf(&b, "\x00v%d\x00", analysisSchemaVersion)
	b.WriteString(segIdentity)
	fmt.Fprintf(&b, "\x00ch%d\x00", seg.Chapters[i].Number)
	b.WriteString(seg.Content(normalized, i))
	return Digest([]byte(b.String()))
}

// validateBatch validates in two layers: batch-level contiguity with no gaps or duplicates, and per-chapter value domains and references (RFC §9.4).
func validateBatch(r *AnalysisBatchResult, seg *Segmentation, start, end int) error {
	want := end - start
	if len(r.Chapters) != want {
		return fmt.Errorf("số chương của lô %d != mong đợi %d", len(r.Chapters), want)
	}
	for i, f := range r.Chapters {
		want := seg.Chapters[start+i]
		if f.Chapter != want.Number {
			return fmt.Errorf("mục thứ %d của lô có số chương %d != %d", i, f.Chapter, want.Number)
		}
		if strings.TrimSpace(f.Summary) == "" || strings.TrimSpace(f.CoreEvent) == "" {
			return fmt.Errorf("chương %d: summary/core_event không được để trống", f.Chapter)
		}
		if !domain.ValidHookType(strings.ToLower(f.HookType)) {
			return fmt.Errorf("chương %d: hook_type không hợp lệ: %q", f.Chapter, f.HookType)
		}
		if !domain.ValidDominantStrand(strings.ToLower(f.DominantStrand)) {
			return fmt.Errorf("chương %d: dominant_strand không hợp lệ: %q", f.Chapter, f.DominantStrand)
		}
		for j, fu := range f.ForeshadowUpdates {
			if fu.Action == "plant" && strings.TrimSpace(fu.Description) == "" {
				return fmt.Errorf("chương %d foreshadow[%d] khi plant cần description", f.Chapter, j)
			}
		}
		// An enum validated in lowercase is persisted in lowercase: commit_chapter does not re-validate enums,
		// and a case variant would pass straight into official state (HookHistory and the like consume exact
		// strings, so a variant reads as an unknown type) — validation success normalises it.
		r.Chapters[i].HookType = strings.ToLower(f.HookType)
		r.Chapters[i].DominantStrand = strings.ToLower(f.DominantStrand)
	}
	return nil
}

// AnalyzeNext builds one batch from the first missing analysis and persists it atomically, returning the
// number of chapters committed.
// Truncation means "fail + shrink and re-batch" by default (§9.5); if the batch is down to a single chapter
// and still truncates, it reports insufficient capacity explicitly.
func AnalyzeNext(ctx context.Context, m callModel, systemPrompt string, w *Workspace, normalized []byte, seg *Segmentation, segIdentity, promptVersion string, budget AnalyzeBudget, prof callProfile) (int, error) {
	total := len(seg.Chapters)
	start := analyzedChapters(w, seg, normalized, segIdentity, promptVersion)
	if start >= total {
		return 0, nil
	}
	ledger := buildLedger(loadPriorFacts(w, start))
	end := planBatch(seg.Chapters, start, len(ledger), budget)

	for {
		payload := buildAnalyzePayload(normalized, seg, ledger, start, end)
		res, err := callStructured[AnalysisBatchResult](ctx, m, analysisContract, systemPrompt, payload, budget.MaxOutputTokens, prof, func(r *AnalysisBatchResult) error {
			return validateBatch(r, seg, start, end)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) {
				// On truncation the largest contiguous valid prefix from the batch's first chapter is salvaged first, and what was committed is not redone (§9.5).
				if salvaged := salvagePrefix(tr.Raw, seg, start); len(salvaged) > 0 {
					for i, f := range salvaged {
						ch := start + i + 1
						digest := chapterInputDigest(segIdentity, promptVersion, seg, normalized, start+i)
						art := ChapterAnalysisPayload{BatchStart: start + 1, BatchEnd: end, Facts: f}
						if werr := writeArtifact(w, analysisPath(ch), digest, art); werr != nil {
							return i, fmt.Errorf("ghi chương vớt được %d xuống đĩa: %w", ch, werr)
						}
					}
					w.writeFailure(FailureMeta{Stage: "analyze", Detail: fmt.Sprintf("lô %d-%d bị cắt do độ dài", start+1, end),
						StopReason: "length", PrefixSalvage: fmt.Sprintf("available:%d", len(salvaged))}, tr.Raw)
					prof.logger().Info("phân tích imp bị cắt, vớt tiền tố liên tục", "batch_start", start+1, "salvaged", len(salvaged))
					echoChapterFacts(prof, salvaged)
					return len(salvaged), nil
				}
				// No salvageable prefix: record it as unavailable and "fail + shrink and re-batch"; a single chapter
				// still truncating reports insufficient capacity.
				w.writeFailure(FailureMeta{Stage: "analyze", Detail: fmt.Sprintf("lô %d-%d bị cắt do độ dài, không có tiền tố nào vớt được", start+1, end),
					StopReason: "length", PrefixSalvage: "unavailable"}, tr.Raw)
				if end-start > 1 {
					prof.logger().Warn("phân tích imp bị cắt, thu nhỏ rồi gộp lô lại", "batch", fmt.Sprintf("%d-%d", start+1, end), "prefix_salvage", "unavailable")
					end = start + (end-start)/2
					// A progress row without a Key: it shows the user the batch shrinking while also preventing the
					// backoff rows of two independent calls from being wrongly merged under one Key (the Key contract
					// covers only transient backoff within a single call).
					prof.step(0, 0, "Đầu ra bị cắt do độ dài và không có tiền tố vớt được, thu nhỏ lô thành chương %d-%d rồi thử lại", start+1, end)
					continue
				}
				return 0, fmt.Errorf("lô một chương của chương %d vẫn bị cắt do độ dài, năng lực đầu ra khả kiến của model không đủ", start+1)
			}
			return 0, err
		}
		for i, f := range res.Chapters {
			ch := start + i + 1
			digest := chapterInputDigest(segIdentity, promptVersion, seg, normalized, start+i)
			payloadArt := ChapterAnalysisPayload{BatchStart: start + 1, BatchEnd: end, Facts: f}
			if err := writeArtifact(w, analysisPath(ch), digest, payloadArt); err != nil {
				return i, fmt.Errorf("ghi phân tích chương %d xuống đĩa: %w", ch, err)
			}
		}
		echoChapterFacts(prof, res.Chapters)
		return end - start, nil
	}
}

// echoChapterFacts echoes the model's core understanding of each chapter into the panel — the user should
// see what the model understood, not just mechanical batch counts (§14.1).
func echoChapterFacts(prof callProfile, facts []ImportedChapterFacts) {
	for _, f := range facts {
		prof.step(0, 0, "Chương %d <%s>: %s", f.Chapter, snippet(f.Title, 24), snippet(f.CoreEvent, 60))
	}
}

// buildAnalyzePayload assembles the batch input: the contiguous chapters' original text + the pre-batch ledger.
func buildAnalyzePayload(normalized []byte, seg *Segmentation, ledger string, start, end int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hãy phân tích chương %d-%d, trả về {\"chapters\":[mỗi chương một đối tượng sự thật]}, thứ tự mảng khớp số chương.\n\n", start+1, end)
	if ledger != "" {
		b.WriteString("## Sổ cái liên tục (tham khảo)\n\n")
		b.WriteString(ledger)
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		c := seg.Chapters[i]
		fmt.Fprintf(&b, "## Chương %d: %s\n\n", c.Number, c.Title)
		b.WriteString(seg.Content(normalized, i))
		b.WriteString("\n\n---\n\n")
	}
	return b.String()
}

// salvagePrefix parses the largest contiguous valid prefix out of a length-truncated batch response (RFC
// §9.5).
// It saves only objects contiguous from the batch's first chapter that pass per-chapter validation; it stops
// at the first incomplete, invalid or skipped-number object and interprets no bytes after it.
// A pure function, called by AnalyzeNext first on a capacity truncation so fully generated prefix chapters
// are not discarded.
func salvagePrefix(raw string, seg *Segmentation, start int) []ImportedChapterFacts {
	arr := extractChaptersArray(raw)
	if arr == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(arr))
	if _, err := dec.Token(); err != nil { // Consume '['.
		return nil
	}
	var out []ImportedChapterFacts
	for dec.More() {
		var f ImportedChapterFacts
		if err := dec.Decode(&f); err != nil {
			break // First incomplete object: stop.
		}
		idx := start + len(out)
		if idx >= len(seg.Chapters) || f.Chapter != seg.Chapters[idx].Number {
			break // Gap or out of range.
		}
		one := AnalysisBatchResult{Chapters: []ImportedChapterFacts{f}}
		if err := validateBatch(&one, seg, idx, idx+1); err != nil {
			break
		}
		out = append(out, one.Chapters[0]) // validateBatch already normalised the enums in place; take the validated value.
	}
	return out
}

// extractChaptersArray extracts the JSON array text after "chapters" (possibly truncated at the tail).
func extractChaptersArray(raw string) string {
	i := strings.Index(raw, "\"chapters\"")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(raw[i:], '[')
	if j < 0 {
		return ""
	}
	return raw[i+j:]
}
