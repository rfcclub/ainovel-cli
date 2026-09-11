package chapterfacts

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// Properties returns the JSON Schema fields shared by full chapter facts.
func Properties(includeFeedback bool) []schema.Prop {
	textList := func(description string) map[string]any {
		return schema.Array(description, schema.String(description))
	}
	timeline := schema.Object(
		schema.Property("time", schema.String("Thời gian trong truyện")).Required(),
		schema.Property("event", schema.String("Sự kiện")).Required(),
		schema.Property("characters", textList("Nhân vật liên quan")).Required(),
	)
	foreshadow := schema.Object(
		schema.Property("id", schema.String("ID phục bút")).Required(),
		schema.Property("action", schema.Enum("Hành động", "plant", "advance", "resolve")).Required(),
		schema.Property("description", llmcontract.Nullable(schema.String("Mô tả khi plant; các hành động khác thì null"))).Required(),
	)
	relationship := schema.Object(
		schema.Property("character_a", schema.String("Nhân vật A")).Required(),
		schema.Property("character_b", schema.String("Nhân vật B")).Required(),
		schema.Property("relation", schema.String("Quan hệ ở cuối chương này")).Required(),
	)
	stateChange := schema.Object(
		schema.Property("entity", schema.String("Thực thể")).Required(),
		schema.Property("field", schema.String("Thuộc tính")).Required(),
		schema.Property("old_value", llmcontract.Nullable(schema.String("Giá trị trước khi thay đổi"))).Required(),
		schema.Property("new_value", schema.String("Giá trị sau khi thay đổi")).Required(),
		schema.Property("reason", llmcontract.Nullable(schema.String("Nguyên nhân"))).Required(),
	)
	props := []schema.Prop{
		schema.Property("title", schema.String("Tiêu đề cuối cùng")).Required(),
		schema.Property("summary", schema.String("Tóm tắt chương")).Required(),
		schema.Property("characters", textList("Nhân vật xuất hiện")).Required(),
		schema.Property("key_events", textList("Sự kiện then chốt")).Required(),
		schema.Property("timeline_events", schema.Array("Sự kiện dòng thời gian", timeline)).Required(),
		schema.Property("foreshadow_updates", schema.Array("Thao tác phục bút", foreshadow)).Required(),
		schema.Property("relationship_changes", schema.Array("Thay đổi quan hệ", relationship)).Required(),
		schema.Property("state_changes", schema.Array("Thay đổi trạng thái", stateChange)).Required(),
		schema.Property("cast_intros", schema.Array("Nhân vật phụ mới", schema.Object(
			schema.Property("name", schema.String("Tên")).Required(),
			schema.Property("brief_role", schema.String("Định vị")).Required(),
		))).Required(),
		schema.Property("hook_type", llmcontract.Nullable(schema.Enum("Móc câu cuối chương", domain.HookTypes()...))).Required(),
		schema.Property("dominant_strand", llmcontract.Nullable(schema.Enum("Tuyến tự sự chủ đạo", domain.DominantStrands()...))).Required(),
	}
	if includeFeedback {
		feedback := schema.Object(
			schema.Property("deviation", schema.String("Mô tả độ lệch so với đại cương")).Required(),
			schema.Property("suggestion", schema.String("Đề xuất điều chỉnh cho đại cương phía sau")).Required(),
		)
		feedback["description"] = "Đối tượng gợi ý cho đại cương phía sau; bắt buộc truyền thẳng JSON object, đừng truyền chuỗi JSON"
		props = append(props, schema.Property("feedback", llmcontract.Nullable(feedback)).Required())
	}
	return props
}

