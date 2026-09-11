package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// The closed set of story statuses (RFC §10.4).
const (
	storyOpen      = "open"
	storyClosed    = "closed"
	storyUncertain = "uncertain"
)

// synthesisSchemaVersion is part of the RangeDigest / synthesis InputDigest; bump it when the synthesis contract changes to invalidate persisted artifacts.
// synthesizePromptVersion is part of the synthesis InputDigest; bump it when the synthesis prompt changes, or an old synthesis would still be misjudged valid.
const (
	synthesisSchemaVersion  = 3
	synthesizePromptVersion = "synthesize-v3"
	rangePromptVersion      = "range-v2" // Feeds rangeInputDigest; bump it when the Range prompt changes, otherwise old range digests are wrongly treated as valid.
)

// ImportedArcRange / ImportedVolumeRange: synthesis returns only volume/arc ranges and never re-emits every chapter (RFC §10.3).
type ImportedArcRange struct {
	Title        string `json:"title"`
	Goal         string `json:"goal"`
	StartChapter int    `json:"start_chapter"`
	EndChapter   int    `json:"end_chapter"`
}

type ImportedVolumeRange struct {
	Title string             `json:"title"`
	Theme string             `json:"theme"`
	Arcs  []ImportedArcRange `json:"arcs"`
}

// BookSynthesis is the final synthesis result: global facts + volume/arc ranges (RFC §10.3).
type BookSynthesis struct {
	Title        *string               `json:"title"`
	Synopsis     string                `json:"synopsis"`
	Premise      string                `json:"premise"`
	Characters   []domain.Character    `json:"characters"`
	WorldRules   []domain.WorldRule    `json:"world_rules"`
	Structure    []ImportedVolumeRange `json:"structure"`
	Compass      domain.StoryCompass   `json:"compass"`
	PlanningTier domain.PlanningTier   `json:"planning_tier"`
	StoryStatus  string                `json:"story_status"`
	StatusReason string                `json:"status_reason,omitempty"`
}

// RangeDigest is the contiguous-range summary of the Map stage for a long book, with output bounded by a single range (RFC §10.2).
type RangeDigest struct {
	StartChapter    int      `json:"start_chapter"`
	EndChapter      int      `json:"end_chapter"`
	Plot            string   `json:"plot"`
	Characters      []string `json:"characters,omitempty"`
	WorldFacts      []string `json:"world_facts,omitempty"`
	OpenedThreads   []string `json:"opened_threads,omitempty"`
	ResolvedThreads []string `json:"resolved_threads,omitempty"`
}

var validPlanningTiers = map[domain.PlanningTier]bool{
	domain.PlanningTierShort: true,
	domain.PlanningTierMid:   true,
	domain.PlanningTierLong:  true,
}

// planFactRanges splits per-chapter facts into contiguous ranges by byte budget; when a short book fits in one pass, a single range goes straight to synthesis (RFC §10.2).
func planFactRanges(facts []ImportedChapterFacts, budgetBytes int) [][2]int {
	if len(facts) == 0 {
		return nil
	}
	if budgetBytes <= 0 {
		return [][2]int{{0, len(facts)}}
	}
	var ranges [][2]int
	start, acc := 0, 0
	for i, f := range facts {
		size := len(compactFact(f))
		if i > start && acc+size > budgetBytes {
			ranges = append(ranges, [2]int{start, i})
			start, acc = i, 0
		}
		acc += size
	}
	ranges = append(ranges, [2]int{start, len(facts)})
	return ranges
}

// compactView is the compact view fed to synthesis: it keeps the fields cross-chapter induction needs and
// carries no full text.
// The character/world evidence is the observation extracted during per-chapter reverse-engineering
// specifically for whole-book synthesis and must come along — otherwise the synthesiser could only invent
// official characters and world rules from summaries, wasting evidence already extracted (RFC §9.1/§10).
type compactView struct {
	Chapter           int                     `json:"chapter"`
	Title             string                  `json:"title"`
	CoreEvent         string                  `json:"core_event"`
	Summary           string                  `json:"summary"`
	Characters        []string                `json:"characters,omitempty"`
	CharacterEvidence []ImportedCharacterFact `json:"character_evidence,omitempty"`
	WorldEvidence     []ImportedWorldFact     `json:"world_evidence,omitempty"`
}

