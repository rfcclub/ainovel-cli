package store

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/rules"
)

// WorldStore manages timelines, foreshadows, character relationships, state changes, world rules, style rules, reviews and handover.
type WorldStore struct {
	io *IO

	timeline                *appendLog[domain.TimelineEvent]
	stateChanges            *appendLog[domain.StateChange]
	timelineProjectionReady bool
}

func NewWorldStore(io *IO) *WorldStore {
	return &WorldStore{
		io: io,
		timeline: newAppendLog(
			"timeline.jsonl",
			"timeline.json",
			timelineEventKey,
			cloneTimelineEvent,
		),
		stateChanges: newAppendLog(
			"meta/state_changes.jsonl",
			"meta/state_changes.json",
			stateChangeKey,
			func(change domain.StateChange) domain.StateChange { return change },
		),
	}
}

// ── Timeline ──

// SaveTimeline replaces the timeline facts and the human-readable projection wholesale.
func (s *WorldStore) SaveTimeline(events []domain.TimelineEvent) error {
	return s.io.WithWriteLock(func() error {
		if err := s.timeline.replaceUnlocked(s.io, events); err != nil {
			s.timelineProjectionReady = false
			return err
		}
		if err := s.io.WriteMarkdownUnlocked("timeline.md", renderTimeline(events, s.io.labels())); err != nil {
			s.timelineProjectionReady = false
			return err
		}
		s.timelineProjectionReady = true
		return nil
	})
}

// LoadTimeline reads the timeline.
func (s *WorldStore) LoadTimeline() ([]domain.TimelineEvent, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.timeline.allUnlocked(s.io)
}

// AppendTimelineEvents appends timeline events. A resubmitted event is deduplicated by stable key, so a rerun after a
// commit_chapter crash does not pollute the timeline.
func (s *WorldStore) AppendTimelineEvents(newEvents []domain.TimelineEvent) error {
	return s.io.WithWriteLock(func() error {
		if !s.timelineProjectionReady {
			existing, err := s.timeline.allUnlocked(s.io)
			if err != nil {
				return err
			}
			if err := s.ensureTimelineProjectionUnlocked(existing); err != nil {
				return err
			}
		}

		added, err := s.timeline.appendUnlocked(s.io, newEvents)
		if err != nil {
			// On an append error the disk may already hold some complete records, so the projection must be rebuilt from
			// the fact log before replaying rather than relying on the `added` return value alone.
			s.timelineProjectionReady = false
			return err
		}
		if len(added) == 0 {
			return nil
		}
		if err := s.io.AppendLineUnlocked("timeline.md", []byte(renderTimelineEntries(added, s.io.labels()))); err != nil {
			s.timelineProjectionReady = false
			return err
		}
		return nil
	})
}

// ensureTimelineProjectionUnlocked reconciles timeline.md on the process's first append or after a failed projection.
// The normal path does one full reconciliation and thereafter appends in step with the JSONL per chapter; the
// projection takes no part in fact reads.
func (s *WorldStore) ensureTimelineProjectionUnlocked(events []domain.TimelineEvent) error {
	if s.timelineProjectionReady {
		return nil
	}
	expected := renderTimeline(events, s.io.labels())
	actual, err := s.io.ReadFileUnlocked("timeline.md")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if string(actual) != expected {
		if err := s.io.WriteMarkdownUnlocked("timeline.md", expected); err != nil {
			return err
		}
	}
	s.timelineProjectionReady = true
	return nil
}

// LoadRecentTimeline returns timeline events within the last window chapters.
func (s *WorldStore) LoadRecentTimeline(current, window int) ([]domain.TimelineEvent, error) {
	all, err := s.LoadTimeline()
	if err != nil {
		return nil, err
	}
	minCh := max(current-window, 1)
	var filtered []domain.TimelineEvent
	for _, e := range all {
		if e.Chapter >= minCh {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// ── Foreshadows ──

// SaveForeshadowLedger writes foreshadow_ledger.json + foreshadow_ledger.md wholesale (atomic write).
func (s *WorldStore) SaveForeshadowLedger(entries []domain.ForeshadowEntry) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(entries, s.io.labels()))
	})
}

