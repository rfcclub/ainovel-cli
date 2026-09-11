package revision

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/chapterfacts"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Projector rebuilds every chapter-level derived state from the chapter records.
type Projector struct{ store *store.Store }

func NewProjector(st *store.Store) *Projector { return &Projector{store: st} }

// ValidateRecords checks whether a full set of chapter records can be replayed deterministically,
// writing no projection.
func ValidateRecords(records []domain.ChapterRecord) error {
	records = slices.Clone(records)
	slices.SortFunc(records, func(a, b domain.ChapterRecord) int { return a.Chapter - b.Chapter })
	for _, record := range records {
		if err := chapterfacts.Validate(record.Facts); err != nil {
			return fmt.Errorf("sự thật chương %d không hợp lệ: %w", record.Chapter, err)
		}
	}
	_, _, _, _, err := projectWorld(records)
	return err
}

func (p *Projector) Apply(records []domain.ChapterRecord) error {
	records = slices.Clone(records)
	slices.SortFunc(records, func(a, b domain.ChapterRecord) int { return a.Chapter - b.Chapter })
	for _, record := range records {
		if err := chapterfacts.Validate(record.Facts); err != nil {
			return fmt.Errorf("sự thật chương %d không hợp lệ: %w", record.Chapter, err)
		}
	}

	timeline, ledger, relationships, changes, err := projectWorld(records)
	if err != nil {
		return err
	}
	cast, err := p.projectCast(records)
	if err != nil {
		return err
	}

	for _, record := range records {
		facts := record.Facts
		if err := p.store.Summaries.SaveSummary(domain.ChapterSummary{
			Chapter: record.Chapter, Title: facts.Title, Summary: facts.Summary,
			Characters: facts.Characters, KeyEvents: facts.KeyEvents,
		}); err != nil {
			return fmt.Errorf("lưu tóm tắt chương %d: %w", record.Chapter, err)
		}
	}
	if err := p.store.World.SaveTimeline(timeline); err != nil {
		return fmt.Errorf("dựng lại dòng thời gian: %w", err)
	}
	if err := p.store.World.SaveForeshadowLedger(ledger); err != nil {
		return fmt.Errorf("dựng lại sổ phục bút: %w", err)
	}
	if err := p.store.World.SaveRelationships(relationships); err != nil {
		return fmt.Errorf("dựng lại quan hệ nhân vật: %w", err)
	}
	if err := p.store.World.SaveStateChanges(changes); err != nil {
		return fmt.Errorf("dựng lại thay đổi trạng thái: %w", err)
	}
	if err := p.store.Cast.Save(cast); err != nil {
		return fmt.Errorf("dựng lại danh bạ nhân vật phụ: %w", err)
	}
	if err := p.updateProgress(records); err != nil {
		return err
	}
	if err := p.store.World.SaveAuthorRevisionStyle(projectStyle(records)); err != nil {
		return fmt.Errorf("lưu văn phong do người dùng tu chỉnh: %w", err)
	}
	return p.refreshRuleViolations(records)
}

func projectWorld(records []domain.ChapterRecord) ([]domain.TimelineEvent, []domain.ForeshadowEntry, []domain.RelationshipEntry, []domain.StateChange, error) {
	var timeline []domain.TimelineEvent
	var changes []domain.StateChange
	ledger := make([]domain.ForeshadowEntry, 0)
	foreshadowIndex := make(map[string]int)
	relationships := make(map[string]domain.RelationshipEntry)

	for _, record := range records {
		chapter := record.Chapter
		for _, event := range record.Facts.TimelineEvents {
			event.Chapter = chapter
			timeline = append(timeline, event)
		}
		for _, change := range record.Facts.StateChanges {
			change.Chapter = chapter
			changes = append(changes, change)
		}
		for _, relation := range record.Facts.RelationshipChanges {
			relation.Chapter = chapter
			relationships[relationshipKey(relation.CharacterA, relation.CharacterB)] = relation
		}
		for _, update := range record.Facts.ForeshadowUpdates {
			idx, exists := foreshadowIndex[update.ID]
			switch update.Action {
			case "plant":
				if strings.TrimSpace(update.ID) == "" || strings.TrimSpace(update.Description) == "" {
					return nil, nil, nil, nil, fmt.Errorf("phục bút plant của chương %d thiếu id hoặc description", chapter)
				}
				if exists {
					if ledger[idx].Description == "" {
						ledger[idx].Description = update.Description
					}
					continue
				}
				foreshadowIndex[update.ID] = len(ledger)
				ledger = append(ledger, domain.ForeshadowEntry{ID: update.ID, Description: update.Description, PlantedAt: chapter, Status: "planted"})
			case "advance":
				if !exists {
					return nil, nil, nil, nil, fmt.Errorf("chương %d thúc đẩy phục bút không tồn tại %q", chapter, update.ID)
				}
				ledger[idx].Status = "advanced"
			case "resolve":
				if !exists {
					return nil, nil, nil, nil, fmt.Errorf("chương %d thu hồi phục bút không tồn tại %q", chapter, update.ID)
				}
				ledger[idx].Status = "resolved"
				ledger[idx].ResolvedAt = chapter
			default:
				return nil, nil, nil, nil, fmt.Errorf("thao tác phục bút ở chương %d không hợp lệ: %q", chapter, update.Action)
			}
		}
	}

	relationList := make([]domain.RelationshipEntry, 0, len(relationships))
	for _, relation := range relationships {
		relationList = append(relationList, relation)
	}
	slices.SortFunc(relationList, func(a, b domain.RelationshipEntry) int {
		return strings.Compare(relationshipKey(a.CharacterA, a.CharacterB), relationshipKey(b.CharacterA, b.CharacterB))
	})
	return timeline, ledger, relationList, changes, nil
}

