# Giải thích quản lý ngữ cảnh

Tài liệu này trình bày hệ thống quản lý ngữ cảnh hiện tại của `ainovel-cli`, gồm:

- Vì sao phải quản lý ngữ cảnh
- Ngữ cảnh đến từ đâu
- Lúc chạy thì nén, khôi phục và bàn giao như thế nào
- Giá trị, điều kiện kích hoạt và tình huống áp dụng của từng chiến lược
- Khi có sự cố thì nên xem ở đâu trước

Mục tiêu không phải giới thiệu khái niệm trừu tượng, mà để người bảo trì sau này mở đúng tài liệu này là hiểu nhanh bản triển khai hiện tại cùng các cửa vào chẩn đoán.

## 1. Mục tiêu thiết kế

Quản lý ngữ cảnh của dự án này không phải kịch bản chat tổng quát mà hướng tới kịch bản sáng tác tiểu thuyết. Nó phải đồng thời giải mấy lớp vấn đề:

1. Hội thoại dài sẽ vượt cửa sổ ngữ cảnh của model.
2. Thứ sáng tác tiểu thuyết cần giữ lại không phải "bản thân lịch sử chat" mà là trí nhớ tự sự có cấu trúc.
3. Sau khi nén, Writer không được đánh mất trạng thái nhân vật, phục bút, kế hoạch chương, ràng buộc văn phong và các mục thẩm duyệt cần sửa.
4. Khi khôi phục viết, không được giả định model còn "nhớ đã chat những gì", mà phải ưu tiên dựa vào sản phẩm lưu trữ bền vững.

Vì thế chúng tôi dùng một phương án "trí nhớ phân tầng":

- Trí nhớ ngắn hạn: phần đuôi tin nhắn gần đây được giữ lại
- Trí nhớ trung hạn: `ContextSummary` do nén sinh ra
- Trí nhớ dài hạn: sản phẩm có cấu trúc trong store của dự án
- Trí nhớ khôi phục: handoff / restore pack / novel_context

## 2. Kiến trúc tổng thể

### 2.1 Các tầng chính

Quản lý ngữ cảnh hiện tại chia thành bốn tầng:

1. `agentcore/context`
   Phụ trách ngân sách ngữ cảnh tổng quát, đường ống chiến lược, khung nén/khôi phục.

2. `internal/tools/novel_context`
   Phụ trách lắp ghép dữ liệu có cấu trúc trong dự án tiểu thuyết thành ngữ cảnh dùng được cho lượt hiện tại.

3. `internal/agents/ctxpack`
   Phụ trách nén nhanh dựa trên store dành riêng cho Writer.

4. `internal/agents/ctxpack` (restore pack)
   Phụ trách nối thêm một gói khôi phục sau nén ở sau `FullSummary`, bảo đảm Writer viết tiếp được.

### 2.2 Luồng dữ liệu

Lúc chạy có chủ yếu hai đường ngữ cảnh:

1. Đường làm việc bình thường
   - Agent gọi `novel_context`
   - `novel_context` đọc từ store tóm tắt chương, kế hoạch, nhân vật, dòng thời gian v.v.
   - Dữ liệu đó vào prompt của lượt hiện tại

2. Đường ngữ cảnh quá dài
   - `ContextManager` phát hiện áp lực token
   - Nén theo thứ tự chiến lược
   - Ưu tiên thử nén nhẹ và nén dựa trên store
   - Vẫn chưa đủ mới đi tới `FullSummary` bằng LLM
   - Sau `FullSummary` thì tiêm restore pack

## 3. Các file then chốt

### 3.1 Engine ngữ cảnh tổng quát

- `../agentcore/context/strategy.go`
- `../agentcore/context/engine.go`
- `../agentcore/context/strategy_tool.go`
- `../agentcore/context/strategy_trim.go`
- `../agentcore/context/strategy_summary.go`
- `../agentcore/context/message.go`
- `../agentcore/context/summary_run.go`

Tác dụng:

- Định nghĩa `Strategy` / `ForceCompactionStrategy`
- Thực thi chuỗi chiến lược dựa trên ngân sách
- Biểu diễn `ContextSummary` và chuyển đổi sang LLM
- Nén tóm tắt `FullSummary` bằng LLM

### 3.2 Nối dây phía dự án

- `internal/agents/build.go`

Tác dụng:

