# Tiến hóa mặt điều khiển: Engine + Arbiter (bỏ vòng lặp dài của Coordinator)

> Trạng thái (2026-07-14 v6): **triển khai mã hoàn tất** — Engine/Arbiter đã được hiện thực, Coordinator cùng toàn bộ phần kèm theo đã bị xóa (danh sách §10); kiểm chứng đầu-cuối viết trọn một cuốn sách, phán định thất bại, phán định bế tắc, nghiệm thu làm lại (thời điểm hold+editor), dừng ngay khi có boundary hold, bảo toàn can thiệp khi tranh chấp lúc thoát và một giấy phép một chương. Mọi mục chặn của ba vòng phản biện bên ngoài đều đã được xử lý (gồm khép kín vòng sự thật feedback, bảo vệ sập cho PendingSteer, tranh chấp lifecycle).
> **Di trú tài liệu đã hoàn tất (2026-07-12)**: phần thân architecture.md đã được viết lại toàn bộ theo kiến trúc hiện hành Engine+Arbiter (gồm chiến lược kiểm chứng/thư mục/kỷ luật mới); phần tường thuật kiến trúc cũ trong README, context-management, evaluation-system, observability, user-rules-runtime đã được dọn (chỉ giữ đối chiếu lịch sử có đánh dấu). Đường cấu hình và tương thích phiên của Coordinator đều đã bị xóa; Arbiter hiện cố ý dùng thống nhất model Default, không mở cấu hình vai trò riêng.
> **Làm rõ ngữ nghĩa thiết kế (vòng phản biện bốn/năm)**: ① điểm tiêu thụ writer feedback **chính là** thao tác cấu trúc kế tiếp (expand_arc/append_volume/update_compass làm rỗng feedback sau khi tham chiếu qua novel_context) — nó là "đề xuất cho đại cương phía sau" (nguyên văn trong schema commit), không phải tín hiệu điều phối tức thời; lệch nghiêm trọng giữa cung đi qua kênh thẩm duyệt của editor và can thiệp người dùng. Sách không phân tầng thì không có thao tác cấu trúc, **commit không ghi feedback của nó xuống đĩa** (tránh sự thật rác mãi mãi không có người tiêu thụ; bản phản chiếu trong giá trị trả về vẫn giữ để chẩn đoán). ② rule_violations đã khép kín: commit ghi xuống đĩa ở hai đường (**metadata chất lượng best-effort**, không mạnh-đồng-bộ ngang hàng với commit chương — sập đúng lúc sau khi xóa pending_commit sẽ thiếu một bản ghi, chấp nhận được) → novel_context(chapter=N) tiêm vào → editor tiêu thụ theo ánh xạ §kiểm tra cơ học. ③ Bảo vệ sập cho PendingSteer là **lưu trữ bền vững best-effort cho một can thiệp đang trên đường**: lần lưu đầu thất bại sẽ dừng phán định rõ ràng; trong lúc phán định, áp dụng hành động thất bại, thoát bình thường/Abort đều được bảo vệ; hai cửa sổ dứt khoát không bảo đảm là (a) sau khi việc giao việc chuyển vào hàng đợi thực thi trong bộ nhớ (e.next) và trước khi worker khởi động mà tiến trình bị kill cứng (cửa sổ cỡ mili giây, defer không chạy); (b) can thiệp đồng thời đang chờ interMu (chưa ghi vào ô). Người dùng có mặt thì cảm nhận được, gửi lại chỉ tốn vài giây, không dựng intent/FIFO bền vững cho việc này. ④ Phán định khởi động thất bại không phải bế tắc (bổ túc từ sự cố thật 2026-07-12: tài khoản provider mất hiệu lực khiến plan_start thất bại, mọi đường khôi phục đều tắc): StartPrompt (sự thật đầu vào) được chuyển sang ghi xuống đĩa **trước** khi phán định; khi plan_start chưa từng hoàn tất, engine planStartFallback dựa vào nó để quyết tại chỗ…
> Tài liệu này giữ lại làm bản ghi quyết định thiết kế; kiến trúc hiện hành xem mục kiến trúc trong README và docs/engine-rfc.md. Liên quan: docs/voice-layer.md (đã triển khai).

