package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveArcSummaryTool saves the arc-level summary, character snapshot and writing rules, called by the Editor at the end of an arc.
type SaveArcSummaryTool struct {
	store *store.Store
}

func NewSaveArcSummaryTool(store *store.Store) *SaveArcSummaryTool {
	return &SaveArcSummaryTool{store: store}
}

func (t *SaveArcSummaryTool) Name() string { return "save_arc_summary" }
func (t *SaveArcSummaryTool) Description() string {
	return "Lưu tóm tắt cấp cung, ảnh chụp trạng thái nhân vật và quy tắc viết (chế độ truyện dài, gọi khi kết thúc một cung)"
}
func (t *SaveArcSummaryTool) Label() string { return "Lưu tóm tắt cung" }

// A write tool; concurrency is forbidden.
func (t *SaveArcSummaryTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveArcSummaryTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveArcSummaryTool) Schema() map[string]any {
	snapshotSchema := schema.Object(
		schema.Property("name", schema.String("Tên nhân vật")).Required(),
		schema.Property("status", schema.String("Trạng thái hiện tại (còn sống/bị thương/mất tích...)")).Required(),
		schema.Property("power", schema.String("Thay đổi năng lực")),
		schema.Property("motivation", schema.String("Động cơ hiện tại")).Required(),
		schema.Property("relations", schema.String("Thay đổi quan hệ then chốt")),
	)
	voiceSchema := schema.Object(
		schema.Property("name", schema.String("Tên nhân vật")).Required(),
		schema.Property("rules", schema.Array("2-3 quy tắc đặc trưng ngôn ngữ (mỗi quy tắc ≤30 chữ)", schema.String(""))).Required(),
	)
	styleRulesSchema := schema.Object(
		schema.Property("prose", schema.Array("3-5 quy tắc văn phong tự sự (mỗi quy tắc ≤50 chữ, phải cụ thể và thực thi được)", schema.String(""))).Required(),
		schema.Property("dialogue", schema.Array("Quy tắc đặc trưng đối thoại của nhân vật cốt lõi", voiceSchema)).Required(),
		schema.Property("taboos", schema.Array("Những cách viết cuốn tiểu thuyết này cần tránh", schema.String(""))),
	)
	return schema.Object(
		schema.Property("volume", schema.Int("Số tập")).Required(),
		schema.Property("arc", schema.Int("Số cung")).Required(),
		schema.Property("title", schema.String("Tiêu đề cung")).Required(),
		schema.Property("summary", schema.String("Tóm tắt cung (trong 500 chữ)")).Required(),
		schema.Property("key_events", schema.Array("Sự kiện then chốt trong cung", schema.String(""))).Required(),
		schema.Property("character_snapshots", schema.Array("Ảnh chụp trạng thái nhân vật", snapshotSchema)).Required(),
		schema.Property("style_rules", styleRulesSchema).Required(),
	)
}