func (p *Projector) projectCast(records []domain.ChapterRecord) ([]domain.CastEntry, error) {
	characters, err := p.store.Characters.Load()
	if err != nil {
		return nil, fmt.Errorf("đọc nhân vật cốt lõi: %w", err)
	}
	core := make(map[string]bool)
	for _, character := range characters {
		core[character.Name] = true
		for _, alias := range character.Aliases {
			core[alias] = true
		}
	}
	entries := make(map[string]*domain.CastEntry)
	for _, record := range records {
		intros := make(map[string]string)
		for _, intro := range record.Facts.CastIntros {
			intros[intro.Name] = intro.BriefRole
		}
		seen := make(map[string]bool)
		for _, name := range record.Facts.Characters {
			if name == "" || core[name] || seen[name] {
				continue
			}
			seen[name] = true
			entry := entries[name]
			if entry == nil {
				entry = &domain.CastEntry{Name: name, BriefRole: intros[name], FirstSeenChapter: record.Chapter}
				entries[name] = entry
			} else if entry.BriefRole == "" {
				entry.BriefRole = intros[name]
			}
			entry.LastSeenChapter = record.Chapter
			entry.AppearanceChapters = append(entry.AppearanceChapters, record.Chapter)
			entry.AppearanceCount = len(entry.AppearanceChapters)
		}
	}
	out := make([]domain.CastEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, *entry)
	}
	slices.SortFunc(out, func(a, b domain.CastEntry) int {
		if a.FirstSeenChapter != b.FirstSeenChapter {
			return a.FirstSeenChapter - b.FirstSeenChapter
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

func (p *Projector) updateProgress(records []domain.ChapterRecord) error {
	progress, err := p.store.Progress.Load()
	if err != nil {
		return fmt.Errorf("đọc tiến độ: %w", err)
	}
	if progress == nil {
		return fmt.Errorf("progress chưa được khởi tạo")
	}
	progress.ChapterWordCounts = make(map[int]int, len(records))
	progress.TotalWordCount = 0
	progress.HookHistory = nil
	progress.StrandHistory = nil
	for _, record := range records {
		count := utf8.RuneCountInString(record.Content)
		progress.ChapterWordCounts[record.Chapter] = count
		progress.TotalWordCount += count
		setChapterHistory(&progress.HookHistory, record.Chapter, record.Facts.HookType)
		setChapterHistory(&progress.StrandHistory, record.Chapter, record.Facts.DominantStrand)
	}
	if err := p.store.Progress.Save(progress); err != nil {
		return fmt.Errorf("cập nhật chiếu tiến độ chương: %w", err)
	}
	return nil
}

func (p *Projector) refreshRuleViolations(records []domain.ChapterRecord) error {
	structured := rules.SystemDefaults().Structured
	if snapshot, err := p.store.UserRules.Load(); err != nil {
		return fmt.Errorf("đọc quy tắc người dùng: %w", err)
	} else if snapshot != nil {
		structured = snapshot.Structured
	}
	for _, record := range records {
		violations := append(rules.Lint(record.Content), rules.Check(record.Content, structured)...)
		if err := p.store.World.SaveRuleViolations(record.Chapter, violations); err != nil {
			return fmt.Errorf("cập nhật kiểm tra cơ học chương %d: %w", record.Chapter, err)
		}
	}
	return nil
}

func projectStyle(records []domain.ChapterRecord) domain.AuthorRevisionStyle {
	style := domain.AuthorRevisionStyle{}
	style.Prose = appendUnique(style.Prose, collectStyle(records, func(s domain.StyleDelta) []string { return s.Prose })...)
	style.Taboos = appendUnique(style.Taboos, collectStyle(records, func(s domain.StyleDelta) []string { return s.Taboos })...)
	voiceIndex := make(map[string]int)
	for _, record := range records {
		if record.Origin == domain.ChapterOriginUser && record.AcceptedAt.After(style.UpdatedAt) {
			style.UpdatedAt = record.AcceptedAt
		}
		for _, voice := range record.StyleDelta.Dialogue {
			idx, exists := voiceIndex[voice.Name]
			if !exists {
				idx = len(style.Dialogue)
				voiceIndex[voice.Name] = idx
				style.Dialogue = append(style.Dialogue, domain.CharacterVoice{Name: voice.Name})
			}
			style.Dialogue[idx].Rules = appendUnique(style.Dialogue[idx].Rules, voice.Rules...)
		}
	}
	return style
}

func collectStyle(records []domain.ChapterRecord, selectItems func(domain.StyleDelta) []string) []string {
	var out []string
	for _, record := range records {
		out = append(out, selectItems(record.StyleDelta)...)
	}
	return out
}

func appendUnique(dst []string, values ...string) []string {
	seen := make(map[string]bool, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = true
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		dst = append(dst, value)
	}
	return dst
}

func relationshipKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func setChapterHistory(history *[]string, chapter int, value string) {
	if value == "" {
		return
	}
	for len(*history) < chapter {
		*history = append(*history, "")
	}
	(*history)[chapter-1] = value
}