func toCompact(f ImportedChapterFacts) compactView {
	return compactView{
		Chapter:           f.Chapter,
		Title:             f.Title,
		CoreEvent:         f.CoreEvent,
		Summary:           f.Summary,
		Characters:        f.Characters,
		CharacterEvidence: f.CharacterEvidence,
		WorldEvidence:     f.WorldEvidence,
	}
}

func compactFact(f ImportedChapterFacts) string {
	data, _ := json.Marshal(toCompact(f))
	return string(data)
}

func compactFacts(facts []ImportedChapterFacts) string {
	views := make([]compactView, len(facts))
	for i, f := range facts {
		views[i] = toCompact(f)
	}
	data, _ := json.Marshal(views)
	return string(data)
}

// Synthesize is layered synthesis: a short book goes straight to BookSynthesis, a long one produces
// RangeDigests first and merges them (RFC §10).
// bookPrompt describes the BookSynthesis contract and rangePrompt the RangeDigest contract — the two stages
// have different output shapes and must use their own system prompts, or the model would get BookSynthesis
// instructions while being asked for a RangeDigest, a self-contradictory instruction.
func Synthesize(ctx context.Context, m callModel, bookPrompt, rangePrompt string, w *Workspace, facts []ImportedChapterFacts, budgetBytes, maxTokens int, prof callProfile) (*BookSynthesis, error) {
	ranges := planFactRanges(facts, budgetBytes)
	if len(ranges) <= 1 {
		return synthesizeBook(ctx, m, bookPrompt, compactFacts(facts), len(facts), maxTokens, prof)
	}
	digests := make([]RangeDigest, 0, len(ranges))
	for ri, r := range ranges {
		rangeFacts := facts[r[0]:r[1]]
		startCh, endCh := rangeFacts[0].Chapter, rangeFacts[len(rangeFacts)-1].Chapter
		want := rangeInputDigest(rangeFacts)
		rel := rangeDigestPath(startCh, endCh)
		// A persisted range digest whose InputDigest matches is reused directly, so a crash on any range of a long book costs nothing twice (RFC §6/§10.2).
		if art, err := readArtifact[RangeDigest](w, rel); err == nil && art.InputDigest == want {
			digests = append(digests, art.Payload)
			continue
		}
		prof.step(ri+1, len(ranges), "Tóm tắt khoảng %d/%d (chương %d-%d)...", ri+1, len(ranges), startCh, endCh)
		rd, err := callStructured[RangeDigest](ctx, m, rangeContract, rangePrompt, buildRangePayload(rangeFacts), maxTokens, prof, func(d *RangeDigest) error {
			return validateRangeDigest(d, startCh, endCh, "range digest")
		})
		if err != nil {
			return nil, fmt.Errorf("tổng hợp khoảng %d-%d: %w", startCh, endCh, err)
		}
		if err := writeArtifact(w, rel, want, rd); err != nil {
			return nil, fmt.Errorf("ghi range digest xuống đĩa: %w", err)
		}
		digests = append(digests, rd)
	}
	// Recursive Reduce: the total of range digests can still exceed the final synthesis input budget (which
	// pushes #83 from "every chapter" out to "every range digest").
	// Only merging level by level until it fits gives genuinely unbounded scaling (RFC §10.2).
	digests, err := reduceToFit(ctx, m, rangePrompt, digests, budgetBytes, maxTokens, prof)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(digests)
	return synthesizeBook(ctx, m, bookPrompt, string(data), len(facts), maxTokens, prof)
}