// Validate checks the deterministic constraints shared by normal commits and manual revisions.
func Validate(facts domain.ChapterFacts) error {
	if strings.TrimSpace(facts.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(facts.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if len(facts.KeyEvents) == 0 {
		return fmt.Errorf("key_events must contain at least one event")
	}
	if err := validateTextItems("characters", facts.Characters); err != nil {
		return err
	}
	if err := validateTextItems("key_events", facts.KeyEvents); err != nil {
		return err
	}
	for i, event := range facts.TimelineEvents {
		if strings.TrimSpace(event.Time) == "" || strings.TrimSpace(event.Event) == "" {
			return fmt.Errorf("timeline_events[%d] requires time and event", i)
		}
		if err := validateTextItems(fmt.Sprintf("timeline_events[%d].characters", i), event.Characters); err != nil {
			return err
		}
	}
	for i, update := range facts.ForeshadowUpdates {
		if strings.TrimSpace(update.ID) == "" {
			return fmt.Errorf("foreshadow_updates[%d].id is required", i)
		}
		if placeholderForeshadowID(update.ID) {
			return fmt.Errorf(
				"foreshadow_updates[%d].id = %q là chỗ giữ chỗ, không phải định danh. Mỗi phục bút cần một id "+
					"duy nhất và ổn định (ví dụ FORESHADOW_OBSERVER, jade-pendant), các chương sau dựa vào nó để advance/resolve; "+
					"dùng lại chỗ giữ chỗ sẽ khiến phục bút mới đụng vào bản ghi cũ",
				i, update.ID)
		}
		switch update.Action {
		case "plant":
			if strings.TrimSpace(update.Description) == "" {
				return fmt.Errorf("foreshadow_updates[%d] plant requires description", i)
			}
		case "advance", "resolve":
		default:
			return fmt.Errorf("foreshadow_updates[%d].action invalid: %q", i, update.Action)
		}
	}
	for i, change := range facts.RelationshipChanges {
		if strings.TrimSpace(change.CharacterA) == "" || strings.TrimSpace(change.CharacterB) == "" || strings.TrimSpace(change.Relation) == "" {
			return fmt.Errorf("relationship_changes[%d] requires character_a, character_b and relation", i)
		}
		if change.CharacterA == change.CharacterB {
			return fmt.Errorf("relationship_changes[%d] cannot relate a character to itself", i)
		}
	}
	for i, change := range facts.StateChanges {
		if strings.TrimSpace(change.Entity) == "" || strings.TrimSpace(change.Field) == "" || strings.TrimSpace(change.NewValue) == "" {
			return fmt.Errorf("state_changes[%d] requires entity, field and new_value", i)
		}
	}
	for i, intro := range facts.CastIntros {
		if strings.TrimSpace(intro.Name) == "" || strings.TrimSpace(intro.BriefRole) == "" {
			return fmt.Errorf("cast_intros[%d] requires name and brief_role", i)
		}
	}
	if facts.HookType != "" && !domain.ValidHookType(facts.HookType) {
		return fmt.Errorf("invalid hook_type %q", facts.HookType)
	}
	if facts.DominantStrand != "" && !domain.ValidDominantStrand(facts.DominantStrand) {
		return fmt.Errorf("invalid dominant_strand %q", facts.DominantStrand)
	}
	if facts.Feedback != nil && (strings.TrimSpace(facts.Feedback.Deviation) == "" || strings.TrimSpace(facts.Feedback.Suggestion) == "") {
		return fmt.Errorf("feedback requires deviation and suggestion")
	}
	return nil
}

func validateTextItems(name string, items []string) error {
	for i, item := range items {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("%s[%d] cannot be empty", name, i)
		}
	}
	return nil
}

// placeholderForeshadowIDs are the placeholder words models habitually write into the id
// field. They identify nothing, and a foreshadow id must stay stable across chapters — a
// model was observed naming every new foreshadow "new", so from the second one on it collided
// with the first and was silently dropped: 6 plants across 22 chapters, only 2 recorded.
var placeholderForeshadowIDs = map[string]struct{}{
	"new": {}, "none": {}, "null": {}, "nil": {}, "tbd": {}, "todo": {},
	"auto": {}, "id": {}, "-": {}, "n/a": {}, "na": {}, "unknown": {}, "?": {},
}

func placeholderForeshadowID(id string) bool {
	_, ok := placeholderForeshadowIDs[strings.ToLower(strings.TrimSpace(id))]
	return ok
}