// LoadForeshadowLedger reads the foreshadow ledger.
func (s *WorldStore) LoadForeshadowLedger() ([]domain.ForeshadowEntry, error) {
	var entries []domain.ForeshadowEntry
	if err := s.io.ReadJSON("foreshadow_ledger.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// UpdateForeshadow applies foreshadow increment operations in a batch.
func (s *WorldStore) UpdateForeshadow(chapter int, updates []domain.ForeshadowUpdate) error {
	return s.io.WithWriteLock(func() error {
		var entries []domain.ForeshadowEntry
		if err := s.io.ReadJSONUnlocked("foreshadow_ledger.json", &entries); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(entries))
		for i, e := range entries {
			idx[e.ID] = i
		}
		for _, u := range updates {
			if strings.TrimSpace(u.ID) == "" {
				return fmt.Errorf("foreshadow id không được để trống")
			}
			switch u.Action {
			case "plant":
				if strings.TrimSpace(u.Description) == "" {
					return fmt.Errorf("plant foreshadow %q requires description", u.ID)
				}
				if i, ok := idx[u.ID]; ok {
					// A repeated plant from the same chapter is a full replay of chapter records and must be idempotent; the
					// same id colliding from a different chapter means a new foreshadow is being blocked by an old entry —
					// this used to `continue` unconditionally, silently discarding the new foreshadow along with its
					// description. Measured on a 22-chapter book: six plants landed as only two entries because the model
					// reused the same placeholder ids ("1" and "new") each time, and four foreshadows evaporated without an
					// error anywhere.
					if planted := entries[i].PlantedAt; planted != 0 && planted != chapter {
						return fmt.Errorf(
							"ID phục bút %q đã bị chương %d chiếm (%s); mỗi phục bút cần một ID riêng, duy nhất và ổn định, "+
								"đừng dùng lại các chỗ giữ chỗ kiểu \"new\"/\"1\" — các chương sau dựa vào ID để tìm lại nó mà advance/resolve: %w",
							u.ID, planted, truncateForError(entries[i].Description), errs.ErrToolArgs)
					}
					if entries[i].Description == "" {
						entries[i].Description = u.Description
					}
					if entries[i].PlantedAt == 0 {
						entries[i].PlantedAt = chapter
					}
					if entries[i].Status == "" {
						entries[i].Status = "planted"
					}
					continue
				}
				idx[u.ID] = len(entries)
				entries = append(entries, domain.ForeshadowEntry{
					ID:          u.ID,
					Description: u.Description,
					PlantedAt:   chapter,
					Status:      "planted",
				})
			case "advance":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "advanced"
				} else {
					return fmt.Errorf("advance unknown foreshadow %q", u.ID)
				}
			case "resolve":
				if i, ok := idx[u.ID]; ok {
					entries[i].Status = "resolved"
					entries[i].ResolvedAt = chapter
				} else {
					return fmt.Errorf("resolve unknown foreshadow %q", u.ID)
				}
			default:
				return fmt.Errorf("invalid foreshadow action %q", u.Action)
			}
		}
		if err := s.io.WriteJSONUnlocked("foreshadow_ledger.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("foreshadow_ledger.md", renderForeshadow(entries, s.io.labels()))
	})
}

// LoadActiveForeshadow returns unrecovered foreshadow entries.
func (s *WorldStore) LoadActiveForeshadow() ([]domain.ForeshadowEntry, error) {
	all, err := s.LoadForeshadowLedger()
	if err != nil {
		return nil, err
	}
	var active []domain.ForeshadowEntry
	for _, e := range all {
		if e.Status != "resolved" {
			active = append(active, e)
		}
	}
	return active, nil
}

// ── Character relationships ──

// SaveRelationships writes relationship_state.json + relationship_state.md wholesale (atomic write).
func (s *WorldStore) SaveRelationships(entries []domain.RelationshipEntry) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("relationship_state.json", entries); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("relationship_state.md", renderRelationships(entries, s.io.labels()))
	})
}

