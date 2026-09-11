package diag

import (
	"fmt"
	"strings"
)

// GhostCharacter detects core/important characters that have not appeared for a long time.
func GhostCharacter(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Characters) == 0 || len(snap.Summaries) == 0 {
		return nil
	}
	completed := snap.CompletedCount()
	if completed < 5 {
		return nil
	}

	// Compute each character's last appearing chapter number
	lastSeen := make(map[string]int)
	for ch, s := range snap.Summaries {
		for _, name := range s.Characters {
			if ch > lastSeen[name] {
				lastSeen[name] = ch
			}
		}
	}

	threshold := completed / 3
	if threshold < 5 {
		threshold = 5
	}
	latest := snap.LatestCompleted()

	var ghosts []string
	for _, c := range snap.Characters {
		if c.Tier != "core" && c.Tier != "important" {
			continue
		}
		seen, ok := lastSeen[c.Name]
		if !ok {
			// Check aliases too
			for _, alias := range c.Aliases {
				if s, exists := lastSeen[alias]; exists && s > seen {
					seen = s
					ok = true
				}
			}
		}
		gap := latest - seen
		if !ok {
			ghosts = append(ghosts, fmt.Sprintf("%s(chưa từng xuất hiện trong tóm tắt)", c.Name))
		} else if gap > threshold {
			ghosts = append(ghosts, fmt.Sprintf("%s(xuất hiện cuối ở ch%d, đã vắng %d chương)", c.Name, seen, gap))
		}
	}
	if len(ghosts) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "GhostCharacter",
		Category:   CatContext,
		Severity:   SevInfo,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.characters",
		Title:      fmt.Sprintf("Nhân vật biến mất: %d nhân vật cốt lõi vắng mặt lâu dài", len(ghosts)),
		Evidence:   strings.Join(ghosts, "; "),
		Suggestion: "Writer có thể đã mất dấu nhân vật này. Cân nhắc gửi thẳng chỉ thị can thiệp trong ô nhập để đưa nhân vật trở lại, hoặc hạ tier của nó trong characters.json.",
	}}
}

// TimelineGaps detects completed chapters missing timeline events.
func TimelineGaps(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.CompletedChapters) == 0 {
		return nil
	}
	if len(snap.Timeline) == 0 && snap.CompletedCount() > 0 {
		return []Finding{{
			Rule:       "TimelineGaps",
			Category:   CatContext,
			Severity:   SevInfo,
			Confidence: ConfMedium,
			AutoLevel:  AutoNone,
			Target:     "context.timeline",
			Title:      "Dòng thời gian rỗng",
			Evidence:   fmt.Sprintf("completed=%d, timeline_events=0", snap.CompletedCount()),
			Suggestion: "Việc trích xuất dòng thời gian trong commit_chapter có thể chưa có tác dụng. Kiểm tra xem đầu ra của Writer có trường timeline không.",
		}}
	}

	// Build the chapter → event mapping
	chaptersWithEvents := make(map[int]bool)
	for _, e := range snap.Timeline {
		chaptersWithEvents[e.Chapter] = true
	}

	var missing []int
	for _, ch := range snap.Progress.CompletedChapters {
		if !chaptersWithEvents[ch] {
			missing = append(missing, ch)
		}
	}
	// A small number of gaps is allowed (some transitional chapters may genuinely have no major event)
	if len(missing) == 0 || float64(len(missing))/float64(snap.CompletedCount()) < ThresholdTimelineGapRate {
		return nil
	}
	return []Finding{{
		Rule:       "TimelineGaps",
		Category:   CatContext,
		Severity:   SevInfo,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.timeline",
		Title:      fmt.Sprintf("Thiếu hụt dòng thời gian: %d chương không có bản ghi sự kiện", len(missing)),
		Evidence:   fmt.Sprintf("missing=[%s]", intsToStr(missing)),
		Suggestion: "Việc trích xuất dòng thời gian trong commit_chapter có thể hỏng một phần. Kiểm tra định dạng trường timeline trong đầu ra của Writer.",
	}}
}

// RelationshipStagnation detects relationship data that has stopped updating.
func RelationshipStagnation(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Relationships) == 0 {
		return nil
	}
	completed := snap.CompletedCount()
	if completed < 6 {
		return nil
	}

	// Find the latest chapter of relationship data
	latestRelCh := 0
	for _, r := range snap.Relationships {
		if r.Chapter > latestRelCh {
			latestRelCh = r.Chapter
		}
	}

	// If the latest relationship data falls in the first third, judge it stagnant
	cutoff := snap.LatestCompleted() - completed/3
	if latestRelCh >= cutoff {
		return nil
	}
	return []Finding{{
		Rule:       "RelationshipStagnation",
		Category:   CatContext,
		Severity:   SevInfo,
		Confidence: ConfLow,
		AutoLevel:  AutoNone,
		Target:     "context.relationships",
		Title:      fmt.Sprintf("Dữ liệu quan hệ đình trệ: cập nhật mới nhất ở chương %d", latestRelCh),
		Evidence:   fmt.Sprintf("relationship_entries=%d, latest_update=ch%d, latest_completed=ch%d", len(snap.Relationships), latestRelCh, snap.LatestCompleted()),
		Suggestion: "Việc cập nhật quan hệ trong commit_chapter có thể đã ngừng hoạt động, hoặc quan hệ trong truyện thật sự không đổi. Kiểm tra trường relationships trong đầu ra của Writer.",
	}}
}
