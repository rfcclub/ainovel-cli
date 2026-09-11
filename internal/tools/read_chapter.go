package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReadChapterTool reads chapter text so the Agent can re-read its own words and earlier prose.
type ReadChapterTool struct {
	store *store.Store
}

func NewReadChapterTool(store *store.Store) *ReadChapterTool {
	return &ReadChapterTool{store: store}
}

func (t *ReadChapterTool) Name() string { return "read_chapter" }
func (t *ReadChapterTool) Description() string {
	return "Đọc nguyên văn chương. Có thể đọc bản chung cuộc, bản nháp, hoặc trích đoạn đối thoại của nhân vật"
}
func (t *ReadChapterTool) Label() string { return "Đọc chương" }

// A pure read tool; it may be scheduled concurrently (the editor often reads several chapters at once while reviewing).
func (t *ReadChapterTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *ReadChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ReadChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương (bắt buộc khi đọc một chương)")),
		schema.Property("from", schema.Int("Số chương bắt đầu (dùng khi đọc theo khoảng)")),
		schema.Property("to", schema.Int("Số chương kết thúc (dùng khi đọc theo khoảng)")),
		schema.Property("source", schema.Enum("Nguồn", "final", "draft")).Required(),
		schema.Property("character", schema.String("Tên nhân vật (dùng khi trích đoạn đối thoại)")),
		schema.Property("max_runes", schema.Int("Số ký tự tối đa mỗi chương (cắt bớt khi đọc theo khoảng, mặc định 2000)")),
	)
}

func (t *ReadChapterTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter   int    `json:"chapter"`
		From      int    `json:"from"`
		To        int    `json:"to"`
		Source    string `json:"source"`
		Character string `json:"character"`
		MaxRunes  int    `json:"max_runes"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Source != "final" && a.Source != "draft" {
		return nil, fmt.Errorf("source must be final or draft")
	}

	// Mode 1: extract character dialogue
	if a.Character != "" {
		var warnings []string
		warn := func(scope string, err error) {
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("đọc %s thất bại: %v", scope, err))
			}
		}
		chars, err := t.store.Characters.Load()
		warn("characters", err)
		var aliases []string
		for _, c := range chars {
			if c.Name == a.Character {
				aliases = c.Aliases
				break
			}
		}
		var maxCompleted int
		p, err := t.store.Progress.Load()
		warn("progress", err)
		if p != nil {
			maxCompleted = maxCompletedChapter(p.CompletedChapters)
		}
		samples, err := t.store.Drafts.ExtractDialogue(a.Character, aliases, 8, maxCompleted)
		warn("dialogue_samples", err)
		result := map[string]any{
			"character": a.Character,
			"samples":   samples,
		}
		if len(samples) == 0 {
			result["hint"] = "Nhân vật này chưa có mẫu đối thoại đã commit nào khả dụng"
		}
		if len(warnings) > 0 {
			result["status"] = "partial"
			result["_warnings"] = warnings
		}
		return json.Marshal(result)
	}

	// Mode 2: range read
	if a.From > 0 && a.To > 0 {
		maxRunes := a.MaxRunes
		if maxRunes <= 0 {
			maxRunes = 2000
		}
		var load func(int) (string, error)
		if a.Source == "draft" {
			load = t.store.Drafts.LoadDraft
		} else {
			load = t.store.Drafts.LoadChapterText
		}
		texts := make(map[int]string)
		for ch := a.From; ch <= a.To; ch++ {
			chapter, err := load(ch)
			if err != nil {
				return nil, fmt.Errorf("load %s chapter %d: %w", a.Source, ch, err)
			}
			if chapter == "" {
				continue
			}
			runes := []rune(chapter)
			if len(runes) > maxRunes {
				chapter = string(runes[:maxRunes]) + "..."
			}
			texts[ch] = chapter
		}
		return json.Marshal(map[string]any{
			"chapters": texts,
			"from":     a.From,
			"to":       a.To,
			"source":   a.Source,
		})
	}

	// Mode 3: single-chapter read
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter is required")
	}

	var content string
	var err error
	switch a.Source {
	case "draft":
		content, err = t.store.Drafts.LoadDraft(a.Chapter)
	default: // final
		content, err = t.store.Drafts.LoadChapterText(a.Chapter)
	}
	if err != nil {
		return nil, fmt.Errorf("read chapter %d: %w", a.Chapter, err)
	}
	if content == "" {
		return json.Marshal(map[string]any{
			"chapter": a.Chapter,
			"source":  a.Source,
			"exists":  false,
			"hint":    "Nguồn được yêu cầu không có chương này; nếu cần đọc nguồn khác, hãy chỉ định rõ source",
		})
	}

	return json.Marshal(map[string]any{
		"chapter":    a.Chapter,
		"source":     a.Source,
		"content":    content,
		"word_count": len([]rune(content)),
	})
}

// maxCompletedChapter returns the highest chapter number in the completed-chapter list.
func maxCompletedChapter(completed []int) int {
	m := 0
	for _, ch := range completed {
		if ch > m {
			m = ch
		}
	}
	return m
}