- Lắp ghép `ContextManager` của Writer (Coordinator đã về hưu từ 2026-07-12, xem docs/engine-arbiter.md)
- Tiêm `StoreSummaryCompact` bổ sung cho Writer
- Cấu hình prompt `FullSummary` tùy chỉnh cho tiểu thuyết
- Cấu hình `WriterRestorePack` cho Writer

### 3.3 Nén và khôi phục phía dự án

- `internal/agents/ctxpack/strategy.go`
- `internal/agents/ctxpack/builder.go`
- `internal/agents/ctxpack/restore.go`

Tác dụng:

- Trước khi tóm tắt bằng LLM, ưu tiên dùng dữ liệu store để nén nhanh
- Lắp ghép thống nhất ngữ cảnh có cấu trúc cần cho nén và khôi phục của Writer
- Nối thêm một restore message thuần bộ nhớ sau `FullSummary`

### 3.4 Lắp ghép ngữ cảnh có cấu trúc

- `internal/tools/novel_context.go`
- `internal/tools/novel_context_builders.go`
- `internal/domain/runtime.go`

Tác dụng:

- Định nghĩa `ContextProfile` / `MemoryPolicy`
- Quyết định nạp bao nhiêu tóm tắt chương, bao nhiêu dòng thời gian, có bật tóm tắt phân tầng không
- Lắp ghép chương, nhân vật, phục bút, dòng thời gian, kinh nghiệm thẩm duyệt v.v. từ store

### 3.5 Bàn giao và khôi phục

- `internal/host/resume.go`
- `internal/host/engine.go`

Tác dụng:

- Ưu tiên dựa vào dữ liệu sự thật của store ở giai đoạn truyện dài/làm lại/thẩm duyệt
- Khi khôi phục thì ghép gói bàn giao có cấu trúc vào prompt

### 3.6 Khả năng quan sát

- `internal/host/observer.go`
- `internal/host/observer_events.go`
- `internal/entry/tui/panels_activity.go`

Tác dụng:

- Ghi sự kiện viết lại ngữ cảnh
- Xuất tên chiến lược, thay đổi token, lượng tin nhắn giữ lại
- Để TUI thấy ngữ cảnh hiện tại là `projected` hay `compacted`

## 4. ContextManager được lắp ghép thế nào

Writer đi qua `newContextManager` (mỗi lần spawn thì factory dựng lại theo cửa sổ model hiện tại). Trước khi Coordinator về hưu nó đi cùng factory đó, cấu hình của nó được giữ trong bảng dưới làm đối chiếu lịch sử.

Các tham số then chốt của `contextManagerConfig` hiện tại:

- `ContextWindow`
  Tổng cửa sổ ngữ cảnh của model.

- `ReserveTokens`
  Token dành trước cho đầu ra của model.

- `KeepRecentTokens`
  Ngân sách đuôi tin nhắn gần đây cố gắng giữ khi nén.

- `ToolMicrocompact`
  Cấu hình nén nhỏ kết quả công cụ.

- `ExtraStrategies`
  Chiến lược nén bổ sung phía dự án. Writer hiện dùng để treo `StoreSummaryCompact`.

- `Summary`
  Cấu hình `FullSummary`, gồm prompt tùy chỉnh và post-summary hook.

Giá trị cấu hình thực tế hiện tại:

| Tham số | Writer | Coordinator (đã về hưu, đối chiếu lịch sử) |
|------|--------|-------------|
| ReserveTokens | 16,384 | 32,000 |
| KeepRecentTokens | 20,000 | 30,000 |
| CommitOnProject | false | true |
| IdleThreshold | 5min | không có |
| ExtraStrategies | StoreSummaryCompact | không có |
| Summary Prompt tùy chỉnh | bản tự sự tiểu thuyết | mặc định (bản trợ lý mã) |

Ngưỡng kích hoạt nén = `ContextWindow - ReserveTokens`. Ví dụ cửa sổ 128K thì Writer kích hoạt ở ~112K.

Thứ tự đường ống chiến lược của Writer hiện tại là:

1. `ToolResultMicrocompact`
2. `LightTrim`
3. `StoreSummaryCompact`
4. `FullSummary`

Thứ tự này có ý nghĩa rõ ràng:

- Trước tiên dọn nhiễu công cụ bằng cách rẻ nhất
- Rồi cắt các khối văn bản quá dài
- Nếu dữ liệu store đủ thì nén có cấu trúc với zero LLM luôn
- Cuối cùng mới lùi về tóm tắt bằng LLM