// reduceToFit repeatedly groups and merges contiguous range digests by budget until the serialised result
// fits the final BookSynthesis input budget.
// Each round strictly reduces the digest count, so it necessarily converges; a single over-budget digest is
// no longer split (the layer below is already the smallest semantic unit) and goes to the final call, where a
// resulting truncation is reported explicitly by callStructured rather than silently overflowing.
func reduceToFit(ctx context.Context, m callModel, rangePrompt string, digests []RangeDigest, budgetBytes, maxTokens int, prof callProfile) ([]RangeDigest, error) {
	round := 0
	for len(digests) > 1 {
		if budgetBytes <= 0 {
			return digests, nil
		}
		data, _ := json.Marshal(digests)
		if len(data) <= budgetBytes {
			return digests, nil
		}
		groups := groupDigestsByBudget(digests, budgetBytes)
		if len(groups) >= len(digests) {
			return digests, nil // Nothing left to merge (each group holds a single digest).
		}
		round++
		merged := make([]RangeDigest, 0, len(groups))
		for gi, g := range groups {
			startCh, endCh := g[0].StartChapter, g[len(g)-1].EndChapter
			prof.step(gi+1, len(groups), "Gộp tóm tắt khoảng (vòng %d, %d/%d, chương %d-%d)...",
				round, gi+1, len(groups), startCh, endCh)
			rd, err := callStructured[RangeDigest](ctx, m, rangeContract, rangePrompt, buildDigestReducePayload(g), maxTokens, prof, func(d *RangeDigest) error {
				return validateRangeDigest(d, startCh, endCh, "khoảng gộp")
			})
			if err != nil {
				return nil, fmt.Errorf("gộp khoảng %d-%d: %w", startCh, endCh, err)
			}
			merged = append(merged, rd)
		}
		digests = merged
	}
	return digests, nil
}

func validateRangeDigest(d *RangeDigest, startChapter, endChapter int, label string) error {
	if strings.TrimSpace(d.Plot) == "" {
		return fmt.Errorf("%s: plot rỗng", label)
	}
	if d.StartChapter != startChapter || d.EndChapter != endChapter {
		return fmt.Errorf("%s: khoảng chương %d-%d không khớp yêu cầu %d-%d", label, d.StartChapter, d.EndChapter, startChapter, endChapter)
	}
	return nil
}

