package imp

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func nullableString(description string) map[string]any {
	return llmcontract.Nullable(schema.String(description))
}

func stringList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var segmentContract = llmcontract.Contract{
	Name:        "import_segment",
	Description: "Nhận diện ranh giới chương, tập/thiên và văn bản phụ trợ trong văn bản nhập vào",
	Schema: schema.Object(
		schema.Property("boundaries", schema.Array("Các ranh giới xếp theo thứ tự trong nguyên văn", schema.Object(
			schema.Property("unit_id", schema.String("unit id nằm trong khoảng owned")).Required(),
			schema.Property("anchor", nullableString("Đoạn nguyên văn định vị khi một unit có nhiều ranh giới; nếu không thì null")).Required(),
			schema.Property("kind", schema.Enum("Loại ranh giới", kindChapter, kindGroup, kindFrontMatter, kindBackMatter)).Required(),
			schema.Property("title", nullableString("Nguyên văn tiêu đề; null khi không có tiêu đề")).Required(),
			schema.Property("uncertain", schema.Bool("Có cần người dùng xác nhận hay không")).Required(),
			schema.Property("reason", nullableString("Lý do chưa chắc chắn; null khi không cần giải thích")).Required(),
		))).Required(),
	),
}

var analysisContract = llmcontract.Contract{
	Name:        "import_chapter_analysis",
	Description: "Trích xuất các sự thật truyện có thể truy vết từ các chương liên tiếp",
	Schema: schema.Object(
		schema.Property("chapters", schema.Array("Sự thật từng chương khớp thứ tự số chương đầu vào", chapterFactsSchema())).Required(),
	),
}