## 1. Động cơ: giả định lỗi thời bị vây quanh bởi bản vá

Giả định nền tảng của dự án — "một Prompt, một vòng lặp LLM dài thường trú dẫn dắt cả cuốn sách" — đã lỗi thời. Sau cuộc tái cấu trúc Hybrid tháng 4, quyền quyết định thực tế nằm trong tay `flow.Route`, còn 90% lệnh gọi của Coordinator trong vòng lặp chính chỉ làm **chuyển tiếp nguyên dạng**. Cái hệ sinh thái bản vá phải trả để "duy trì một phiên LLM lẽ ra không nên dừng":

1. Kỹ thuật văn bản động StopGuard + blockMessage
2. Giao thức chỉ thị lặp của Dispatcher ("lần thứ N ra lệnh")
3. Răn dạy hành vi trong coordinator.md (đặc lệ ca khôi phục / truy vấn phải giao việc cùng lượt / không được dùng ngừng máy để bày tỏ lập trường)
4. completePhaseGate / writerExpandedChapterGate
5. MaxTurns=100_000
6. FlowBoundaryHook
7. Đặc lệ bất đồng kết thúc

**Mọi hệ thống con mới bên trong dự án (import/simulation/cocreate/userrules) đều không đi qua Coordinator, đều đang dùng mô hình "Host trực tiếp điều phối + LLM như một hàm"** — phương án này đưa luồng chính về đúng mô hình đã được tự chứng minh đó.

## 2. Hình thái mục tiêu

```
Entry
  ↓
Host (giữ tên package; bên trong thêm EngineLoop, không đổi tên thuần cơ học)
  ├─ đọc Store → flow.Route → chạy Worker trực tiếp
  ├─ tình huống ngữ nghĩa rõ ràng → gọi hàm Arbiter
  └─ chiếu sự kiện / ngân sách / điểm dừng / thông báo (giữ nguyên trách nhiệm hiện có)
  ↓
Workers (architect / writer / editor, tự chủ bên trong, giữ guard checkpoint-delta)
  ↓
Tools → Store (nguồn sự thật duy nhất)
```

Trách nhiệm: **Route quản mọi bước tiếp theo tra bảng được; Arbiter quản các phán đoán ngữ nghĩa có biên rõ ràng; Worker quản sáng tác mở; Engine thi hành quyết định, không tham gia phán đoán văn học; Observer/Diag chỉ quan sát.**

Một câu tóm trạng thái cuối: **một Engine tất định tuần tự, ba Worker tự chủ, một vài hàm Arbiter gọi theo nhu cầu, một tầng sự thật trên hệ thống file.**

### Hai mặt phẳng đối xứng (dự kiến ghi vào architecture.md như thiết luật mới)

```
mặt tất định:  flow.LoadState   → flow.Route     → Instruction   (kiểm tra bằng đặc tả vét cạn)
mặt ngữ nghĩa: arbiter.Collect* → arbiter.Decide* → XxxDecision   (quyết định ghi xuống đĩa + hồi quy eval)
              └── thu thập sự thật (IO) ──┘└── lõi quyết định (phát lại offline được) ──┘└── Engine thực thi ──┘
```

## 3. Các tình huống Arbiter (tập cuối, giữ tối thiểu)

