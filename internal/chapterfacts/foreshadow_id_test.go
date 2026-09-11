package chapterfacts

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Tái hiện sự cố thật: model đặt mọi phục bút mới là "new", nên từ lần thứ hai
// trở đi chúng đâm vào bản ghi cũ và bị nuốt lặng — 22 chương gieo 6 lần, vào sổ 2.
func TestRejectsPlaceholderForeshadowID(t *testing.T) {
	for _, id := range []string{"new", "New", " none ", "TBD", "-", "n/a", "auto"} {
		f := domain.ChapterFacts{
			Title:     "Chương 1: Dược Nông",
			Summary:   "Lê An lần đầu cảm nhận khí trời đất qua cây cỏ",
			KeyEvents: []string{"Lê An chạm vào cây thông già"},
			ForeshadowUpdates: []domain.ForeshadowUpdate{
				{ID: id, Action: "plant", Description: "Ánh mắt vô hình soi xét từ xa"},
			}}
		err := Validate(f)
		if err == nil {
			t.Errorf("id %q là chỗ giữ chỗ, phải bị bác bỏ", id)
			continue
		}
		if !strings.Contains(err.Error(), "chỗ giữ chỗ") {
			t.Errorf("id %q: thông báo phải giải thích lý do, nhận %v", id, err)
		}
	}
}

func TestAcceptsRealForeshadowID(t *testing.T) {
	for _, id := range []string{"FORESHADOW_OBSERVER", "jade-pendant", "f1", "quan-sat-vien"} {
		f := domain.ChapterFacts{
			Title:     "Chương 1: Dược Nông",
			Summary:   "Lê An lần đầu cảm nhận khí trời đất qua cây cỏ",
			KeyEvents: []string{"Lê An chạm vào cây thông già"},
			ForeshadowUpdates: []domain.ForeshadowUpdate{
				{ID: id, Action: "plant", Description: "Ánh mắt vô hình soi xét từ xa"},
			}}
		if err := Validate(f); err != nil {
			t.Errorf("id %q hợp lệ nhưng bị bác: %v", id, err)
		}
	}
}
