# Chapter Advance Gate

> Trạng thái: đã triển khai
> Ngày: 2026-07-14
> Giải quyết: nghiệm thu từng chương, tạm dừng an toàn sau can thiệp, giấy phép chương chính xác khi khôi phục sau sập

## 1. Vì sao cần nó

Rủi ro cốt lõi của sáng tác dài tự động không phải là tiêu thêm một lệnh gọi, mà là hệ thống tiếp tục ghi chương mới trong lúc người dùng đang đọc thẩm, rồi gấp các tóm tắt, trạng thái nhân vật và phản hồi đại cương dựa trên cốt truyện cũ vào nguồn sự thật phía sau. Xóa một chương viết thừa không tự hoàn tác được những trạng thái dẫn xuất đó, và người dùng sẽ mất niềm tin vào quá trình sáng tác.

Dự án vẫn lấy định vị mặc định là "đưa ra mục tiêu rồi tự chủ hoàn thành liên tục", nên không biến việc xác nhận từng chương thành mặc định toàn cục. Hệ thống đưa ra hai chính sách rõ ràng:

- `auto`: chế độ mặc định, đẩy tự chủ liên tục;
- `review`: chế độ nghiệm thu từng chương do người dùng chủ động chọn, mỗi chương mới thuận chiều đều cần một giấy phép chính xác.

Đây không phải là trả luồng công việc về cho Coordinator LLM. Khi nào cần người dùng xác nhận là chính sách của người dùng; luồng tất định tiếp theo vẫn do Route suy ra; chỉ việc có cần dừng lại một lần để nghiệm thu kết quả can thiệp hay không mới do Arbiter phán đoán ngữ nghĩa.

## 2. Phân chia ranh giới

| Câu hỏi | Thuộc về | Lý do |
|---|---|---|
| Hiện có phải chế độ nghiệm thu từng chương không | RunMeta / Host | Ý định vận hành bền vững của người dùng |
| Chương nào đã được cấp phép | RunMeta / Gate | Sự thật cơ học có thể kiểm chứng, có thể khôi phục |
| Bước tiếp theo chạy Worker nào | `flow.Route` | Suy ra bằng hàm thuần từ sự thật sáng tác |
| Chỉ thị có mở một chương mới thuận chiều không | `flow.StartsForwardChapter` | Phán đoán cơ học có kiểu |
| "Sửa xong cho tôi xem" có cần tạm dừng không | Arbiter | Phán đoán ngữ nghĩa ngôn ngữ tự nhiên |
| Tạm dừng kích hoạt khi nào | `ChapterAdvanceGate` | Thực thi tất định trên một ý định một lần |
| Ngân sách có cho phép tiếp tục không | `BudgetSentinel` | Chính sách Host độc lập |

`AdvanceMode`, giấy phép chương và hold một lần không vào bảng quyết định của Route, và cũng không cho model sửa. Máy trạng thái sáng tác của Route trực giao với chính sách nghiệm thu từng chương.

## 3. Mô hình trạng thái tối thiểu

`meta/run.json` chỉ thêm ba ý định vận hành:

```go
type RunMeta struct {
	AdvanceMode          ChapterAdvanceMode `json:"advance_mode"`
	AdvancePermitChapter int                `json:"advance_permit_chapter,omitempty"`
	AdvanceHold          *AdvanceHold       `json:"advance_hold,omitempty"`
}

const (
	ChapterAdvanceAuto   ChapterAdvanceMode = "auto"
	ChapterAdvanceReview ChapterAdvanceMode = "review"
)

const (
	AdvanceHoldAtBoundary           AdvanceHoldAfter = "boundary"
	AdvanceHoldAfterRewritesDrained AdvanceHoldAfter = "rewrites_drained"
	AdvanceHoldAtChapter            AdvanceHoldAfter = "chapter"
)

type AdvanceHold struct {
	After         AdvanceHoldAfter `json:"after"`
	TargetChapter int              `json:"target_chapter,omitempty"`
	Reason        string           `json:"reason"`
}
```

Không có PolicyEngine tổng quát, mảng điều kiện, hàng đợi giấy phép, thời điểm hết hạn hay phiên bản chính sách. Việc đẩy chương chỉ giữ một chế độ bền vững, một giấy phép chính xác và một hold một lần có kiểu.

### 3.1 Bất biến