func (t *SaveArcSummaryTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Volume             int                        `json:"volume"`
		Arc                int                        `json:"arc"`
		Title              string                     `json:"title"`
		Summary            string                     `json:"summary"`
		KeyEvents          []string                   `json:"key_events"`
		CharacterSnapshots []domain.CharacterSnapshot `json:"character_snapshots"`
		StyleRules         *arcSummaryStyleRules      `json:"style_rules"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		if strings.Contains(err.Error(), "style_rules.dialogue") {
			return nil, fmt.Errorf("invalid args: style_rules.dialogue must be an array of objects {name, rules}, not strings: %w: %w", errs.ErrToolArgs, err)
		}
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Volume <= 0 || a.Arc <= 0 {
		return nil, fmt.Errorf("volume and arc must be > 0: %w", errs.ErrToolArgs)
	}
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Summary) == "" {
		return nil, fmt.Errorf("title and summary are required: %w", errs.ErrToolArgs)
	}
	if err := validateArcSummaryStyleRules(a.StyleRules); err != nil {
		return nil, err
	}
	for i := range a.CharacterSnapshots {
		a.CharacterSnapshots[i].Volume = a.Volume
		a.CharacterSnapshots[i].Arc = a.Arc
	}
	arcSummary := domain.ArcSummary{
		Volume: a.Volume, Arc: a.Arc, Title: a.Title, Summary: a.Summary, KeyEvents: a.KeyEvents,
	}
	rules := domain.WritingStyleRules{
		Volume:    a.Volume,
		Arc:       a.Arc,
		Prose:     a.StyleRules.Prose,
		Dialogue:  a.StyleRules.Dialogue,
		Taboos:    a.StyleRules.Taboos,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	replay, err := t.arcSummaryReplay(arcSummary, a.CharacterSnapshots, rules)
	if err != nil {
		return nil, err
	}
	if !replay {
		if err := requireAggregateTarget(t.store, flow.AggregateArcSummary, a.Volume, a.Arc, 0); err != nil {
			return nil, err
		}
		if len(a.CharacterSnapshots) > 0 {
			if err := t.store.Characters.SaveSnapshots(a.Volume, a.Arc, a.CharacterSnapshots); err != nil {
				return nil, fmt.Errorf("save character snapshots: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		if err := t.store.World.SaveStyleRules(rules); err != nil {
			return nil, fmt.Errorf("save style rules: %w: %w", errs.ErrStoreWrite, err)
		}

		// The arc summary is the Router's completion marker and is written as the last semantic artifact. If any earlier
		// step failed the summary stays missing, and after recovery the Router re-dispatches this task.
		if err := t.store.Summaries.SaveArcSummary(arcSummary); err != nil {
			return nil, fmt.Errorf("save arc summary: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	artifacts := []string{fmt.Sprintf("summaries/arc-v%02da%02d.json", a.Volume, a.Arc)}
	if len(a.CharacterSnapshots) > 0 {
		artifacts = append(artifacts, fmt.Sprintf("meta/snapshots/v%02da%02d.json", a.Volume, a.Arc))
	}
	artifacts = append(artifacts, "meta/style_rules.json")

	if _, err := t.store.Checkpoints.AppendArtifacts(
		domain.ArcScope(a.Volume, a.Arc), "arc_summary", artifacts...,
	); err != nil {
		return nil, fmt.Errorf("checkpoint arc summary: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"saved": true, "type": "arc_summary",
		"volume": a.Volume, "arc": a.Arc,
		"snapshots":         len(a.CharacterSnapshots),
		"style_rules_saved": true,
	})
}

// arcSummaryReplay lets through only an idempotent wrap-up with identical content, for retries where the semantic
// artifact landed but the checkpoint append failed. Any difference is an explicit conflict; a retry must not be used to
// overwrite historical aggregate facts.
func (t *SaveArcSummaryTool) arcSummaryReplay(
	summary domain.ArcSummary,
	snapshots []domain.CharacterSnapshot,
	rules domain.WritingStyleRules,
) (bool, error) {
	existing, err := t.store.Summaries.LoadArcSummary(summary.Volume, summary.Arc)
	if err != nil {
		return false, fmt.Errorf("load arc summary: %w: %w", errs.ErrStoreRead, err)
	}
	if existing == nil {
		return false, nil
	}
	storedSnapshots, err := t.store.Characters.LoadSnapshots(summary.Volume, summary.Arc)
	if err != nil {
		return false, fmt.Errorf("load character snapshots: %w: %w", errs.ErrStoreRead, err)
	}
	storedRules, err := t.store.World.LoadStyleRules()
	if err != nil {
		return false, fmt.Errorf("load style rules: %w: %w", errs.ErrStoreRead, err)
	}
	if storedRules != nil {
		rules.UpdatedAt = storedRules.UpdatedAt
	}
	if !reflect.DeepEqual(*existing, summary) ||
		!slices.Equal(storedSnapshots, snapshots) ||
		storedRules == nil || !reflect.DeepEqual(*storedRules, rules) {
		return false, fmt.Errorf("tóm tắt tập %d cung %d đã tồn tại nhưng gắn với sản phẩm khác, từ chối ghi đè: %w", summary.Volume, summary.Arc, errs.ErrToolConflict)
	}
	return true, nil
}

type arcSummaryStyleRules struct {
	Prose    []string                `json:"prose"`
	Dialogue []domain.CharacterVoice `json:"dialogue"`
	Taboos   []string                `json:"taboos"`
}

func validateArcSummaryStyleRules(rules *arcSummaryStyleRules) error {
	if rules == nil {
		return fmt.Errorf("style_rules is required: %w", errs.ErrToolArgs)
	}
	if len(rules.Prose) == 0 {
		return fmt.Errorf("style_rules.prose is required: %w", errs.ErrToolArgs)
	}
	if len(rules.Dialogue) == 0 {
		return fmt.Errorf("style_rules.dialogue is required; expected array of objects {name, rules}: %w", errs.ErrToolArgs)
	}
	for i, voice := range rules.Dialogue {
		if strings.TrimSpace(voice.Name) == "" {
			return fmt.Errorf("style_rules.dialogue[%d].name is required: %w", i, errs.ErrToolArgs)
		}
		if len(voice.Rules) == 0 {
			return fmt.Errorf("style_rules.dialogue[%d].rules is required: %w", i, errs.ErrToolArgs)
		}
		for j, rule := range voice.Rules {
			if strings.TrimSpace(rule) == "" {
				return fmt.Errorf("style_rules.dialogue[%d].rules[%d] is empty: %w", i, j, errs.ErrToolArgs)
			}
		}
	}
	return nil
}