// groupDigestsByBudget splits contiguous range digests into contiguous groups by byte budget; a single over-budget digest forms its own group.
func groupDigestsByBudget(digests []RangeDigest, budgetBytes int) [][]RangeDigest {
	var groups [][]RangeDigest
	var cur []RangeDigest
	acc := 0
	for _, d := range digests {
		b, _ := json.Marshal(d)
		if len(cur) > 0 && acc+len(b) > budgetBytes {
			groups = append(groups, cur)
			cur, acc = nil, 0
		}
		cur = append(cur, d)
		acc += len(b)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// buildDigestReducePayload assembles the input for "merge several lower-layer range digests into one RangeDigest".
func buildDigestReducePayload(digests []RangeDigest) string {
	data, _ := json.Marshal(digests)
	return fmt.Sprintf("Hãy gộp nhiều tóm tắt khoảng cấp dưới của chương %d-%d thành một RangeDigest (tóm tắt khoảng liên tục). Tóm tắt cấp dưới:\n%s",
		digests[0].StartChapter, digests[len(digests)-1].EndChapter, string(data))
}

// rangeDigestPath returns the relative path of a contiguous-range digest artifact.
func rangeDigestPath(startChapter, endChapter int) string {
	return fmt.Sprintf("%s/%06d-%06d.json", dirRangeDigests, startChapter, endChapter)
}

// rangeInputDigest binds that contiguous range's compact facts and the Range prompt/schema version (RFC §6.3).
func rangeInputDigest(facts []ImportedChapterFacts) string {
	return Digest([]byte(fmt.Sprintf("range\x00%s\x00v%d\x00%s", rangePromptVersion, synthesisSchemaVersion, compactFacts(facts))))
}

func synthesizeBook(ctx context.Context, m callModel, systemPrompt, payload string, n, maxTokens int, prof callProfile) (*BookSynthesis, error) {
	prof.step(0, 0, "Sinh tổng hợp toàn sách (thông tin tác phẩm/premise/characters/cấu trúc đại cương)...")
	s, err := callStructured[BookSynthesis](ctx, m, synthesisContract, systemPrompt, buildBookPayload(payload, n), maxTokens, prof, func(s *BookSynthesis) error {
		return validateSynthesis(s, n)
	})
	if err != nil {
		return nil, err
	}
	// Echoes the model's whole-book understanding: this is the import's most central semantic output and worth showing the user immediately.
	prof.step(0, 0, "Model khái quát toàn sách: %s", snippet(s.Premise, 80))
	return &s, nil
}

func buildRangePayload(facts []ImportedChapterFacts) string {
	return fmt.Sprintf("Hãy sinh một RangeDigest (tóm tắt khoảng liên tục) cho chương %d-%d. Sự thật từng chương:\n%s",
		facts[0].Chapter, facts[len(facts)-1].Chapter, compactFacts(facts))
}

func buildBookPayload(inner string, n int) string {
	return fmt.Sprintf("Dưới đây là sự thật/tóm tắt khoảng cô đọng của %d chương toàn sách. Hãy sinh BookSynthesis: title, synopsis, premise, characters, world_rules, phạm vi tập-cung structure, compass, planning_tier, story_status.\n\n%s", n, inner)
}

// validateSynthesis validates the synthesis result's structural constraints (value domains / closed sets / ranges) and never re-judges literary quality.
func validateSynthesis(s *BookSynthesis, n int) error {
	if strings.TrimSpace(s.Synopsis) == "" {
		return fmt.Errorf("synopsis rỗng")
	}
	if strings.TrimSpace(s.Premise) == "" {
		return fmt.Errorf("premise rỗng")
	}
	if len(s.Characters) == 0 {
		return fmt.Errorf("characters rỗng")
	}
	if !validPlanningTiers[s.PlanningTier] {
		return fmt.Errorf("planning_tier không hợp lệ: %q", s.PlanningTier)
	}
	switch s.StoryStatus {
	case storyOpen, storyClosed, storyUncertain:
	default:
		return fmt.Errorf("story_status không hợp lệ: %q", s.StoryStatus)
	}
	if strings.TrimSpace(s.Compass.EndingDirection) == "" {
		return fmt.Errorf("compass.ending_direction rỗng")
	}
	return validateStructure(s.Structure, n)
}

// validateStructure validates that volume/arc ranges are contiguous, non-overlapping and fully covering 1..N (RFC §11 / invariant 5).
func validateStructure(structure []ImportedVolumeRange, n int) error {
	if len(structure) == 0 {
		return fmt.Errorf("structure rỗng")
	}
	next := 1
	for vi, v := range structure {
		if len(v.Arcs) == 0 {
			return fmt.Errorf("tập[%d] %q không có cung", vi, v.Title)
		}
		for ai, a := range v.Arcs {
			if a.StartChapter != next {
				return fmt.Errorf("tập[%d] cung[%d] điểm bắt đầu %d phải là %d (phải liên tục, không có khoảng trống)", vi, ai, a.StartChapter, next)
			}
			if a.EndChapter < a.StartChapter {
				return fmt.Errorf("tập[%d] cung[%d] khoảng bị đảo ngược %d..%d", vi, ai, a.StartChapter, a.EndChapter)
			}
			next = a.EndChapter + 1
		}
	}
	if next-1 != n {
		return fmt.Errorf("phạm vi tập-cung bao phủ %d chương, phải là %d chương", next-1, n)
	}
	return nil
}

// synthesisInputDigest binds the compact facts of the ordered per-chapter analysis set plus the synthesis
// prompt/schema version (RFC §6.3 / invariant 6).
// Version included, so an old synthesis invalidates and is redone naturally when the synthesis contract
// changes.
func synthesisInputDigest(facts []ImportedChapterFacts) string {
	var b strings.Builder
	b.WriteString("synthesize\x00")
	b.WriteString(synthesizePromptVersion)
	fmt.Fprintf(&b, "\x00v%d", synthesisSchemaVersion)
	for _, f := range facts {
		b.WriteByte(0)
		b.WriteString(compactFact(f))
	}
	return Digest([]byte(b.String()))
}

// Foundation is the set of official domain objects assembled from BookSynthesis + per-chapter facts (fully validated before publication, RFC §11).
type Foundation struct {
	Book         domain.BookMetadata
	PlanningTier domain.PlanningTier
	Premise      string
	Characters   []domain.Character
	WorldRules   []domain.WorldRule
	Volumes      []domain.VolumeOutline
	Compass      domain.StoryCompass
	Closed       bool
}

// AssembleFoundation assembles the official Foundation from synthesis semantics + per-chapter facts and
// validates it fully.
// closed is the closure fact after the story_status ruling; fallbackName is the inferred title used when the
// prose cannot confirm a book title.
func AssembleFoundation(s *BookSynthesis, facts []ImportedChapterFacts, closed bool, fallbackName string) (*Foundation, error) {
	n := len(facts)
	if err := validateSynthesis(s, n); err != nil {
		return nil, err
	}
	byChapter := make(map[int]ImportedChapterFacts, n)
	for _, f := range facts {
		byChapter[f.Chapter] = f
	}

	volumes := make([]domain.VolumeOutline, 0, len(s.Structure))
	for vi, v := range s.Structure {
		vol := domain.VolumeOutline{Index: vi + 1, Title: v.Title, Theme: v.Theme}
		for ai, a := range v.Arcs {
			arc := domain.ArcOutline{Index: ai + 1, Title: a.Title, Goal: a.Goal}
			for ch := a.StartChapter; ch <= a.EndChapter; ch++ {
				f, ok := byChapter[ch]
				if !ok {
					return nil, fmt.Errorf("phạm vi cung tham chiếu chương không tồn tại %d", ch)
				}
				arc.Chapters = append(arc.Chapters, domain.OutlineEntry{
					Chapter: ch, Title: f.Title, CoreEvent: f.CoreEvent, Hook: f.Hook, Scenes: f.Scenes,
				})
			}
			vol.Arcs = append(vol.Arcs, arc)
		}
		volumes = append(volumes, vol)
	}
	if closed && len(volumes) > 0 {
		volumes[len(volumes)-1].Final = true
	}

	// After FlattenOutline the chapter count is N and titles match the per-chapter facts (RFC §11.5).
	flat := domain.FlattenOutline(volumes)
	if len(flat) != n {
		return nil, fmt.Errorf("FlattenOutline có số chương %d != %d", len(flat), n)
	}
	for _, e := range flat {
		if e.Title != byChapter[e.Chapter].Title {
			return nil, fmt.Errorf("tiêu đề chương %d không khớp sự thật từng chương", e.Chapter)
		}
	}

	title := ""
	if s.Title != nil {
		title = strings.TrimSpace(*s.Title)
	}
	if title == "" {
		title = importedBookTitle(fallbackName)
	}
	return &Foundation{
		Book:         (domain.BookMetadata{Title: title, Synopsis: s.Synopsis}).Normalized(),
		PlanningTier: s.PlanningTier,
		Premise:      s.Premise,
		Characters:   s.Characters,
		WorldRules:   s.WorldRules,
		Volumes:      volumes,
		Compass:      s.Compass,
		Closed:       closed,
	}, nil
}

// importedBookTitle uses the source filename when the prose cannot confirm a title, so the work still has a definite title.
func importedBookTitle(fallbackName string) string {
	name := strings.TrimSuffix(fallbackName, ".txt")
	name = strings.TrimSuffix(name, ".md")
	if name == "" {
		name = "Bản nhập chưa đặt tên"
	}
	return name
}