## 5. Tác dụng của từng chiến lược

### 5.1 ToolResultMicrocompact

Vị trí hiện thực:

- `../agentcore/context/strategy_tool.go`

Tác dụng:

- Dọn `tool_result` trong lịch sử
- Thay kết quả công cụ cũ bằng văn bản giữ chỗ ngắn

Giá trị:

- Nội dung công cụ trả về thường cồng kềnh, mật độ thông tin thấp
- Nhiều kết quả công cụ cũ chỉ là "nhiễu quá trình", không phải trí nhớ tiểu thuyết

Đặc điểm cấu hình hiện tại của Writer:

- Đặt `IdleThreshold = 5m`

Nghĩa là:

- Nếu tin nhắn assistant gần nhất đã rảnh quá ngưỡng
- Sẽ giảm số kết quả công cụ cũ được giữ lại một cách quyết liệt hơn

Tình huống áp dụng:

- Nhiều lượt `novel_context`
- Sau nhiều lượt công cụ read / check / draft

### 5.2 LightTrim

Vị trí hiện thực:

- `../agentcore/context/strategy_trim.go`

Tác dụng:

- Cắt cụt các khối văn bản rất dài
- Giữ phần đầu và phần đuôi, ở giữa thay bằng chỗ giữ chỗ

Giá trị:

- Giữ nguyên cấu trúc tin nhắn
- Chi phí thấp
- Rất phù hợp xử lý nguyên văn chương quá dài hay đầu ra dài

Tình huống áp dụng:

- Một tin nhắn quá dài, nhưng chưa cần tóm tắt cả đoạn lịch sử

### 5.3 StoreSummaryCompact

Vị trí hiện thực:

- `internal/agents/ctxpack/strategy.go`
- `internal/agents/ctxpack/builder.go`

Tác dụng:

- Khi ngữ cảnh của Writer quá dài
- Ưu tiên dùng trí nhớ có cấu trúc trong store lưu trữ bền vững để thay tin nhắn cũ
- Không gọi LLM

Nó không phải tóm tắt hội thoại mà là "thay thế bằng trí nhớ có cấu trúc".

Dữ liệu cốt lõi được giữ lại hiện tại gồm:

- Tiến độ hiện tại
- Tóm tắt chương gần đây
- Kế hoạch chương hiện tại
- Đại cương chương hiện tại
- Tóm tắt cung hiện tại
- Tóm tắt tập hiện tại
- Ảnh chụp nhân vật
- Phục bút đang hoạt động
- Vấn đề thẩm duyệt chưa sửa
- Dòng thời gian gần đây
- Quy tắc văn phong

Điều kiện tiên quyết để kích hoạt:

- Chương hiện tại lớn hơn 1
- Trong store đã có đủ tóm tắt lịch sử
- Và chương hiện tại ít nhất có dữ liệu trạng thái làm việc
  - `chapter_plan` hoặc `current_outline`

Giá trị:

- Giảm số lần nén bằng LLM
- Tránh thông tin then chốt của tiểu thuyết trôi dạt khi tóm tắt
- Để trí nhớ dài hạn ưu tiên dựa vào sự thật trên đĩa thay vì lịch sử chat

Vì sao chỉ dùng cho Writer:

- Đây là chiến lược nghiệp vụ tiểu thuyết, không phải chiến lược khung tổng quát
- Chế độ ngữ cảnh của Editor / Architect khác (nhiệm vụ một lần, áp lực cửa sổ nhỏ)
- Hợp lý nhất là nghiệm thu trước trên Writer, nơi cần trí nhớ sáng tác liên tục nhất

### 5.4 FullSummary

Vị trí hiện thực:

- `../agentcore/context/strategy_summary.go`
- `../agentcore/context/summary_run.go`

Tác dụng:

- Khi mấy tầng trên vẫn chưa đủ, dùng model sinh `ContextSummary`
- Giữ đuôi tin nhắn gần đây
- Biến ngữ cảnh cũ hơn thành checkpoint có cấu trúc

Chỗ Writer khác trợ lý mã mặc định:

- Writer dùng prompt tóm tắt tùy chỉnh
- Nội dung tóm tắt yêu cầu rõ phải giữ:
  - Tiến độ hiện tại
  - Trạng thái tức thời của nhân vật
  - Phục bút và manh mối đang hoạt động
  - Phản hồi thẩm duyệt và vấn đề cần sửa
  - Văn phong và nhịp điệu
  - Quyết định then chốt
  - Bước tiếp theo
  - Ngữ cảnh then chốt