1. `AdvanceMode` chỉ được là `auto` hoặc `review`; giá trị không xác định trả `UnsupportedAdvanceModeError`.
2. Chế độ không xác định không được khởi động Host, cũng không được ghi lại RunMeta.
3. Ở `auto`, giấy phép bắt buộc phải là `0`.
4. Ở `review`, giấy phép chỉ được là `0` hoặc một số chương nguyên dương.
5. Cấp phép lặp lại cùng mục tiêu là idempotent; mục tiêu khác không được ghi đè giấy phép đang trên đường.
6. Giấy phép chỉ ràng buộc "bắt đầu một chương mới thuận chiều chưa hoàn thành"; quy hoạch, thẩm duyệt, làm lại, gọt giũa và khôi phục commit không bị chặn.
7. Giấy phép gắn với số chương, không gắn với một lần chạy tiến trình hay một lần gọi Worker.
8. Giấy phép chỉ được coi là tiêu thụ ổn định khi chương mục tiêu đã vào `CompletedChapters`, `PendingCommit` tương ứng đã rỗng, và checkpoint `commit` của chương đó tồn tại.
9. Chương mục tiêu đã hoàn thành nhưng thiếu checkpoint commit là trạng thái hỏng: báo lỗi rõ ràng và tạm dừng, không đoán mò sửa.
10. Giấy phép chưa tiêu thụ phải bằng `Progress.NextChapter()`. `PendingRewrites` không đổi `NextChapter()`, nên làm lại và giấy phép thuận chiều đang trên đường có thể cùng tồn tại một cách cơ học.
11. `AdvanceHold` chỉ được dùng `boundary`, `rewrites_drained` hoặc `chapter`, và phải mang lý do khác rỗng; `chapter` phải mang chương mục tiêu là số dương, các điều kiện khác cấm mang theo.
12. hold và giấy phép dùng compare-and-clear; khi trạng thái bị hành động mới thay thế thì không được xóa nhầm.
13. Hold theo chương mục tiêu ở chế độ `review` tạo thành một uỷ quyền khoảng một lần; sau khi tạm dừng, chính sách nghiệm thu từng chương sẵn có giữ nguyên.

## 4. Store API

RunMetaStore cung cấp các thao tác nguyên tử hẹp và có kiểu:

```go
SetAdvanceMode(mode domain.ChapterAdvanceMode) error
GrantAdvancePermit(chapter int) error
ClearAdvancePermit(chapter int) error
SetAdvanceHold(hold domain.AdvanceHold) error
ClearAdvanceHold(expected domain.AdvanceHold) error
```

- Khi chuyển về `auto`, giấy phép chương được xóa trong cùng một khóa ghi, nhưng không xóa hold do một can thiệp khác của người dùng tạo ra;
- Cấp phép chỉ hợp lệ dưới `review`;
- Thao tác xóa chỉ tiêu thụ đúng mục tiêu mà bên gọi vừa đọc;
- Khi khởi tạo, RunMeta mặc định chế độ là `auto`, và giữ lại chế độ, giấy phép cùng hold đã ghi xuống đĩa.

Dự án hiện không có dữ liệu lịch sử cần di trú, nên bản triển khai không gồm đọc trường cũ, ghi kép hay nhánh hạ cấp.

## 5. Ngữ nghĩa hàm thuần

### 5.1 Nhận diện chương mới thuận chiều

```go
func StartsForwardChapter(
	inst *Instruction,
	progress *domain.Progress,
	pending *domain.PendingCommit,
) bool
```

Chỉ trả về true khi các điều kiện sau đồng thời đúng:

- Worker là `writer`;
- phase là `writing`;
- không có `PendingCommit`;
- không có hàng đợi làm lại;
- không có `InProgressChapter`;
- chương mục tiêu bằng `NextChapter()`.

Việc phán đoán chỉ đọc các trường có kiểu, không phân tích văn bản Task hay Reason.

### 5.2 Hold một lần

`ResolveAdvanceHold` trả về theo hold và Progress:

- `keep`: điều kiện chưa thoả;
- `consume`: ở trạng thái kết thúc sách chỉ cần dọn ý định;
- `consume-and-stop`: dọn ý định và tạm dừng.

`boundary` kích hoạt ở ranh giới Worker hiện tại; `rewrites_drained` kích hoạt sau khi hàng đợi làm lại cạn; `chapter` kích hoạt sau khi chương mục tiêu vào danh sách hoàn thành, `PendingCommit` rỗng và checkpoint commit tồn tại. Điều kiện không xác định và sự thật thiếu đều báo lỗi trực tiếp.

