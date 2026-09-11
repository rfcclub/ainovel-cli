package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/store"
)

// buildStoryStateSummary assembles a compact story-state summary so the stage
// co-creation assistant knows what has already been written. It reuses store
// access points and only takes the high-level facts needed for planning
// direction (progress / compass / latest volume / main characters / active
// foreshadows); it pulls no prose and does not feed novel_context's full JSON —
// co-creation is a conversation, it wants a readable overview, not writing
// context. Any missing item is skipped (best-effort); an empty string means no
// usable progress yet.
func buildStoryStateSummary(s *store.Store) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	var warnings []string
	warn := func(scope string, err error) {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("đọc %s thất bại: %v", scope, err))
		}
	}

	if book, err := s.Book.Load(); book != nil {
		fmt.Fprintf(&b, "- Tên sách: 《%s》\n", book.Title)
	} else {
		warn("book", err)
	}

	if progress, err := s.Progress.Load(); progress != nil {
		fmt.Fprintf(&b, "- Tiến độ: đã hoàn thành %d chương", len(progress.CompletedChapters))
		if progress.Layered {
			outline, outlineErr := s.Outline.LoadOutline()
			if outlineErr != nil {
				warn("outline", outlineErr)
			} else if len(outline) > 0 {
				fmt.Fprintf(&b, " / hiện đã chi tiết hóa %d chương (phần sau quy hoạch động theo cung)", len(outline))
			}
		} else if progress.TotalChapters > 0 {
			fmt.Fprintf(&b, " / quy hoạch %d chương", progress.TotalChapters)
		}
		fmt.Fprintf(&b, ", khoảng %d chữ, chương tiếp theo là chương %d\n", progress.TotalWordCount, progress.NextChapter())
		if progress.Layered && progress.CurrentVolume > 0 {
			fmt.Fprintf(&b, "- Vị trí hiện tại: tập %d cung %d\n", progress.CurrentVolume, progress.CurrentArc)
		}
	} else {
		warn("progress", err)
	}

	if compass, err := s.Outline.LoadCompass(); compass != nil {
		if dir := strings.TrimSpace(compass.EndingDirection); dir != "" {
			fmt.Fprintf(&b, "- Hướng kết cục: %s\n", dir)
		}
		if compass.EstimatedScale != "" {
			fmt.Fprintf(&b, "- Quy mô ước lượng: %s\n", compass.EstimatedScale)
		}
		if len(compass.OpenThreads) > 0 {
			fmt.Fprintf(&b, "- Tuyến dài đang mở: %s\n", strings.Join(compass.OpenThreads, "; "))
		}
	} else {
		warn("story_compass", err)
	}

	// Latest volume summary, so the assistant knows where the story just got to.
	if vols, err := s.Summaries.LoadAllVolumeSummaries(); len(vols) > 0 {
		last := vols[len(vols)-1]
		fmt.Fprintf(&b, "- Gần nhất 《%s》: %s\n", last.Title, truncate(last.Summary, 200))
	} else {
		warn("volume_summaries", err)
	}

	// Main characters (core/important), at most 8.
	if chars, err := s.Characters.Load(); len(chars) > 0 {
		var names []string
		for _, c := range chars {
			if c.Tier == "secondary" || c.Tier == "decorative" {
				continue
			}
			line := c.Name
			if role := strings.TrimSpace(c.Role); role != "" {
				line += " (" + role + ")"
			}
			names = append(names, line)
			if len(names) >= 8 {
				break
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "- Nhân vật chính: %s\n", strings.Join(names, ", "))
		}
	} else {
		warn("characters", err)
	}

	// Unresolved foreshadows, at most 6.
	if fs, err := s.World.LoadActiveForeshadow(); len(fs) > 0 {
		var items []string
		for _, f := range fs {
			items = append(items, truncate(f.Description, 40))
			if len(items) >= 6 {
				break
			}
		}
		fmt.Fprintf(&b, "- Phục bút chưa thu hồi: %s\n", strings.Join(items, "; "))
	} else {
		warn("foreshadow", err)
	}

	if len(warnings) > 0 {
		fmt.Fprintf(&b, "- Cảnh báo dữ liệu: %s\n", strings.Join(warnings, "; "))
	}

	return strings.TrimSpace(b.String())
}

// stageSystemPrompt assembles the full stage co-creation system prompt: the stage
// prompt plus the current story-state summary. The summary hangs at the end as a
// data appendix (separated from the format spec by a rule), echoing the prompt's
// "see progress below" pointer.
func stageSystemPrompt(s *store.Store) string {
	prompt := stageCoCreateSystemPrompt
	if summary := buildStoryStateSummary(s); summary != "" {
		prompt += "\n\n---\n## Trạng thái truyện hiện tại\n(Bên dưới là tóm tắt khách quan về nội dung đã viết, để bạn tham chiếu khi quy hoạch phần sau; đừng chép nguyên văn vào <draft>)\n" + summary
	}
	return prompt
}