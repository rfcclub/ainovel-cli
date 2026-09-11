package rules

import "testing"

// Văn tiếng Việt sạch: luật cũ bắn 1021 lần trên chương 1 thật. Phải im.
func TestScriptMixingSilentOnCleanVietnamese(t *testing.T) {
	text := "Giả Mộc cúi xuống bên luống thuốc, ngón tay lần theo sống lá.\n\nHắn biết cây này ưa nắng sớm."
	for _, v := range appendScriptMixing(nil, text) {
		t.Fatalf("không được báo lỗi trên tiếng Việt sạch, nhận: %+v", v)
	}
}

// Han characters leaking into Vietnamese prose — exactly the error seen in the log: "dùng根系之力 ổn định".
func TestScriptMixingCatchesHanLeakInVietnamese(t *testing.T) {
	text := "Lữ Cầm buộc phải dùng根系之力 để ổn định làng quê trước cơn lũ đang tràn tới."
	vs := appendScriptMixing(nil, text)
	if len(vs) != 1 || vs[0].Rule != "cjk_leak" {
		t.Fatalf("mong đợi cjk_leak, nhận: %+v", vs)
	}
}

// Văn Trung lẫn tiếng Anh: hành vi cũ phải giữ nguyên.
func TestScriptMixingKeepsChineseBehaviour(t *testing.T) {
	text := "他在院中练剑，忽然想起那个 pattern 的用法，心神一乱。"
	vs := appendScriptMixing(nil, text)
	if len(vs) != 1 || vs[0].Rule != "non_cjk_fragments" {
		t.Fatalf("mong đợi non_cjk_fragments, nhận: %+v", vs)
	}
}

// Dấu câu Trung lọt vào văn Việt — DeepSeek tìm ra, luật chỉ bắt chữ Hán đã bỏ sót.
func TestScriptMixingCatchesCJKPunctuation(t *testing.T) {
	text := "Hắn muốn được tự mình chứng kiến。Gió thổi qua luống thuốc, lá lay động nhẹ."
	vs := appendScriptMixing(nil, text)
	if len(vs) != 1 || vs[0].Rule != "cjk_leak" {
		t.Fatalf("mong đợi cjk_leak cho dấu 。, nhận: %+v", vs)
	}
}

// Tên riêng bị cắt đôi qua dấu xuống đoạn — nguyên văn từ chương 4.
func TestBrokenWordCatchesSplitName(t *testing.T) {
	text := "Hắn bước ra khỏi Thanh Vân Tông, Ng\n\nọc Lâm dừng bước.\n\nTrước mặt là vực sâu."
	vs := appendBrokenWords(nil, text)
	if len(vs) != 1 || vs[0].Rule != "broken_word" {
		t.Fatalf("mong đợi broken_word, nhận: %+v", vs)
	}
}

func TestBrokenWordSilentOnNormalParagraphs(t *testing.T) {
	text := "Hắn cúi xuống bên luống thuốc.\n\nGió thổi qua, lá lay động nhẹ.\n\nNgọc Lâm dừng bước."
	if vs := appendBrokenWords(nil, text); len(vs) != 0 {
		t.Fatalf("không được báo trên văn bình thường, nhận: %+v", vs)
	}
}