Giá trị:

- Là chiến lược dự phòng cuối cùng
- Kể cả khi dữ liệu store không đủ, vẫn duy trì được tính liên tục nhờ LLM

### 5.5 Cầu dao (Circuit Breaker)

Vị trí hiện thực:

- `../agentcore/context/engine.go`

Tác dụng:

- Khi nén thất bại liên tiếp đạt ngưỡng (mặc định 3 lần), bỏ qua việc nén lượt hiện tại
- Khi bỏ qua vẫn phát `RewriteEvent` (`Reason = "circuit_breaker"`)
- TUI hiển thị scope là "bỏ qua do cầu dao"
- Dùng chế độ nửa mở: sau khi bỏ qua một lượt, lần sau sẽ thử lại, thành công thì phục hồi, thất bại lại thì lại bỏ qua

Vì sao cần:

- Tóm tắt bằng LLM có thể thất bại liên tiếp vì mạng, model từ chối v.v.
- Không có cầu dao thì mỗi lượt Project đều thử và thất bại, lãng phí lệnh gọi API
- Trong phiên viết truyện dài sự lãng phí đó sẽ tích lũy

Chẩn đoán:

- Nếu TUI liên tục hiển thị "bỏ qua do cầu dao", nghĩa là đường tóm tắt bằng LLM có vấn đề
- Kiểm tra sự kiện viết lại ngữ cảnh có `reason=circuit_breaker` trong slog
- Cầu dao không ảnh hưởng `StoreSummaryCompact` (nó không gọi LLM)

### 5.6 Ước lượng token (nhận biết CJK)

Vị trí hiện thực:

- `../agentcore/context/usage.go`

Tác dụng:

- Mọi điều khiển ngân sách và thời điểm kích hoạt nén đều dựa vào ước lượng token
- `estimateTextTokens` tự phát hiện văn bản có chủ yếu là ký tự CJK không
- Văn bản chủ yếu CJK: `runes × 1.5`
- Văn bản chủ yếu ASCII: `bytes / 4`

Vì sao không dùng `bytes/4` chuẩn:

- Một chữ Hán UTF-8 = 3 bytes
- `bytes/4` sẽ ước một chữ Hán là 0.75 token, thực tế khoảng 1.5 token
- Ước thấp gấp 2 lần khiến việc kích hoạt nén chậm đi nghiêm trọng

Phạm vi ảnh hưởng:

- `EstimateTokens` (một tin nhắn)
- `EstimateTotal` (danh sách tin nhắn)
- `EstimateContextTokens` (ước lượng hỗn hợp: Usage do LLM báo + ước lượng đuôi tin nhắn)
- Việc cắt theo ngân sách trong `builder.go`

Lưu ý: args của ToolCall là JSON (chủ yếu ASCII), vẫn dùng `bytes/4`, không bị điều chỉnh CJK ảnh hưởng.

## 6. Vì sao Writer có hai bộ "trí nhớ sau nén"

Writer hiện tại có hai đường trông gần giống nhau nhưng trách nhiệm khác nhau:

### 6.1 StoreSummaryCompact

Trách nhiệm:

- Thay thẳng tin nhắn cũ trong quá trình nén

Đặc điểm:

- Xảy ra trước `FullSummary`
- Zero LLM
- Dùng store thay lịch sử cũ hơn

### 6.2 WriterRestorePack

Vị trí hiện thực:

- `internal/agents/ctxpack/restore.go`

Trách nhiệm:

- Nối thêm một restore message sau `FullSummary`

Đặc điểm:

- Xảy ra sau khi nén bằng LLM
- Tiêm qua `PostSummaryHook`
- Dùng để bổ sung thông tin có cấu trúc mà Writer bắt buộc phải thấy để viết tiếp

Vì sao cần cả hai:

- `StoreSummaryCompact` không phải lúc nào cũng trúng
  - ví dụ chương 1 hoặc khi dữ liệu store chưa đủ
- `FullSummary` dù làm tốt đến đâu vẫn có thể bỏ sót thông tin chính xác trong store
- Nên restore pack là lớp bảo hiểm cuối cùng

Hiện hai thứ này đã dùng chung `builder.go`, tránh trôi dạt khẩu độ.

## 7. Tác dụng của novel_context

Vị trí hiện thực:

- `internal/tools/novel_context.go`
- `internal/tools/novel_context_builders.go`