| Tình huống | Kích hoạt | Ghi chú |
|------|------|------|
| `plan_start` | sách mới khởi động | Chọn kiến trúc sư short/long + mở rộng nhu cầu quá ngắn |
| `intervention` | người dùng can thiệp | Truy vấn / quy tắc dài hạn / điều chỉnh cấu trúc tình tiết / làm lại phần đã viết / làm lại sau khi kết thúc sách hoặc từ chối |
| `worker_failure` | Worker báo lỗi **và phân loại tất định không có lối ra** | Mạng/tham số/thiếu sản phẩm tiên quyết do mã tất định phân loại trước, không gửi cho Arbiter |
| `deadlock` | sau vòng trước vẫn sinh cùng một chỉ thị định tuyến | Ngữ nghĩa đếm và kết thúc xem §8 câu hỏi bắt buộc 5 |
| `completion_dispute` | **dự bị, có bằng chứng mới thêm** | Phán định kết thúc cuối tập đã do Route giao architect (nhánh 10) đảm nhiệm; chỉ bất đồng giữa chừng kiểu "cấu trúc chưa tới ranh giới nhưng truyện nên khép lại" mới cần, tỷ lệ xảy ra thật chưa biết, không dựng trước |

Tổng kết kết thúc sách không phải phán định mà là nhiệm vụ sinh nội dung — do Engine giao thẳng editor hoặc một lệnh gọi LLM thường, không chiếm tình huống Arbiter.

## 4. Thiết kế Arbiter

### 4.1 Kiểu Decision theo từng tình huống (sửa đổi v3: bỏ cấu trúc vạn năng)

```go
// Kiểu con dùng chung, chống trôi dạt giữa các tình huống
type DispatchDecision struct {
    Instruction flow.Instruction
    Expect      DispatchExpect // xem §5
}

type PlanStartDecision struct {
    Planner string // architect_long | architect_short
    Task    string // gồm nhu cầu đã mở rộng
    Reason  string
}

type InterventionDecision struct {
    Answer   string
    Rules    string
    Hold     *AdvanceHoldOp
    Reopen   *ReopenOp
    Dispatch *DispatchDecision
    Reason   string
}

type FailureDecision struct {
    Action   string // retry | reroute | abort
    Dispatch *DispatchDecision
    Reason   string
}
```

Bản ghi tiến hóa: danh sách hành động (kiểm thứ tự thừa, mảng đa hình dễ sai) → cấu trúc phẳng vạn năng (thứ tự bất hợp pháp không biểu diễn được, nhưng tổ hợp bất hợp pháp phải kiểm bằng ma trận tình huống × hành động) → **kiểu theo tình huống (hành động không khớp tình huống không biểu diễn được, phép kiểm ma trận biến mất, schema một tình huống nhỏ hơn, đầu ra LLM ổn định hơn, eval đánh giá được theo từng tình huống)**. Validate thu lại thành kiểm chứng sự thật theo từng kiểu (ràng buộc phase v.v.).

### 4.2 API: mỗi tình huống một cặp hàm tường minh

```go
func CollectInterventionFacts(st *store.Store) InterventionFacts        // ranh giới IO, cùng kỷ luật với flow.LoadState
func DecideIntervention(ctx, model, facts, text) (InterventionDecision, error) // không IO ngoài yêu cầu model do bộ thực thi chung quản, phát lại offline được
// các tình huống còn lại một cặp cùng hình; Collect/Decide hình dạng thống nhất, không dựng khung Question/Decision chung
```

- **Đường thất bại**: bộ thực thi có cấu trúc chung chọn JSON Schema gốc hay contract prompt theo năng lực model; lỗi định dạng/Schema ở chế độ prompt và lỗi kiểm nghiệp vụ ở cả hai chế độ đều mang lý do chính xác để giao model sửa, vòng đời chỉ do `context` điều khiển. Vi phạm contract gốc, từ chối trả lời, cắt cụt, kết thúc lỗi và lỗi yêu cầu không thử lại được đều trả về tường minh ngay; can thiệp không tạo ghi nào, khởi động báo lỗi tường minh, failure/deadlock tạm dừng thận trọng
- **Trí nhớ can thiệp**: decisions.jsonl kiêm luôn lịch sử can thiệp, `CollectInterventionFacts` đưa vào tóm tắt N phán định gần nhất
- **Model**: Arbiter dùng thống nhất Default, không lộ role riêng; chỉ mở rộng contract cấu hình khi xuất hiện nhu cầu rõ ràng về năng lực hoặc chi phí

