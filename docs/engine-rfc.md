# RFC Bước 2: Engine chạy Worker trực tiếp (đáp án bảy câu hỏi bắt buộc)

> Trạng thái: chốt (2026-07-12). Dựa trên khảo sát mã của host/observer/subagent/usage/cocreate.
> Kết luận: cả bảy câu đều có đáp án rủi ro thấp, chuyển sang triển khai. Liên quan: docs/engine-arbiter.md.

## 1. Mặt thực thi của Worker: gọi subagent.Runner theo kiểu lập trình

> Hậu ký (2026-07-22): agentcore đã tách việc thực thi có kiểu khỏi giao thức công cụ của model. `Runner.Run` là
> cửa vào của host; `Runner.AsTool()` chỉ dành cho host cần để LLM tự giao việc cho subagent. AINovel chỉ phụ thuộc Runner.

`Runner.Run(agent, task)` mỗi lần khởi động một `agentcore.AgentLoop` đầy đủ. Engine gọi thẳng nó, và
**toàn bộ lắp ghép trong build.go giữ nguyên hiệu lực**: model theo vai trò + failover, prompt cache key (#seq tự tăng mỗi lần),
ThinkingLevel, UsageRecorder/SessionLogger (OnMessage), Writer ContextManagerFactory, RestorePack, StopGuardFactory,
StopAfterTools. Kết quả có kiểu và chuỗi lỗi trả về trực tiếp, không qua mã hóa/giải mã JSON hay dò đoán kết quả công cụ.

**Chiếu sự kiện**: phần chuyển tiếp tiến độ của subagent đọc **callback ToolProgress trong ctx**
(`agentcore.ReportToolProgress(ctx,...)`). Engine gọi `Runner.Run` với `agentcore.WithToolProgress(ctx, relay)`, bộ chuyển tiếp
hoạt động như cũ; relay tổng hợp ProgressPayload thành `EventToolExecUpdate` đưa cho `observer.handleToolUpdate` sẵn có —
phần xử lý phía worker của observer (dòng TOOL/chính văn streaming/thinking/retry/context) **tái sử dụng ~95%**. Dòng DISPATCH
chuyển sang do Engine trực tiếp mở/kết thúc (thêm hai cửa vào cho observer). Luồng tường thuật ở cột trái của Coordinator biến mất,
thay bằng sự kiện tường thuật của Engine.

**/model và mức suy luận**: chuyển model qua ModelSet swap (configs giữ failover wrapper, cơ chế cũ); mức suy luận qua
`runner.SetThinkingLevel` (giữ applyThinking, xóa nhánh coordinator).

## 2. Vòng đời Engine

Vòng lặp tuần tự một goroutine; cancel `ctx` = tạm dừng/hủy (lan vào vòng lặp worker, checkpoint đảm bảo không mất dữ liệu);
Resume/Continue = mở vòng lặp mới. Tính tuần tự của một Worker được cấu trúc vòng lặp đảm bảo tự nhiên. Các sentinel ngân sách/điểm dừng
được Engine gọi trực tiếp ở mỗi ranh giới vòng (thay cho đăng ký sự kiện và FlowBoundaryHook).

## 3. Giao thức commit trạng thái → tính tuần tự khiến nó gần như biến mất

Engine chỉ `LoadState+Route` trước mỗi lần spawn, nên chỉ thị luôn dựa trên sự thật mới nhất — chỉ thị Route không có TOCTOU, không cần đối chiếu Expect.
Ảnh chụp Expect chỉ dùng cho **dispatch của phán định Arbiter** (giữa tư vấn và thực thi có một lần chạy worker): trước khi thực thi ở ranh giới
đối chiếu {Phase, QueueHead}, không khớp → hủy + hỏi lại bằng sự thật mới. Kiểm tra tiên quyết (trách nhiệm cũ của Gate) trở thành mã thường của Engine:
phase=complete thì không giao việc; chương mục tiêu của writer chưa mở rộng → đổi sang architect_long expand (tất định, không cần văn bản giảng giải).
Các hành động trạng thái điều khiển của can thiệp (hold/reopen/dispatch) vào hàng đợi Engine để commit ở ranh giới; answer/rules thì tức thời.

## 4. Phân loại lỗi (tất định đi trước)

- retryable (mạng/giới hạn tần suất/stream-idle): MaxRetries=7 bên trong subagent đã tiêu hóa tại chỗ, không ra khỏi vòng lặp
- worker trả error (escalate/hard_stop/lỗi cứng của công cụ): Engine thử lại cùng chỉ thị 1 lần → vẫn thất bại → Arbiter
  hỏi `worker_failure` (retry/reroute/abort) → abort hoặc chính Arbiter thất bại → tạm dừng + notify
- lỗi tất định như sai tham số/agent không xác định: tạm dừng + notify ngay (lỗi mã, thử lại vô nghĩa)

## 5. Giao thức bế tắc

Mỗi vòng ghi lại khóa chỉ thị `Agent+Task`. Sau khi vòng trước thực thi mà Route vẫn sinh cùng khóa, nghĩa là hậu điều kiện của nhiệm vụ chưa đạt, `repeat++`; chỉ thị đổi thì về 0. Các checkpoint trung gian bên trong Worker như `plan/draft/edit` không tính là tiến triển cấp Engine.
repeat==3 → Arbiter hỏi `deadlock`; Arbiter khuyên retry **không** reset về 0; repeat==5 → ngắt cứng: tạm dừng + notify.
(Thời Coordinator "không đặt ngưỡng" dựa vào tính tự chủ của nó; một Engine tất định bắt buộc phải có biên.)

## 6. Ngữ nghĩa sập nguồn → miễn phí

Không cần phán đoán "Worker trước có sinh sự thật hợp lệ hay không": checkpoint+digest ở tầng công cụ là idempotent, Route tính lại từ store,
giao việc trùng lặp vẫn an toàn. Việc thử lại luồng model của agentcore không vượt qua ranh giới thực thi công cụ. Khôi phục = vào thẳng vòng lặp. PendingSteer,
trước khi vòng lặp khởi động, đi qua Arbiter như một can thiệp.

## 7. Nghiệm thu nguyên mẫu

Bài kiểm tra tích hợp đầu-cuối (ChatModel giả): toàn tuyến quy hoạch → bổ sung → viết chương → thẩm duyệt/tóm tắt cuối cung → mở rộng → kết thúc sách; phân loại can thiệp ghi vào store;
tạm dừng/khôi phục; ngắt cầu bế tắc; ghi usage; hình dạng sự kiện observer (dòng DISPATCH/TOOL, delta streaming). Cộng với
đặc tả Route 60k, contract agentcore và bài kiểm tra luồng editor sẵn có làm lưới hồi quy.

## Tổng kết giai đoạn hoàn tất (quyết định thiết kế)

Tổng kết kết thúc sách chuyển thành **sinh tất định**: store đã có toàn bộ sự thật (tóm tắt chương/nhân vật/sổ phục bút/số chữ), Engine kết xuất báo cáo trực tiếp,
không tiêu thêm một lệnh gọi LLM nào để tạo văn bản mang tính nghi lễ. Tổng kết bằng LLM của coordinator cũ bị bỏ (engine-arbiter.md §3: tổng kết không phải phán định).