`novel_context` không phải chiến lược nén, nó là "bộ lắp ghép ngữ cảnh có cấu trúc" lúc chạy.

Nó chia dữ liệu trong store thành mấy loại:

- `working_memory`
  - Kế hoạch chương hiện tại
  - Đại cương chương hiện tại
  - Tóm tắt chương gần đây
  - Dòng thời gian
  - checkpoint
  - previous tail

- `episodic_memory`
  - Trạng thái nhân vật
  - Trạng thái quan hệ
  - Thay đổi trạng thái gần đây
  - Phục bút

- `reference_pack`
  - Thiết lập và dữ liệu tham chiếu ổn định hơn

- `selected_memory`
  - Lượng nhỏ ký ức quan trọng được chọn theo nhiệm vụ hiện tại

Giá trị:

- Nó quyết định ngữ cảnh tiểu thuyết có cấu trúc thật sự "được đưa cho model" mỗi lượt
- `StoreSummaryCompact` không gọi chính nó, nhưng dùng chung nguồn dữ liệu và cách lắp ghép tương tự

## 8. ContextProfile và MemoryPolicy

Vị trí hiện thực:

- `internal/domain/runtime.go`

### 8.1 ContextProfile

Tác dụng:

- Quyết định kích thước cửa sổ nạp theo tổng số chương

Quy tắc hiện tại:

- `<= 15` chương
  - tóm tắt `10` chương gần đây
  - dòng thời gian `10` chương gần đây

- `<= 50` chương
  - tóm tắt `5` chương gần đây
  - dòng thời gian `8` chương gần đây

- `> 50` chương
  - tóm tắt `3` chương gần đây
  - dòng thời gian `5` chương gần đây
  - bật tóm tắt phân tầng

Giá trị:

- Kiểm soát quy mô ngữ cảnh
- Tránh nhồi toàn bộ lịch sử vào prompt khi truyện dài

### 8.2 MemoryPolicy

Tác dụng:

- Viết rõ chiến lược dùng ngữ cảnh hiện tại
- Cho `novel_context` xuất ra
- Cho logic handoff / reminder / chẩn đoán dùng

Trường then chốt:

- `SummaryWindow`
- `TimelineWindow`
- `LayeredSummaries`
- `SummaryStrategy`
- `HandoffPreferred`
- `ReadOnlyThreshold`

Giá trị:

- Biến "hệ thống hiện tại nên dùng ký ức thế nào" từ logic ngầm thành chiến lược lúc chạy tường minh

## 9. Tác dụng của handoff

Vị trí hiện thực:

- `internal/host/resume.go`

Khi tác phẩm bước vào giai đoạn dài hơn, phức tạp hơn, phụ thuộc sản phẩm có cấu trúc hơn, hệ thống sẽ nghiêng về handoff.

Gói handoff ghi lại:

- Giai đoạn và flow hiện tại
- Vị trí chương kế tiếp
- Commit gần nhất
- Thẩm duyệt gần nhất
- Tóm tắt gần nhất
- Memory policy hiện tại
- Câu hướng dẫn khôi phục

Giá trị:

- Khôi phục sau gián đoạn không dựa vào lịch sử chat
- Trong làm lại/thẩm duyệt/truyện dài thì ưu tiên dựa vào sản phẩm có cấu trúc

## 10. Khả năng quan sát và chẩn đoán

### 10.1 Sự kiện viết lại ngữ cảnh

Vị trí hiện thực:

- `internal/host/observer.go`

Mỗi lần viết lại ngữ cảnh đều xuất qua callback:

- `reason`
- `strategy`
- `committed`
- `tokens_before`
- `tokens_after`
- `messages_before`
- `messages_after`
- `compacted_count`
- `kept_count`
- `split_turn`
- `incremental`
- `summary_runes`
- `duration_ms`

Việc này đồng thời vào:

- `slog`
- hàng đợi ranh giới runtime
- sự kiện `COMPACT` của TUI

### 10.2 TUI thấy được gì

TUI hiển thị:

- token ngữ cảnh hiện tại (kèm màu chuyển sắc theo sức khỏe)
- context window
- scope ngữ cảnh hiện tại (gồm "bỏ qua do cầu dao")
- tên chiến lược cuối cùng
- số lượng summary

Ý nghĩa màu của phần trăm ngữ cảnh (hiện thực ở `internal/entry/tui/layout.go`):

