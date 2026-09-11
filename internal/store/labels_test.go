package store

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Nhãn khuôn mẫu phải theo ngôn ngữ tác phẩm: các file .md này được đọc ngược
// vào ngữ cảnh model, nên nhãn tiếng Trung trong truyện tiếng Việt là nguồn kéo
// model trôi ngôn ngữ, không chỉ là chuyện hiển thị.
func TestRenderersFollowLanguage(t *testing.T) {
	entries := []domain.OutlineEntry{
		{Chapter: 1, Title: "Vô Căn Dược Giả", CoreEvent: "Mở sạp bán thuốc", Hook: "Cây mai rung lên"},
	}
	chars := []domain.Character{
		{Name: "Ngọc Lâm", Role: "Chính", Description: "Dược nông", Arc: "Từ đáy đi lên",
			Traits: []string{"Cần cù", "Điềm tĩnh"}},
	}
	rules := []domain.WorldRule{{Category: "tu_luyen", Rule: "Không linh căn", Boundary: "Chậm nhưng bền"}}

	vi := strings.Join([]string{
		renderOutline(entries, labelsVI),
		renderCharacters(chars, labelsVI),
		renderWorldRules(rules, labelsVI),
	}, "\n")
	for _, han := range []string{"大纲", "第", "核心事件", "钩子", "角色档案", "特征", "世界观规则", "边界", "、"} {
		if strings.Contains(vi, han) {
			t.Errorf("bản tiếng Việt còn nhãn tiếng Trung %q", han)
		}
	}
	for _, want := range []string{"Đề cương", "Chương 1", "Sự kiện chính", "Điểm móc",
		"Hồ sơ nhân vật", "Đặc điểm", "Luật thế giới", "Ranh giới"} {
		if !strings.Contains(vi, want) {
			t.Errorf("thiếu nhãn tiếng Việt %q", want)
		}
	}

	// The outline renderer must never emit Chinese labels: no language branch exists.
	if zh := renderOutline(entries, labelsVI); strings.Contains(zh, "大纲") || strings.Contains(zh, "第 1 章") {
		t.Errorf("bản render lọt nhãn tiếng Trung: %q", zh)
	}
}
