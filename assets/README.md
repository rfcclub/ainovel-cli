# Bản đồ nội dung assets

Trước khi thêm "một đoạn văn / một tài liệu / một quy tắc" vào hệ thống, hãy tra bảng dưới đây để xác định nơi thuộc về, rồi xem cách đấu nối.

| Thư mục | Chứa gì | Ai tiêu thụ | Cách đấu nối |
|---|---|---|---|
| `prompts/` | system prompt của Worker (writer / editor / architect×2), prompt phán định của Arbiter và prompt tác vụ một lần (import / simulation / revision) | `agents/build.go`, `internal/arbiter`, imp / sim / revision runner | trường Prompts của `load.go`. Lưu ý: simulation_guidance do `load.go` chèn lúc nạp, không thấy trong file md |
| `references/` | tài liệu kiến thức viết lách không phụ thuộc thể loại. Không vào system prompt, do novel_context cắt theo vai trò / chương rồi chèn vào `reference_pack` | writer / editor / architect | **ba chỗ đấu nối**: thêm trường vào `tools.References` + `load.go` loadReferences đọc + `novel_context.go` writerReferences / architectReferences chèn. Chỉ bỏ vào thư mục thì không tự động nạp |
| `references/genres/<style>/` | kiến thức riêng theo thể loại (style-references / arc-templates) | như trên, nạp khi `style != default` | `load.go` loadReferences |
| `rules/` | thư mục quy tắc tích hợp cũ đã bỏ; baseline cơ học đã chuyển vào code, quy tắc người dùng đến từ ảnh chụp ngôn ngữ tự nhiên `~/.ainovel/rules/*.md` / `./.ainovel/rules/*.md` | `userrules.Service` chuẩn hóa thành `meta/user_rules.json`; `novel_context` chèn; `commit_chapter` kiểm tra | baseline tích hợp xem `SystemDefaults()` trong `internal/rules/snapshot.go`; `.md` của người dùng không định dạng, không YAML, chuẩn hóa theo ngôn ngữ tự nhiên |
| `styles/<style>.md` | chỉ thị văn phong viết theo thể loại | ghép vào system prompt của **writer** (`agents/build.go`) | tên file chính là giá trị `config.style`. Cùng khái niệm thể loại với `references/genres/<style>/` nhưng hai vật mang: cái trước là chỉ thị văn phong, cái sau là tài liệu kiến thức |

## Phán đoán nơi thuộc về của nội dung mới (năm câu hỏi)

1. Quy trình này bắt buộc phải được **bảo đảm**? → không viết prompt, mà viết ràng buộc bằng code (StopAfterTools / công cụ gác / Flow Router)
2. Đây là tiêu chí phán định? → quy trình dạng tra bảng viết vào `internal/flow/router.go`; phán đoán ngữ nghĩa viết vào `prompts/arbiter-*.md`
3. Đây là chuẩn thẩm mỹ / thi hành của một vai trò? → `prompts/<role>.md`
4. Đây là quy tắc mặc định có thể liệt kê cơ học (từ cấm / ngưỡng)? → `SystemDefaults()` trong `internal/rules/snapshot.go`; quy tắc tùy chỉnh của người dùng viết vào `.ainovel/rules/*.md`, do ảnh chụp chuẩn hóa tiêu thụ (số chữ/độ dài là ràng buộc mềm ngữ nghĩa, đi qua preferences, không làm quy tắc cơ học)
5. Đây là tài liệu kiến thức viết lách? → `references/` (nhớ ba chỗ đấu nối)

## Bảo đảm tính nhất quán

Đường dẫn phong bì mà prompt tham chiếu (`working_memory.*` v.v.) phải nhất quán với `novel_context`. Hình dạng tham số công cụ chỉ được định nghĩa trong Schema của công cụ; prompt chỉ bổ sung ngữ nghĩa nghiệp vụ mà Schema không diễn đạt được, không chép lại danh sách tham số JSON và ví dụ hình dạng.

Prompt có thể mô tả cách thi hành của một Worker đơn lẻ, nhưng định tuyến toàn cục, chuyển trạng thái và logic khôi phục chỉ lấy code làm chuẩn. Bước nào xác định được từ sự thật trong Store thì đặt vào Router/Tool; chỉ phán đoán cần hiểu nội dung truyện hoặc ý định người dùng mới để lại cho model.
