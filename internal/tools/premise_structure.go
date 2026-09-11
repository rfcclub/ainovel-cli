package tools

import (
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// premiseHeadingAliases maps a premise heading written by the model (or a legacy
// Chinese premise) onto the canonical Vietnamese heading. The product prompts instruct
// the model to emit the Vietnamese headings, so those are the primary keys; the Chinese
// ones stay so premises written by older versions still parse.
var premiseHeadingAliases = map[string]string{
	// Vietnamese (product default, what the prompts instruct).
	"Thể loại và giọng điệu": "Thể loại và giọng điệu",
	"Định vị thể loại":      "Định vị thể loại",
	"Xung đột cốt lõi":      "Xung đột cốt lõi",
	"Mục tiêu nhân vật chính": "Mục tiêu nhân vật chính",
	"Hướng kết cục":         "Hướng kết cục",
	"Vùng cấm sáng tác":     "Vùng cấm sáng tác",
	"Điểm bán hàng khác biệt": "Điểm bán hàng khác biệt",
	"Móc câu khác biệt":     "Móc câu khác biệt",
	"Cam kết cốt lõi":       "Cam kết cốt lõi",
	"Động cơ câu chuyện":    "Động cơ câu chuyện",
	"Tuyến quan hệ/trưởng thành": "Tuyến quan hệ/trưởng thành",
	"Lộ trình nâng cấp":     "Lộ trình nâng cấp",
	"Chuyển hướng trung kỳ": "Chuyển hướng trung kỳ",
	"Mệnh đề kết cục":       "Mệnh đề kết cục",
	"Tính phù hợp với truyện ngắn": "Tính phù hợp với truyện ngắn",
	"Câu chuyện này phù hợp với truyện ngắn/một tập vì sao": "Tính phù hợp với truyện ngắn",
	"Kết cục":               "Hướng kết cục",
	"Hướng kết thúc":        "Hướng kết cục",

	// Chinese (legacy premises written before the Vietnamese migration).
	"题材定位":    "Định vị thể loại",
	"题材和基调":   "Thể loại và giọng điệu",
	"核心冲突":    "Xung đột cốt lõi",
	"主角目标":    "Mục tiêu nhân vật chính",
	"结局方向":    "Hướng kết cục",
	"终局方向":    "Hướng kết cục",
	"写作禁区":    "Vùng cấm sáng tác",
	"差异化卖点":   "Điểm bán hàng khác biệt",
	"差异化钩子":   "Móc câu khác biệt",
	"核心兑现承诺":  "Cam kết cốt lõi",
	"故事引擎":    "Động cơ câu chuyện",
	"关系/成长主线": "Tuyến quan hệ/trưởng thành",
	"升级路径":    "Lộ trình nâng cấp",
	"中段转折":    "Chuyển hướng trung kỳ",
	"中期转向":    "Chuyển hướng trung kỳ",
	"终局命题":    "Mệnh đề kết cục",
	"短篇适配性":   "Tính phù hợp với truyện ngắn",
	"本作为什么适合短篇/单卷收束": "Tính phù hợp với truyện ngắn",
}

func parsePremiseSections(premise string) map[string]string {
	lines := strings.Split(premise, "\n")
	sections := make(map[string]string)
	var current string
	var body []string

	flush := func() {
		if current == "" {
			return
		}
		sections[current] = strings.TrimSpace(strings.Join(body, "\n"))
		body = body[:0]
	}

	for _, line := range lines {
		if heading, ok := canonicalPremiseHeading(line); ok {
			flush()
			current = heading
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

func canonicalPremiseHeading(line string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimLeft(line, "#"))
	if title == "" {
		return "", false
	}
	canonical, ok := premiseHeadingAliases[title]
	if ok {
		return canonical, true
	}
	// The prompts annotate headings with an explanation after a colon
	// ("Móc câu khác biệt: ..."), and models sometimes echo that whole line as the
	// heading. Match on the part before the first colon as a fallback.
	if base, _, found := strings.Cut(title, ":"); found {
		if canonical, ok := premiseHeadingAliases[strings.TrimSpace(base)]; ok {
			return canonical, true
		}
	}
	return "", false
}

func premiseStructure(premise string, tier domain.PlanningTier) map[string]any {
	sections := parsePremiseSections(premise)
	required := requiredPremiseHeadings(tier)
	found := make([]string, 0, len(required))
	var missing []string
	for _, heading := range required {
		if _, ok := sections[heading]; ok {
			found = append(found, heading)
			continue
		}
		missing = append(missing, heading)
	}

	structure := map[string]any{
		"template_ready": len(missing) == 0,
		"found":          found,
		"missing":        missing,
	}
	if len(sections) > 0 {
		structure["section_count"] = len(sections)
	}
	return structure
}

func requiredPremiseHeadings(tier domain.PlanningTier) []string {
	common := []string{
		"Thể loại và giọng điệu",
		"Định vị thể loại",
		"Xung đột cốt lõi",
		"Mục tiêu nhân vật chính",
		"Hướng kết cục",
		"Vùng cấm sáng tác",
		"Điểm bán hàng khác biệt",
		"Móc câu khác biệt",
		"Cam kết cốt lõi",
	}

	switch tier {
	case domain.PlanningTierLong:
		return append(common,
			"Động cơ câu chuyện",
			"Tuyến quan hệ/trưởng thành",
			"Lộ trình nâng cấp",
			"Chuyển hướng trung kỳ",
			"Mệnh đề kết cục",
		)
	case domain.PlanningTierMid:
		return append(common,
			"Động cơ câu chuyện",
			"Chuyển hướng trung kỳ",
		)
	case domain.PlanningTierShort:
		return append(common,
			"Tính phù hợp với truyện ngắn",
		)
	default:
		return common
	}
}