| Màu | Điều kiện | Ý nghĩa |
|------|------|------|
| Xanh | < 70% | Thoải mái, xa ngưỡng nén |
| Vàng | 70-85% | Gần ngưỡng nén |
| Đỏ | > 85% | Sắp hoặc đang nén |

Nhãn scope:

| Scope | Hiển thị | Ý nghĩa |
|-------|------|------|
| baseline | đường cơ sở | Trạng thái bình thường |
| projected | chiếu trước | Bản xem trước nén tạm thời |
| compacted | đã commit | Nén đã có hiệu lực |
| recovered | khôi phục | Khôi phục sau tràn |
| skipped | bỏ qua do cầu dao | Nén bị cầu dao bỏ qua |

Giá trị:

- Phán đoán nhanh sức khỏe ngữ cảnh hiện tại
- Vàng/đỏ thì có thể dự đoán sắp nén
- Thấy "bỏ qua do cầu dao" nghĩa là đường tóm tắt bằng LLM có vấn đề

### 10.3 Có sự cố thì xem ở đâu trước

#### Kịch bản 1: sau khi nén, Writer mất kế hoạch chương

Xem trước:

- `novel_context` có tiêm `chapter_plan` ổn định không
- `builder.go` có lấy được `chapterPlan` không
- `WriterRestorePack` có được làm mới không

File trọng điểm:

- `internal/tools/novel_context_builders.go`
- `internal/agents/ctxpack/builder.go`
- `internal/store/session.go`

#### Kịch bản 2: sau khi nén mất trạng thái nhân vật/phục bút

Xem trước:

- `LoadLatestSnapshots`
- `LoadActiveForeshadow`
- `builder.go`
- Prompt tóm tắt của Writer có bị ghi đè không

#### Kịch bản 3: nén thường xuyên nhưng mãi không trúng store_summary

Xem trước:

- Chương hiện tại có đang `<= 1` không
- Đã có recent summaries / arc / volume summary chưa
- Có tồn tại `chapter_plan` hay `current_outline` không
- Chiến lược cuối cùng được ghi cho `writer.Context.Strategy` có phải `full_summary` không

#### Kịch bản 4: sau khôi phục ngữ cảnh không đủ

Xem trước:

- handoff có được sinh không
- restore pack có được làm mới không
- prompt khôi phục có tiêm handoff không

#### Kịch bản 5: kết quả công cụ quá nhiều khiến ngữ cảnh phình to

Xem trước:

- `ToolResultMicrocompact` có trúng không
- `IdleThreshold` có hiệu lực không

## 11. Đánh đổi của bản triển khai hiện tại

### Hướng đã dứt khoát giữ

1. Không nhồi logic nghiệp vụ tiểu thuyết vào `agentcore`
2. Ưu tiên dựa vào store có cấu trúc thay vì lịch sử chat
3. Writer dùng prompt tóm tắt chuyên cho tiểu thuyết
4. Nén và khôi phục cố gắng dùng chung builder, tránh trôi dạt khẩu độ

### Giới hạn cố ý giữ lại hiện tại

1. `StoreSummaryCompact` chỉ dùng cho Writer
2. Chương đầu sẽ không trúng store-based compact
3. Khi dữ liệu store không đủ vẫn lùi về `FullSummary`
4. `WriterRestorePack` là bù đắp dạng nối thêm, không thay thế `FullSummary`

Những giới hạn này không phải khuyết điểm, mà là biên giới đặt ra ở giai đoạn hiện tại để kiểm soát độ phức tạp.

## 12. Tóm gọn một câu

Quản lý ngữ cảnh của dự án này không đơn giản là "nén hội thoại dài thành ngắn", mà là:

`Ưu tiên dùng trí nhớ tiểu thuyết có cấu trúc để duy trì tính liên tục, chỉ để LLM tóm tắt hội thoại khi cần thiết; và ở cả ba khâu nén, khôi phục, bàn giao đều cố gắng dựa vào cùng một bộ sản phẩm lưu trữ bền vững.`

Nếu sau này bạn sửa hệ thống này, ưu tiên giữ ba điều dưới đây:

1. Đừng để ký ức then chốt của Writer lại chỉ dựa vào lịch sử chat.
2. Đừng để `store_summary` và `writer_restore` trôi dạt khẩu độ.
3. Khi có vấn đề liên tục, trước tiên kiểm tra sản phẩm có cấu trúc đã vào ngữ cảnh chưa, rồi mới quyết định có sửa prompt không.