func chapterFactsSchema() map[string]any {
	characterEvidence := schema.Object(
		schema.Property("chapter", schema.Int("Chương chứa bằng chứng")).Required(),
		schema.Property("name", schema.String("Tên nhân vật")).Required(),
		schema.Property("note", nullableString("Sự thật về nhân vật; không có thì null")).Required(),
	)
	worldEvidence := schema.Object(
		schema.Property("chapter", schema.Int("Chương chứa bằng chứng")).Required(),
		schema.Property("category", nullableString("Loại sự thật thế giới; null khi không phân loại được")).Required(),
		schema.Property("fact", schema.String("Sự thật thế giới được chính văn nêu rõ")).Required(),
	)
	timelineEvent := schema.Object(
		schema.Property("chapter", schema.Int("Số chương")).Required(),
		schema.Property("time", schema.String("Thời gian trong truyện")).Required(),
		schema.Property("event", schema.String("Sự kiện")).Required(),
		schema.Property("characters", stringList("Nhân vật liên quan")).Required(),
	)
	foreshadow := schema.Object(
		schema.Property("id", schema.String("Tái sử dụng ID phục bút trong ledger")).Required(),
		schema.Property("action", schema.Enum("Hành động phục bút", "plant", "advance", "resolve")).Required(),
		schema.Property("description", nullableString("Mô tả phục bút khi plant; các trường hợp khác có thể null")).Required(),
	)
	relationship := schema.Object(
		schema.Property("character_a", schema.String("Nhân vật A")).Required(),
		schema.Property("character_b", schema.String("Nhân vật B")).Required(),
		schema.Property("relation", schema.String("Thay đổi quan hệ")).Required(),
		schema.Property("chapter", schema.Int("Số chương")).Required(),
	)
	stateChange := schema.Object(
		schema.Property("chapter", schema.Int("Số chương")).Required(),
		schema.Property("entity", schema.String("Nhân vật hoặc thực thể")).Required(),
		schema.Property("field", schema.String("Thuộc tính đã thay đổi")).Required(),
		schema.Property("old_value", nullableString("Trạng thái trước khi thay đổi; null khi xuất hiện lần đầu")).Required(),
		schema.Property("new_value", schema.String("Trạng thái sau khi thay đổi")).Required(),
		schema.Property("reason", nullableString("Nguyên nhân thay đổi; null khi chính văn không nêu")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương")).Required(),
		schema.Property("title", schema.String("Tiêu đề chương")).Required(),
		schema.Property("summary", schema.String("Tóm lược chương này")).Required(),
		schema.Property("key_events", stringList("Sự kiện then chốt")).Required(),
		schema.Property("core_event", schema.String("Điều quan trọng nhất của chương này")).Required(),
		schema.Property("hook", nullableString("Móc câu cuối chương; không có thì null")).Required(),
		schema.Property("scenes", stringList("Chuỗi phân cảnh")).Required(),
		schema.Property("characters", stringList("Nhân vật xuất hiện")).Required(),
		schema.Property("character_evidence", schema.Array("Bằng chứng về nhân vật", characterEvidence)).Required(),
		schema.Property("world_evidence", schema.Array("Bằng chứng sự thật thế giới", worldEvidence)).Required(),
		schema.Property("timeline_events", schema.Array("Sự kiện dòng thời gian", timelineEvent)).Required(),
		schema.Property("foreshadow_updates", schema.Array("Phần gia tăng phục bút", foreshadow)).Required(),
		schema.Property("relationship_changes", schema.Array("Thay đổi quan hệ", relationship)).Required(),
		schema.Property("state_changes", schema.Array("Thay đổi trạng thái", stateChange)).Required(),
		schema.Property("hook_type", schema.Enum("Loại móc câu cuối chương", domain.HookTypes()...)).Required(),
		schema.Property("dominant_strand", schema.Enum("Tuyến tự sự chủ đạo", domain.DominantStrands()...)).Required(),
	)
}

var rangeContract = llmcontract.Contract{
	Name:        "import_range_digest",
	Description: "Khái quát cốt truyện và sự thật của một khoảng chương liên tục",
	Schema: schema.Object(
		schema.Property("start_chapter", schema.Int("Chương đầu khoảng")).Required(),
		schema.Property("end_chapter", schema.Int("Chương cuối khoảng")).Required(),
		schema.Property("plot", schema.String("Diễn tiến cốt truyện chính xuyên chương")).Required(),
		schema.Property("characters", stringList("Nhân vật có tiến triển thực chất")).Required(),
		schema.Property("world_facts", stringList("Sự thật thế giới đã được xác lập")).Required(),
		schema.Property("opened_threads", stringList("Tuyến dài mới mở trong khoảng này")).Required(),
		schema.Property("resolved_threads", stringList("Tuyến dài đã khép lại trong khoảng này")).Required(),
	),
}

var synthesisContract = llmcontract.Contract{
	Name:        "import_book_synthesis",
	Description: "Tổng hợp sự thật toàn sách và đưa ra phạm vi tập/cung liên tục, đầy đủ",
	Schema: schema.Object(
		schema.Property("title", nullableString("Tên sách chính thức trong chính văn; null khi không xác nhận được")).Required(),
		schema.Property("synopsis", schema.String("Giới thiệu tiểu thuyết không spoil dành cho độc giả")).Required(),
		schema.Property("premise", schema.String("Mô tả tiền đề cốt truyện bằng Markdown")).Required(),
		schema.Property("characters", schema.Array("Nhân vật chính", schema.Object(
			schema.Property("name", schema.String("Tên nhân vật")).Required(),
			schema.Property("aliases", stringList("Bí danh và danh hiệu")).Required(),
			schema.Property("role", schema.String("Vai trò tự sự")).Required(),
			schema.Property("description", schema.String("Mô tả nhân vật")).Required(),
			schema.Property("arc", schema.String("Cung nhân vật")).Required(),
			schema.Property("traits", stringList("Đặc điểm nhân vật")).Required(),
			schema.Property("tier", nullableString("Cấp bậc nhân vật; null khi không phán đoán được")).Required(),
		))).Required(),
		schema.Property("world_rules", schema.Array("Quy tắc thế giới được chính văn xác lập", schema.Object(
			schema.Property("category", schema.String("Loại quy tắc")).Required(),
			schema.Property("rule", schema.String("Mô tả quy tắc")).Required(),
			schema.Property("boundary", schema.String("Ranh giới không được vi phạm")).Required(),
		))).Required(),
		schema.Property("structure", schema.Array("Phạm vi chương liên tục của tập và cung", schema.Object(
			schema.Property("title", schema.String("Tiêu đề tập")).Required(),
			schema.Property("theme", schema.String("Xung đột cốt lõi hoặc chủ đề của tập")).Required(),
			schema.Property("arcs", schema.Array("Cung truyện trong tập", schema.Object(
				schema.Property("title", schema.String("Tiêu đề cung")).Required(),
				schema.Property("goal", schema.String("Mục tiêu cung")).Required(),
				schema.Property("start_chapter", schema.Int("Chương bắt đầu")).Required(),
				schema.Property("end_chapter", schema.Int("Chương kết thúc")).Required(),
			))).Required(),
		))).Required(),
		schema.Property("compass", schema.Object(
			schema.Property("ending_direction", schema.String("Hướng kết cục")).Required(),
			schema.Property("open_threads", stringList("Các tuyến dài chưa khép lại")).Required(),
			schema.Property("estimated_scale", nullableString("Quy mô phỏng chừng; null khi không phán đoán được")).Required(),
			schema.Property("last_updated", llmcontract.Nullable(schema.Int("Số chương mới nhất làm căn cứ; null khi không cần điền"))).Required(),
		)).Required(),
		schema.Property("planning_tier", schema.Enum("Cấp quy hoạch", "short", "mid", "long")).Required(),
		schema.Property("story_status", schema.Enum("Truyện đã kết thúc hay chưa", storyOpen, storyClosed, storyUncertain)).Required(),
		schema.Property("status_reason", nullableString("Lý do phán định trạng thái")).Required(),
	),
}
