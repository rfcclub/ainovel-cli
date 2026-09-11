package tools

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestParsePremiseSections(t *testing.T) {
	// A premise written the way the current prompts instruct (Vietnamese headings).
	premise := `# Tiền đề cốt truyện

## Thể loại và giọng điệu
Huyền huyễn phương Đông, tăng trưởng lạnh và cứng.

## Định vị thể loại
Truyện nâng cấp huyền huyễn phương Đông, hướng tới độc giả tìm khoái cảm và tiến triển quan hệ.

## Xung đột cốt lõi
Nhân vật chính phải chọn giữa quy tắc tông môn và lương tri cá nhân.

## Chuyển hướng trung kỳ
Lộ trình tu luyện cũ mất hiệu lực, buộc phải chuyển sang hệ cấm thuật.
`

	sections := parsePremiseSections(premise)
	for _, heading := range []string{
		"Thể loại và giọng điệu", "Định vị thể loại", "Xung đột cốt lõi", "Chuyển hướng trung kỳ",
	} {
		if sections[heading] == "" {
			t.Fatalf("expected %s section, got %+v", heading, sections)
		}
	}
}

// Legacy premises written before the Vietnamese migration use Chinese headings; they
// must still land on the canonical Vietnamese sections so migration keeps working.
func TestParsePremiseSections_LegacyChineseAliases(t *testing.T) {
	premise := `# Premise

## 题材和基调
东方玄幻，冷硬成长。

## 题材定位
东方玄幻升级流。

## 核心冲突
主角必须在宗门规则与个人良知之间做选择。

## 中期转向
旧有修炼路线失效，必须转向禁术体系。
`

	sections := parsePremiseSections(premise)
	for _, heading := range []string{
		"Thể loại và giọng điệu", "Định vị thể loại", "Xung đột cốt lõi", "Chuyển hướng trung kỳ",
	} {
		if sections[heading] == "" {
			t.Fatalf("legacy Chinese heading should alias to %s, got %+v", heading, sections)
		}
	}
}

// Models often echo the prompt's annotated heading ("Móc câu khác biệt: ...") verbatim;
// the section must still be recognised.
func TestParsePremiseSections_HeadingWithExplanatorySuffix(t *testing.T) {
	premise := `## Móc câu khác biệt: Điểm độc đáo nhất đáng để độc giả theo dõi cuốn sách này
Nhân vật chính nhớ lại mọi thứ đã xảy ra ở vòng lặp trước.

## Cam kết cốt lõi: Cuốn sách này liên tục mang lại điều gì cho độc giả
Mỗi chương đều trả một phần câu hỏi lớn.
`
	sections := parsePremiseSections(premise)
	if sections["Móc câu khác biệt"] == "" {
		t.Fatalf("suffixed heading should map to Móc câu khác biệt, got %+v", sections)
	}
	if sections["Cam kết cốt lõi"] == "" {
		t.Fatalf("suffixed heading should map to Cam kết cốt lõi, got %+v", sections)
	}
}

func TestPremiseStructure(t *testing.T) {
	premise := `## Thể loại và giọng điệu
Truyện nâng cấp, thiên lạnh và cứng.

## Định vị thể loại
Truyện nâng cấp.

## Xung đột cốt lõi
Xung đột.

## Mục tiêu nhân vật chính
Mục tiêu.

## Hướng kết cục
Kết cục.

## Vùng cấm sáng tác
Vùng cấm.

## Điểm bán hàng khác biệt
Điểm bán hàng.

## Móc câu khác biệt
Móc câu.

## Cam kết cốt lõi
Cam kết.

## Động cơ câu chuyện
Động cơ.

## Chuyển hướng trung kỳ
Bước ngoặt.
`

	structure := premiseStructure(premise, domain.PlanningTierMid)
	if ready, _ := structure["template_ready"].(bool); !ready {
		t.Fatalf("expected template_ready, got %+v", structure)
	}
	missing, _ := structure["missing"].([]string)
	if len(missing) != 0 {
		t.Fatalf("expected no missing headings, got %+v", missing)
	}
}

// A short-tier premise satisfies the template when it uses the Vietnamese headings the
// current prompt instructs, including the short-specific section.
func TestPremiseStructureShort(t *testing.T) {
	premise := `## Thể loại và giọng điệu
Giải cứu một tập, áp lực cao.

## Định vị thể loại
Phiêu lưu ngắn, mật độ cao.

## Xung đột cốt lõi
Nhân vật chính phải giải cứu con tin trong một đêm.

## Mục tiêu nhân vật chính
Cứu con tin và sống sót rời đi.

## Hướng kết cục
Hoàn thành nhiệm vụ nhưng phải trả giá.

## Vùng cấm sáng tác
Không mở rộng thành truyện dài kỳ.

## Điểm bán hàng khác biệt
Áp lực thời hạn và đảo chiều liên tục.

## Móc câu khác biệt
Mỗi lựa chọn đều rút ngắn thời gian giải cứu.

## Cam kết cốt lõi
Cảm giác gấp gáp, sự lựa chọn và đảo chiều.

## Tính phù hợp với truyện ngắn
Xung đột cốt lõi và cung nhân vật đều khép lại trong một nhiệm vụ.
`

	structure := premiseStructure(premise, domain.PlanningTierShort)
	if ready, _ := structure["template_ready"].(bool); !ready {
		t.Fatalf("expected short template_ready, got %+v", structure)
	}
}

// A legacy Chinese short premise still satisfies the template through the alias table.
func TestPremiseStructureShortAcceptsLegacyHeadingAlias(t *testing.T) {
	premise := `## 题材和基调
单卷高压营救。

## 题材定位
短篇高密度冒险。

## 核心冲突
主角必须在一夜内救出人质。

## 主角目标
救出人质并活着离开。

## 结局方向
完成任务但付出代价。

## 写作禁区
不扩展成长期连载。

## 差异化卖点
时限压力与连续反转。

## 差异化钩子
每次选择都缩短救援时间。

## 核心兑现承诺
紧迫感、抉择与反转。

## 本作为什么适合短篇/单卷收束
核心矛盾和人物弧线都能在单次任务中完成。
`

	structure := premiseStructure(premise, domain.PlanningTierShort)
	if ready, _ := structure["template_ready"].(bool); !ready {
		t.Fatalf("expected legacy short premise to still satisfy the template, got %+v", structure)
	}
}