### 4.3 Audit (nhỏ và ổn định; audit ≠ nguồn khôi phục)

```json
{"schema_version":1,"id":"...","kind":"intervention","checkpoint_seq":123,
 "input":"...","facts":{...},"decision":{...},"reason":"...","duration_ms":1200}
```

(token/chi phí không nằm trong bản ghi — model phán định đi qua wrapper usageTrackedModel, lượng dùng vào thống nhất UsageTracker/ngân sách, cùng một sổ với các Worker.)

- facts chỉ lưu sự thật có cấu trúc + tóm tắt + tham chiếu artifact/checkpoint, **không sao chép chính văn, không lưu gói ngữ cảnh đầy đủ**; có giới hạn kích thước một bản ghi, vượt thì cắt bớt và đánh dấu
- **input được giữ trong bản ghi** (phát lại offline `Decide*(facts, input)` là bắt buộc — audit không có input thì không hồi quy được); việc khử nhạy cảm xảy ra ở **biên xuất diag**, không xảy ra lúc ghi xuống đĩa
- Log audit không phải event sourcing, cũng không phải nguồn dữ liệu khôi phục

## 5. Giao thức commit trạng thái (vòng lặp Engine tuần tự)

```
đọc sự thật → Route / Arbiter sinh quyết định → đối chiếu tiền điều kiện → thực thi hành động
       → Worker chạy → tính lại hậu điều kiện của Route → vòng sau
```

- **Bất biến: trạng thái điều khiển chỉ thay đổi tuần tự ở ranh giới Engine.** Can thiệp có thể được tư vấn song song trong lúc Worker chạy (chỉ đọc nên an toàn, người dùng thấy Answer/Reason hiển thị trong vài giây), nhưng **hành động đổi trạng thái điều khiển (hold/reopen/dispatch) vào hàng đợi Engine, commit sau khi đối chiếu ở ranh giới**; answer (không trạng thái) và rules (mặt nội dung, chương này theo quy tắc cũ, chương sau có hiệu lực tức là ngữ nghĩa) thực thi tức thời
- Mỗi Dispatch mang ảnh chụp tại thời điểm Collect, đối chiếu ở ranh giới, không khớp → hủy, ghi `decision_stale`, hỏi lại bằng sự thật mới:

```go
type DispatchExpect struct {
    CheckpointSeq int64
    Phase         domain.Phase
    Flow          domain.FlowState
    QueueHead     int
}
```

- Tiền điều kiện tường minh tốt hơn hash toàn Store (dễ đọc, dễ chẩn đoán); không làm digest toàn cục

## 6. Mô hình khôi phục (chỉ khôi phục sự thật, không khôi phục phiên)

```
khởi động → đọc Progress → đọc Checkpoint mới nhất → tra PendingSteer/AdvanceHold/giấy phép chương → Gate đối chiếu → Route → chạy tiếp Worker
```

Khôi phục plan_start dựa vào một sự thật lưu trữ bền vững duy nhất (trong RunMeta), **phán định ghi sự thật trước, khởi động thực thi sau**:

```go
type PlanStartRecord struct {
    RawPrompt   string
    Planner     string
    PlannerTask string
    DecisionID  string // liên kết bản ghi audit
    Status      string // decided | dispatched | done —— hiện thực hóa trạng thái trung gian của giao dịch khởi động
}
```

Sập ở bất kỳ điểm nào: Record tồn tại thì đi tiếp theo Status, không tư vấn lại; Record thiếu thì coi như sách mới hỏi lại (hỏi lại chấp nhận được, audit giữ hai bản ghi).

## 7. Lộ trình di trú (xếp lại v3: Engine đi trước, Arbiter nối sau)

