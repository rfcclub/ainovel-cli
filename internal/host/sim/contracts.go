package sim

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func textList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var sourceReportContract = llmcontract.Contract{
	Name:        "simulation_source_report",
	Description: "Trích xuất phương pháp viết có thể tái sử dụng từ một bài ngữ liệu mà không sao chép nguyên văn",
	Schema: schema.Object(
		schema.Property("title", llmcontract.Nullable(schema.String("Tiêu đề tùy chọn; null khi không xác nhận được"))).Required(),
		schema.Property("summary", schema.String("Khái quát giá trị thủ pháp của văn bản mẫu")).Required(),
		schema.Property("style_observations", textList("Quan sát về điểm nhìn tự sự, cấu trúc câu và chất liệu miêu tả")).Required(),
		schema.Property("common_words", textList("Các loại từ tần suất cao, hình ảnh và từ chuyển cảnh")).Required(),
		schema.Property("plot_patterns", textList("Mô thức đẩy tình tiết, bước ngoặt và leo thang xung đột")).Required(),
		schema.Property("hook_patterns", textList("Mô thức móc câu mở đầu, cuối chương và chênh lệch thông tin")).Required(),
		schema.Property("pacing_notes", textList("Mật độ phân cảnh và nhịp giải phóng thông tin")).Required(),
		schema.Property("reader_appeal", textList("Cách thu hút độc giả đọc tiếp")).Required(),
		schema.Property("reusable_techniques", textList("Thủ pháp cấu trúc có thể học hỏi")).Required(),
		schema.Property("warnings", textList("Rủi ro sao chép và áp khuôn cần tránh")).Required(),
	),
}

var synthesisContract = llmcontract.Contract{
	Name:        "simulation_synthesis",
	Description: "Tổng hợp hồ sơ sẵn có và báo cáo ngữ liệu thành hồ sơ phương pháp mô phỏng có thể thực thi",
	Schema: schema.Object(
		schema.Property("style", schema.Object(
			schema.Property("narrative_voice", textList("Ngôi kể, khoảng cách và kiểm soát thông tin")).Required(),
			schema.Property("sentence_rhythm", textList("Nhịp điệu cấu trúc câu")).Required(),
			schema.Property("prose_texture", textList("Chất liệu miêu tả")).Required(),
			schema.Property("perspective", textList("Quy tắc điểm nhìn")).Required(),
			schema.Property("mood", textList("Tông cảm xúc")).Required(),
			schema.Property("do_not_copy", textList("Nội dung cấm sao chép")).Required(),
		)).Required(),
		schema.Property("lexicon", schema.Object(
			schema.Property("common_words", textList("Loại từ thường dùng")).Required(),
			schema.Property("emotion_words", textList("Loại từ cảm xúc")).Required(),
			schema.Property("scene_words", textList("Loại từ phân cảnh")).Required(),
			schema.Property("transition_words", textList("Loại từ chuyển cảnh")).Required(),
			schema.Property("signature_phrases", textList("Đặc trưng giọng điệu sau khi trừu tượng hóa, không chứa câu gốc")).Required(),
		)).Required(),
		schema.Property("plot_design", schema.Object(
			schema.Property("opening_patterns", textList("Cách mở đầu")).Required(),
			schema.Property("escalation_patterns", textList("Cách leo thang xung đột")).Required(),
			schema.Property("turning_point_patterns", textList("Thiết kế bước ngoặt")).Required(),
			schema.Property("payoff_patterns", textList("Cách thu hồi và trả điểm")).Required(),
		)).Required(),
		schema.Property("hook_design", schema.Object(
			schema.Property("hook_types", textList("Loại móc câu")).Required(),
			schema.Property("placement", textList("Vị trí móc câu")).Required(),
			schema.Property("cliffhanger_patterns", textList("Cách treo lửng gây hồi hộp")).Required(),
			schema.Property("payoff_rules", textList("Quy tắc trả móc câu")).Required(),
		)).Required(),
		schema.Property("pacing_density", schema.Object(
			schema.Property("scene_density", textList("Mật độ thông tin mỗi phân cảnh")).Required(),
			schema.Property("information_release", textList("Nhịp giải phóng thông tin")).Required(),
			schema.Property("dialogue_action_ratio", textList("Tỷ lệ đối thoại, hành động và nội tâm")).Required(),
			schema.Property("compression_rules", textList("Quy tắc mở rộng và nén nội dung")).Required(),
		)).Required(),
		schema.Property("reader_engagement", schema.Object(
			schema.Property("methods", textList("Cách thu hút độc giả")).Required(),
			schema.Property("emotional_drivers", textList("Động lực cảm xúc")).Required(),
			schema.Property("progression_rewards", textList("Phần thưởng tiến triển theo giai đoạn")).Required(),
			schema.Property("anti_patterns", textList("Mô thức phản tác dụng làm giảm sức hút")).Required(),
		)).Required(),
		schema.Property("role_guidance", schema.Object(
			schema.Property("architect", textList("Quy tắc để Architect dùng hồ sơ")).Required(),
			schema.Property("writer", textList("Quy tắc để Writer học hỏi mà không sao chép")).Required(),
			schema.Property("editor", textList("Quy tắc để Editor kiểm tra hướng đi và rủi ro xâm phạm")).Required(),
		)).Required(),
	),
}