// LoadRelationships reads the character relationship state.
func (s *WorldStore) LoadRelationships() ([]domain.RelationshipEntry, error) {
	var entries []domain.RelationshipEntry
	if err := s.io.ReadJSON("relationship_state.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// UpdateRelationships merges relationship changes.
func (s *WorldStore) UpdateRelationships(changes []domain.RelationshipEntry) error {
	return s.io.WithWriteLock(func() error {
		var existing []domain.RelationshipEntry
		if err := s.io.ReadJSONUnlocked("relationship_state.json", &existing); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}
		idx := make(map[string]int, len(existing))
		for i, e := range existing {
			idx[pairKey(e.CharacterA, e.CharacterB)] = i
		}
		for _, c := range changes {
			key := pairKey(c.CharacterA, c.CharacterB)
			if i, ok := idx[key]; ok {
				existing[i].Relation = c.Relation
				existing[i].Chapter = c.Chapter
			} else {
				idx[key] = len(existing)
				existing = append(existing, c)
			}
		}
		if err := s.io.WriteJSONUnlocked("relationship_state.json", existing); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("relationship_state.md", renderRelationships(existing, s.io.labels()))
	})
}

// ── State changes ──

// AppendStateChanges appends character state changes. A resubmitted change is deduplicated by stable key.
func (s *WorldStore) AppendStateChanges(changes []domain.StateChange) error {
	return s.io.WithWriteLock(func() error {
		_, err := s.stateChanges.appendUnlocked(s.io, changes)
		return err
	})
}

// LoadStateChanges reads every state-change record.
func (s *WorldStore) LoadStateChanges() ([]domain.StateChange, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.stateChanges.allUnlocked(s.io)
}

// SaveStateChanges replaces the state-change facts wholesale, rebuilding the projection after a chapter revision.
func (s *WorldStore) SaveStateChanges(changes []domain.StateChange) error {
	return s.io.WithWriteLock(func() error {
		return s.stateChanges.replaceUnlocked(s.io, changes)
	})
}

// ── World rules ──

// SaveWorldRules writes world_rules.json + world_rules.md wholesale (atomic write).
func (s *WorldStore) SaveWorldRules(rules []domain.WorldRule) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("world_rules.json", rules); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("world_rules.md", renderWorldRules(rules, s.io.labels()))
	})
}

// LoadWorldRules reads the world rules.
func (s *WorldStore) LoadWorldRules() ([]domain.WorldRule, error) {
	var rules []domain.WorldRule
	if err := s.io.ReadJSON("world_rules.json", &rules); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return rules, nil
}

// ── Style rules ──

// SaveStyleRules saves the writing style rules.
func (s *WorldStore) SaveStyleRules(rules domain.WritingStyleRules) error {
	return s.io.WriteJSON("meta/style_rules.json", rules)
}

// LoadStyleRules reads the writing style rules.
func (s *WorldStore) LoadStyleRules() (*domain.WritingStyleRules, error) {
	var rules domain.WritingStyleRules
	if err := s.io.ReadJSON("meta/style_rules.json", &rules); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &rules, nil
}

func (s *WorldStore) SaveAuthorRevisionStyle(style domain.AuthorRevisionStyle) error {
	return s.io.WriteJSON("meta/author_revision_style.json", style)
}

func (s *WorldStore) LoadAuthorRevisionStyle() (*domain.AuthorRevisionStyle, error) {
	var style domain.AuthorRevisionStyle
	if err := s.io.ReadJSON("meta/author_revision_style.json", &style); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &style, nil
}

// ── Reviews ──

// SaveReview saves a review result.
func (s *WorldStore) SaveReview(r domain.ReviewEntry) error {
	rel := fmt.Sprintf("reviews/%02d.json", r.Chapter)
	if r.Scope == "global" {
		rel = fmt.Sprintf("reviews/%02d-global.json", r.Chapter)
	}
	return s.io.WriteJSON(rel, r)
}