## 6. ChapterAdvanceGate

Gate là thành phần chính sách duy nhất cho việc đẩy sáng tác ngoài ngân sách, với đúng hai nhiệm vụ:

1. Phân giải và tiêu thụ hold một lần ở ranh giới vòng lặp;
2. Kiểm tra giấy phép từng chương trước khi giao việc cho writer, và đối chiếu ở ranh giới xem giấy phép đã tiêu thụ ổn định chưa.

Thứ tự trong Engine:

```text
Commit can thiệp đang chờ
→ Gate kiểm tra ranh giới
→ Route / lấy việc Arbiter giao
→ precheck
→ Gate kiểm tra giấy phép khi giao việc
→ Worker
→ Budget kiểm tra ranh giới
→ Gate kiểm tra ranh giới
→ vòng sau
```

Khi `auto && hold == nil`, phần kiểm tra ranh giới đọc RunMeta rồi trả về ngay, không đọc Progress, PendingCommit hay checkpoint.

### 6.1 hold + dispatch

Arbiter có thể phán định "viết lại chương 3, sửa xong cho tôi xem" thành:

```json
{
  "hold": {
    "after": "rewrites_drained",
    "reason": "chờ người dùng nghiệm thu sau khi viết lại xong"
  },
  "dispatch": {
    "agent": "editor",
    "task": "thẩm duyệt lại chương 3 và lập hàng đợi làm lại theo kết quả"
  }
}
```

Nhóm hành động này phải thực thi việc giao việc đi kèm trước, để Editor tạo ra sự thật làm lại, rồi Gate mới phán đoán hàng đợi đã cạn chưa. Engine gắn "hoãn Gate trong lần giao việc này" với chính chỉ thị trong bộ nhớ đó, và xóa luôn khi lấy chỉ thị đi; việc giao việc thông thường của Arbiter không được vòng qua Gate.

### 6.2 permit và làm lại

`reopen` sau khi kết thúc sách chỉ xảy ra ở `complete`, còn `/next` chỉ xảy ra ở `writing`, hai điều này loại trừ nhau về mặt cơ học. `PendingRewrites` đã tồn tại trong giai đoạn viết không đổi số chương hoàn thành lớn nhất, nên giấy phép vẫn khớp cùng một `NextChapter()`; Worker làm lại có thể chạy, nhưng không tiêu thụ giấy phép thuận chiều.

## 7. Khôi phục sau sập

Commit chương là một saga nhiều bước, nên giấy phép không thể biểu diễn bằng một giá trị boolean kiểu "lần chạy sau được viết một chương". Khi khôi phục, Gate đối chiếu theo ba loại sự thật:

| Cửa sổ sự thật | Hành vi của Gate |
|---|---|
| Chương mục tiêu chưa hoàn thành, không có PendingCommit | Giữ giấy phép, cho phép bắt đầu/khôi phục chương đó |
| PendingCommit thuộc chương mục tiêu | Giữ giấy phép, để việc khôi phục commit hoàn tất |
| Chương mục tiêu hoàn thành, PendingCommit rỗng, checkpoint commit tồn tại | Tiêu thụ giấy phép |
| Chương mục tiêu hoàn thành nhưng thiếu checkpoint | Báo lỗi và tạm dừng |
| Giấy phép trỏ tới một chương chưa hoàn thành khác NextChapter | Báo lỗi và tạm dừng |

Nhờ đó, tiến trình sập ở bất kỳ cửa sổ nào — bản nháp, ghi trạng thái, đánh dấu tiến độ hay ghi tín hiệu — đều không dùng nhầm cùng một giấy phép cho chương kế tiếp.

## 8. Arbiter

Schema can thiệp dùng `AdvanceHoldOp`:

```go
type AdvanceHoldOp struct {
	Cancel        bool                    `json:"cancel,omitempty"`
	After         domain.AdvanceHoldAfter `json:"after,omitempty"`
	TargetChapter int                     `json:"target_chapter,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
}
```

Quy tắc:

- "khoan dừng một chút" rõ ràng dùng `boundary`;
- Dưới `auto`, "sửa chương đã viết, xong cho tôi nghiệm thu" dùng `rewrites_drained`;
- "viết tới chương N" dùng `chapter`, phân biệt nghiêm ngặt với điều chỉnh dung lượng "cả sách gồm N chương";
- `review` vốn đã dừng từng chương, không tạo thêm hold đồng nghĩa;
- Hold theo chương mục tiêu dưới `review` là uỷ quyền hàng loạt một lần người dùng ký rõ ràng;
- "viết tiếp" có thể hủy hold hiện có, nhưng không được cấp giấy phép chương;
- Đổi chế độ chỉ dùng `/review on|off`, cho qua chỉ dùng `/next`.

Engine gọi thẳng RunMetaStore để áp dụng hành động có cấu trúc, không ngụy trang nó thành LLM Tool.

## 9. Giao diện người dùng

### 9.1 `/review on|off`

- `/review on`: lưu ngay chính sách nghiệm thu từng chương; nếu Worker đang chạy, sau khi công việc hiện tại xong sẽ dừng trước chương mới thuận chiều kế tiếp;
- `/review off`: chuyển về đẩy tự động và xóa giấy phép theo kiểu nguyên tử; không ngầm khởi động Engine đã tạm dừng, sự kiện sẽ nhắc rõ người dùng nhập chỉ thị viết tiếp.

### 9.2 `/next`

Chỉ dùng được khi các điều kiện sau đồng thời đúng:

- Engine không chạy;
- không đang đồng sáng tác giai đoạn;
- chế độ là `review`;
- không có hold đang chờ xử lý;
- ngân sách cho phép;
- phase là `writing`.

Lệnh cấp giấy phép chính xác cho `NextChapter()` và khởi động Engine. Thông báo sẽ nói rõ: sau khi chương đó commit, việc thẩm duyệt cần thiết cùng bảo trì cấu trúc cung/tập vẫn hoàn tất, rồi lại chờ cho qua.

### 9.3 Hiển thị trạng thái

`UISnapshot` là nguồn sự thật duy nhất của TUI, gồm:

- `AdvanceMode`;
- `AdvancePermitChapter`;
- `HasAdvanceHold`;
- `AdvanceHoldReason`.

Cột bên hiển thị trạng thái tự động/nghiệm thu từng chương và chương đã cho qua; khi chờ, ô nhập gợi ý "nhập ý kiến sửa, hoặc `/next` để cho qua chương kế tiếp". Loại thông báo là `advance_gate`.

## 10. Kiểm chứng

Bài kiểm tra bao phủ:

- Chuyển trạng thái nguyên tử và compare-and-clear của chế độ, giấy phép, hold trong RunMeta;
- Chế độ không xác định thất bại rõ ràng và không ghi lại RunMeta;
- Nhận diện hàm thuần cho chương mới thuận chiều và cho làm lại/khôi phục;
- Ngữ nghĩa boundary, làm lại chưa cạn, làm lại đã cạn và kết thúc sách của hold;
- Chặn khi không có giấy phép, cho qua khi giấy phép chính xác, báo lỗi khi giấy phép sai chương;
- Giữ giấy phép trong lúc có PendingCommit, tiêu thụ sau khi commit ổn định;
- Tạm dừng khi đánh dấu hoàn thành xung đột với checkpoint;
- permit xen kẽ PendingRewrites không báo nhầm;
- Đầu-cuối ở Engine chứng minh một giấy phép ổn định đúng một chương mới;
- Khi Gate đã đánh dấu tạm dừng nhưng goroutine Engine cũ còn đang thoát, `/next` từ chối vào lại rõ ràng, thử lại sau khôi phục idempotent theo cùng giấy phép chương đó;
- Hồi quy cho hold-only, hold+dispatch và tranh chấp lúc thoát.

## 11. Dứt khoát không làm

- Không để model quyết định chế độ vận hành hay cấp giấy phép;
- Không sửa Route để thích ứng chính sách xác nhận của người dùng;
- Không biến làm lại, quy hoạch, thẩm duyệt và bảo trì cấu trúc thành xác nhận từng bước;
- Không thêm PolicyEngine tổng quát, danh sách StopCondition hay DSL chính sách;
- Không cấp trước nhiều chương hay hàng đợi giấy phép;
- Không giữ mô hình tạm dừng cũ, trường tương thích, DTO di trú hay chuỗi ghi kép;
- Không âm thầm hạ cấp cho chế độ tương lai chưa biết.

Nếu tương lai xuất hiện nhu cầu biên tự trị mới được kiểm chứng lặp lại, hãy mở rộng chế độ dựa trên bằng chứng; chi phí hối hận thấp hiện tại chính là khả năng tương thích tương lai.