Căn cứ xếp lại thứ tự: phương án cũ "Arbiter đi trước" cần một bộ đường ống quá độ (phán định ngụy trang thành chỉ thị Host qua steering để đưa cho Coordinator); **Engine lên trước thì toàn bộ đường ống đó không phải dựng**, Arbiter nối thẳng vào bộ thực thi của Engine. Mỗi bước đều xóa bớt thứ gì đó, không dựng cầu tạm; lo ngại "quyền chọn hai bộ não" bị thứ tự này hóa giải về mặt cấu trúc.

| # | Bước | Trạng thái |
|---|------|------|
| 0 | Hạng mục vô điều kiện: đưa bổ sung quy hoạch vào Router (đặc tả vét cạn đi trước); audit decisions.jsonl. Cải tiến hiện thực: danh tính kiến trúc sư suy từ `RunMeta.PlanningTier` sẵn có, không cần thêm cơ chế bản ghi | ✅ 2026-07-12 |
| 1 | Giao tầng văn phong (docs/voice-layer.md) | ✅ 2026-07-12 |
| 2 | Chốt RFC Bước 2 (docs/engine-rfc.md, bảy câu hỏi bắt buộc) | ✅ 2026-07-12 |
| 3 | WorkerRunner: gọi thẳng subagent.Runner theo kiểu lập trình, sự kiện qua chuyển tiếp ctx ToolProgress | ✅ 2026-07-22 |
| 4-5 | Engine tiếp quản toàn bộ việc giao việc + nối bốn tình huống Arbiter (plan_start/intervention/failure/deadlock), nối thẳng bộ thực thi của Engine (khi triển khai phát hiện Engine đi trước khiến toàn bộ đường ống quá độ steering không phải dựng, 4/5 gộp lại) | ✅ 2026-07-12 |
| 6 | Xóa Coordinator và toàn bộ phần kèm theo (thực thi hết danh sách §10); kiểm tra tích hợp đầu-cuối (viết trọn sách bằng công cụ thật / phán định thất bại / phán định bế tắc) | ✅ 2026-07-12 |

## 8. Câu hỏi bắt buộc của RFC Bước 2 (chưa chốt thì không vào bước 3)

1. **Mặt trích xuất Worker**: API WorkerRunner; quyền sở hữu và vòng đời của toàn bộ linh kiện lắp ghép trong build.go — model theo vai trò/failover, prompt cache key, ThinkingLevel, UsageRecorder, SessionLogger, Writer ContextManagerFactory, RestorePack, StopGuardFactory, StopAfterTools, chiếu sự kiện lồng của Observer
2. **Vòng đời Engine**: khởi động/tạm dừng/hủy/khôi phục; bảo đảm tuần tự cho một Worker; chuyển đổi lúc chạy của /model và thinking
3. **Hoàn thiện giao thức commit trạng thái**: đối chiếu Expect ở §5 cho mọi tình huống; danh sách tiền điều kiện của Engine sau khi tháo Gate
4. **Phân loại học lỗi**: phân loại tất định (retry/reroute/terminal) đi trước, chỉ gửi `worker_failure` khi không có lối ra; phân tầng với việc thử lại ở tầng agentcore
5. **Giao thức bế tắc**: cùng một `Agent+Task` xuất hiện lặp liên tiếp nghĩa là hậu điều kiện định tuyến chưa thoả; checkpoint trung gian bên trong Worker không reset về 0; Arbiter quyết retry cũng không reset; 3 lần tư vấn, 5 lần ngắt cứng.
6. **Ngữ nghĩa sập**: làm sao xác định Worker trước đã sinh sự thật hợp lệ chưa
7. **Nghiệm thu nguyên mẫu**: đối chiếu từng điểm năm hạng mục Observer/Usage/Context/chuyển model/khôi phục với hiện trạng

## 9. Sổ giá trị