// HasArcReview checks whether the given chapter (an arc-end chapter) already has a scope=arc review saved.
func (s *WorldStore) HasArcReview(chapter int) (bool, error) {
	rv, err := s.LoadReview(chapter)
	if err != nil {
		return false, err
	}
	return rv != nil && rv.Scope == "arc", nil
}

// HasGlobalReview checks whether the given chapter already has a scope=global review saved
// (save_review writes reviews/%02d-global.json; a non-layered book triggers it on ReviewInterval).
func (s *WorldStore) HasGlobalReview(chapter int) (bool, error) {
	r, err := s.LoadGlobalReview(chapter)
	if err != nil {
		return false, err
	}
	return r != nil && r.Scope == "global", nil
}

// LoadGlobalReview reads the global review ending at the given chapter.
func (s *WorldStore) LoadGlobalReview(chapter int) (*domain.ReviewEntry, error) {
	var r domain.ReviewEntry
	if err := s.io.ReadJSON(fmt.Sprintf("reviews/%02d-global.json", chapter), &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadReview reads a chapter review result.
func (s *WorldStore) LoadReview(chapter int) (*domain.ReviewEntry, error) {
	var r domain.ReviewEntry
	if err := s.io.ReadJSON(fmt.Sprintf("reviews/%02d.json", chapter), &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadLastReview reads the most recent global review.
func (s *WorldStore) LoadLastReview(fromChapter int) (*domain.ReviewEntry, error) {
	for ch := fromChapter; ch >= 1; ch-- {
		var r domain.ReviewEntry
		if err := s.io.ReadJSON(fmt.Sprintf("reviews/%02d-global.json", ch), &r); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		return &r, nil
	}
	return nil, nil
}

// LoadReviewsAffectingChapter returns every review that explicitly put chapter into the rework queue, newest first.
// Arc and global reviews live at the review's end point and can no longer be found by the target chapter's filename.
func (s *WorldStore) LoadReviewsAffectingChapter(chapter int) ([]domain.ReviewEntry, error) {
	entries, err := os.ReadDir(s.io.path("reviews"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var reviews []domain.ReviewEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(s.io.path("reviews/" + entry.Name()))
		if err != nil {
			return nil, err
		}
		var review domain.ReviewEntry
		if err := json.Unmarshal(data, &review); err != nil {
			return nil, fmt.Errorf("parse reviews/%s: %w", entry.Name(), err)
		}
		if slices.Contains(review.AffectedChapters, chapter) ||
			(review.Scope == "chapter" && review.Chapter == chapter && review.Verdict != "accept" && len(review.AffectedChapters) == 0) {
			reviews = append(reviews, review)
		}
	}
	slices.SortFunc(reviews, func(a, b domain.ReviewEntry) int {
		return b.Chapter - a.Chapter
	})
	return reviews, nil
}

// ── render helpers ──

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func timelineEventKey(e domain.TimelineEvent) string {
	chars := append([]string(nil), e.Characters...)
	slices.Sort(chars)
	parts := make([]string, 0, len(chars)+2)
	parts = append(parts, e.Time, e.Event)
	parts = append(parts, chars...)
	return stableRecordKey(e.Chapter, parts...)
}

func cloneTimelineEvent(event domain.TimelineEvent) domain.TimelineEvent {
	event.Characters = append([]string(nil), event.Characters...)
	return event
}

func stateChangeKey(c domain.StateChange) string {
	return stableRecordKey(c.Chapter, c.Entity, c.Field, c.OldValue, c.NewValue)
}

// stableRecordKey length-prefixes variable text so a separator inside the content cannot cause a dedup collision.
func stableRecordKey(chapter int, parts ...string) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(chapter))
	for _, part := range parts {
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String()
}

func renderTimeline(events []domain.TimelineEvent, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.timeline)
	b.WriteString(renderTimelineEntries(events, l))
	return b.String()
}

func renderTimelineEntries(events []domain.TimelineEvent, l mdLabels) string {
	var b strings.Builder
	for _, e := range events {
		chars := ""
		if len(e.Characters) > 0 {
			chars = l.openParen + strings.Join(e.Characters, l.listSep) + l.closeParen
		}
		fmt.Fprintf(&b, "- **"+l.chapterFmt+" [%s]**%s%s%s\n", e.Chapter, e.Time, l.colon, e.Event, chars)
	}
	return b.String()
}

func renderForeshadow(entries []domain.ForeshadowEntry, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.foreshadow)
	for _, e := range entries {
		status := e.Status
		if e.ResolvedAt > 0 {
			status = fmt.Sprintf(l.resolvedAtFmt, e.ResolvedAt)
		}
		fmt.Fprintf(&b, "- **[%s]** %s — "+l.plantedAtFmt+"\n",
			e.ID, e.Description, e.PlantedAt, status)
	}
	return b.String()
}

func renderRelationships(entries []domain.RelationshipEntry, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.relationships)
	for _, e := range entries {
		fmt.Fprintf(&b, "- **%s ↔ %s**%s%s"+l.atChapterFmt+"\n",
			e.CharacterA, e.CharacterB, l.colon, e.Relation, e.Chapter)
	}
	return b.String()
}

func renderWorldRules(rules []domain.WorldRule, l mdLabels) string {
	grouped := make(map[string][]domain.WorldRule)
	var order []string
	for _, r := range rules {
		cat := r.Category
		if cat == "" {
			cat = "other"
		}
		if _, exists := grouped[cat]; !exists {
			order = append(order, cat)
		}
		grouped[cat] = append(grouped[cat], r)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.worldRules)
	for _, cat := range order {
		fmt.Fprintf(&b, "## %s\n\n", cat)
		for _, r := range grouped[cat] {
			fmt.Fprintf(&b, "- **%s**%s%s\n", l.rule, l.colon, r.Rule)
			if r.Boundary != "" {
				fmt.Fprintf(&b, "  - %s%s%s\n", l.boundary, l.colon, r.Boundary)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ── Chapter mechanical-violation facts ──
//
// commit_chapter's rule_violations (the warning-level results of the user_rules mechanical check) are persisted here;
// the editor reads them through novel_context(chapter=N) while reviewing that chapter and maps them into its seven
// dimensions (editor.md §mechanical-check mapping). A writer reworking that chapter sees them too. Append-only, with
// the latest entry for a chapter winning.

// ChapterViolations is one chapter's mechanical-violation record.
type ChapterViolations struct {
	Chapter    int               `json:"chapter"`
	Violations []rules.Violation `json:"violations"`
	At         string            `json:"at"`
}

const ruleViolationsFile = "meta/rule_violations.jsonl"

// SaveRuleViolations appends a chapter's mechanical violations (an empty list is appended too — it overwrites the old record to mean "cleared after a rewrite").
func (s *WorldStore) SaveRuleViolations(chapter int, violations []rules.Violation) error {
	rec := ChapterViolations{Chapter: chapter, Violations: violations, At: time.Now().Format(time.RFC3339)}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.io.AppendLine(ruleViolationsFile, append(data, '\n'))
}

// LoadRuleViolations reads a chapter's latest mechanical-violation record; nil when there is none.
func (s *WorldStore) LoadRuleViolations(chapter int) []rules.Violation {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	data, err := os.ReadFile(s.io.path(ruleViolationsFile))
	if err != nil {
		return nil
	}
	var latest []rules.Violation
	var found bool
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec ChapterViolations
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Chapter == chapter {
			latest, found = rec.Violations, true
		}
	}
	if !found {
		return nil
	}
	return latest
}

// truncateForError shortens a description so a whole foreshadow body is not stuffed into an error message.
func truncateForError(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 30 {
		return string(r)
	}
	return string(r[:30]) + "…"
}
