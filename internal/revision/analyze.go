package revision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/chapterfacts"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

var analysisContract = llmcontract.Contract{
	Name:        "chapter_revision_analysis",
	Description: "Phân tích phần người dùng tu chỉnh trên chương đã hoàn thành và dựng lại toàn bộ sự thật của chương",
	Schema:      revisionAnalysisSchema(),
}

func revisionAnalysisSchema() map[string]any {
	textList := func(description string) map[string]any { return schema.Array(description, schema.String(description)) }
	voice := schema.Object(
		schema.Property("name", schema.String("Tên nhân vật")).Required(),
		schema.Property("rules", textList("Sở thích về đối thoại")).Required(),
	)
	facts := schema.Object(chapterfacts.Properties(false)...)
	impact := schema.Object(
		schema.Property("deviation", schema.String("Thay đổi của cốt truyện đã diễn ra so với kế hoạch hiện có")).Required(),
		schema.Property("suggestion", schema.String("Đề xuất điều chỉnh cho phần đại cương chưa hoàn thành")).Required(),
	)
	return schema.Object(
		schema.Property("change_summary", schema.String("Tóm lược thay đổi")).Required(),
		schema.Property("story_changed", schema.Bool("Có làm thay đổi sự thật cốt truyện hay không")).Required(),
		schema.Property("facts", facts).Required(),
		schema.Property("style_delta", schema.Object(
			schema.Property("prose", textList("Sở thích tự sự được xác nhận từ lần sửa này")).Required(),
			schema.Property("dialogue", schema.Array("Sở thích đối thoại của nhân vật", voice)).Required(),
			schema.Property("taboos", textList("Điều kiêng kỵ thể hiện qua việc người dùng chủ động xóa/sửa")).Required(),
		)).Required(),
		schema.Property("outline_impact", llmcontract.Nullable(impact)).Required(),
		schema.Property("downstream_issues", textList("Xung đột tiềm ẩn với các chương đã hoàn thành phía sau")).Required(),
	)
}

func Analyze(ctx context.Context, model agentcore.ChatModel, systemPrompt string, change Change, previous domain.ChapterRecord, downstream []domain.ChapterSummary) (domain.RevisionAnalysis, error) {
	if model == nil {
		return domain.RevisionAnalysis{}, fmt.Errorf("revision model is required")
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return domain.RevisionAnalysis{}, fmt.Errorf("revision prompt is required")
	}
	payload, err := json.Marshal(map[string]any{
		"chapter": change.Chapter, "previous_facts": previous.Facts,
		"revised_content": change.After, "changed_excerpt": changedExcerpt(change.Before, change.After),
		"downstream_summaries": downstream,
	})
	if err != nil {
		return domain.RevisionAnalysis{}, err
	}
	analysis, err := llmcontract.Execute(ctx, model, llmcontract.Request[domain.RevisionAnalysis]{
		Contract: analysisContract, SystemPrompt: systemPrompt, Payload: string(payload), Agent: "revision",
		Validate: validateAnalysis,
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("chọn giao thức có cấu trúc cho tu chỉnh chương", "mode", res.Mode, "provider", res.Provider, "model", res.Model)
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("sửa đầu ra tu chỉnh chương", "attempt", ev.Attempt, "layer", ev.Layer, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return domain.RevisionAnalysis{}, fmt.Errorf("phân tích tu chỉnh chương %d: %w", change.Chapter, err)
	}
	analysis.Facts.Feedback = analysis.OutlineImpact
	return analysis, nil
}

func validateAnalysis(analysis *domain.RevisionAnalysis) error {
	if strings.TrimSpace(analysis.ChangeSummary) == "" {
		return fmt.Errorf("change_summary is required")
	}
	if err := chapterfacts.Validate(analysis.Facts); err != nil {
		return fmt.Errorf("facts: %w", err)
	}
	if analysis.OutlineImpact != nil && (strings.TrimSpace(analysis.OutlineImpact.Deviation) == "" || strings.TrimSpace(analysis.OutlineImpact.Suggestion) == "") {
		return fmt.Errorf("outline_impact requires deviation and suggestion")
	}
	if err := validateStyleDelta(analysis.StyleDelta); err != nil {
		return err
	}
	for i, issue := range analysis.DownstreamIssues {
		if strings.TrimSpace(issue) == "" {
			return fmt.Errorf("downstream_issues[%d] cannot be empty", i)
		}
	}
	return nil
}

func validateStyleDelta(style domain.StyleDelta) error {
	for name, items := range map[string][]string{"prose": style.Prose, "taboos": style.Taboos} {
		for i, item := range items {
			if strings.TrimSpace(item) == "" {
				return fmt.Errorf("style_delta.%s[%d] cannot be empty", name, i)
			}
		}
	}
	for i, voice := range style.Dialogue {
		if strings.TrimSpace(voice.Name) == "" || len(voice.Rules) == 0 {
			return fmt.Errorf("style_delta.dialogue[%d] requires name and rules", i)
		}
		for j, rule := range voice.Rules {
			if strings.TrimSpace(rule) == "" {
				return fmt.Errorf("style_delta.dialogue[%d].rules[%d] cannot be empty", i, j)
			}
		}
	}
	return nil
}

type excerpt struct {
	BeforeStart int    `json:"before_start_line"`
	BeforeEnd   int    `json:"before_end_line"`
	Before      string `json:"before"`
	AfterStart  int    `json:"after_start_line"`
	AfterEnd    int    `json:"after_end_line"`
	After       string `json:"after"`
}

func changedExcerpt(before, after string) excerpt {
	oldLines := strings.Split(domain.NormalizeChapterContent(before), "\n")
	newLines := strings.Split(domain.NormalizeChapterContent(after), "\n")
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	oldChanged := oldLines[prefix : len(oldLines)-suffix]
	newChanged := newLines[prefix : len(newLines)-suffix]
	return excerpt{
		BeforeStart: prefix + 1, BeforeEnd: prefix + len(oldChanged), Before: strings.Join(oldChanged, "\n"),
		AfterStart: prefix + 1, AfterEnd: prefix + len(newChanged), After: strings.Join(newChanged, "\n"),
	}
}