| Chiều | Hiện trạng | Trạng thái cuối |
|------|------|------|
| Chi phí LLM mỗi chương | mỗi ranh giới một lệnh gọi chuyển tiếp | bỏ đi; lớp vấn đề chuyển tiếp thất bại biến mất |
| Khả năng kiểm chứng phán định | ~không (lẫn trong phiên dài) | phát lại offline theo tình huống + hồi quy eval |
| Phản hồi can thiệp | chờ ranh giới chương (cấp phút) | tư vấn tức thời, trạng thái điều khiển commit ở ranh giới |
| Độ phức tạp | hệ sinh thái bảy bản vá | giảm ròng 1500+ dòng, ba lớp vấn đề về hưu |
| Khôi phục sau sập | phát lại phiên + giao thức khôi phục | đọc store chạy tiếp |
| Rủi ro giai đoạn quá độ | — | tập trung ở bước 3/4 (trích xuất Worker), do RFC + cổng nguyên mẫu kiểm soát; bước 0/1 vô điều kiện |

## 10. Danh sách xóa ở trạng thái cuối

Coordinator cùng logic khôi phục phiên của nó, Coordinator StopGuard, giao thức steering của Dispatcher và giao thức văn bản `[chỉ thị Host giao xuống]`, FlowBoundaryHook, completePhaseGate / writerExpandedChapterGate (phép kiểm chuyển thành tiền điều kiện của Engine), MaxTurns=100_000, toàn bộ răn dạy hành vi trong coordinator.md.

## 11. Bản ghi dị nghị và phản biện

1. *"Độ đúng của phán định sẽ không tăng"* — đúng; khác biệt thật là tập trung/tuyển chọn/kiểm tiền điều kiện so với trí nhớ phiên, giá trị ròng nhỉnh hơn và lần đầu đo được
2. *"Hiện trạng đang chạy được, động vào mặt điều khiển là mạo hiểm"* — thừa nhận; nền móng được dựng cho việc này, từng bước dừng được và lùi được
3. *"Kiến trúc không phải nút cổ chai, chất lượng nội dung mới là"* — đúng một phần, tầng văn phong đi trước
4. **Phản biện một (2026-07-12)**: thiếu giao thức commit trạng thái → §5; Bước 2 mỏng → câu hỏi bắt buộc §8 + cổng nguyên mẫu; thời điểm khởi động → §6; tuyên bố quá mức "trạng thái bất hợp pháp không biểu diễn được" → bản ghi tiến hóa 4.1; sai sự thật về danh sách trắng vai trò arbiter → 4.2; vệ sinh audit → 4.3
5. **Phản biện hai (2026-07-12)**: kiểu Decision theo tình huống (tiếp thu, 4.1); xếp lại thứ tự di trú Engine đi trước (tiếp thu, §7); commit ở ranh giới thống nhất (tiếp thu, §5); PlanStartRecord (tiếp thu, §6); không đổi tên host (tiếp thu); gợi ý số chữ giữ ở file giao thức (tiếp thu, xem voice-layer). **Ý kiến bảo lưu**: audit phải giữ input nếu không thì không phát lại được (4.3); completion_dispute hạ thành tình huống dự bị (§3)

## 12. Kỷ luật và những gì không làm

**Kỷ luật**: ①điểm quyết định mới phải qua phép chia ba nhánh ở §2 trước, cấm mặc định "thêm luật vào prompt"; ②mỗi điểm quyết định LLM phải có danh sách sự thật/đầu ra có cấu trúc/đường hạ cấp/audit ghi xuống đĩa; ③chỉ viết lan can sự thật, không viết lan can hành vi; ④bất biến khai báo tốt hơn script thủ tục; ⑤đổi mặt điều khiển thì sửa đặc tả vét cạn trước, sửa hiện thực sau.

**Không làm**: viết lại theo event sourcing; trừu tượng hóa Store cho đa thuê bao giả định; DSL luồng công việc tổng quát; State Digest toàn cục; đổi tên package host; dựng trước completion_dispute.