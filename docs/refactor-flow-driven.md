# Đề xuất tái cấu trúc: Hybrid Coordinator — Host định tuyến × LLM phán định

> **Tài liệu lịch sử, đã bị bỏ.** Hybrid Coordinator bị kiến trúc Engine + Arbiter thay thế từ 2026-07-12; thiết kế hiện hành xem `docs/architecture.md`, `docs/engine-rfc.md`. Tài liệu này chỉ giữ lại bản ghi tiến hóa quyết định, không được dùng làm căn cứ triển khai.
>
> Trạng thái gốc: **đã được tiếp thu và lên sóng** (2026-04-20)
> Thời điểm khảo sát: 2026-04-20
> Tài liệu hiện hành tương ứng: `docs/architecture.md` §2 / §3 / §7 / §8 / §13 đã được cập nhật đồng bộ
>
> **Tài liệu này là bản thảo thứ hai.** Vấn đề của bản thảo đầu (phương án cấp tiến xóa hoàn toàn Coordinator) xem phụ lục A, giữ mục đó để tránh đi lại đường vòng.
>
> Kết quả lên sóng:
> - `internal/host/flow/` tạo mới (router.go / state.go / dispatcher.go / router_test.go, 15 unit test nhánh đều xanh)
> - `internal/host/reminder/` xóa `flow.go` / `queue_guard.go` / `book_complete.go`; giữ StopGuard và Guard của subagent
> - `assets/prompts/coordinator.md` nén từ 88 dòng xuống ~45 dòng (thu hẹp trách nhiệm còn thực thi chỉ thị Host + phán định + chọn kiểu khởi động)
> - `internal/host/resume.go` đơn giản hóa lớn, chỉ sinh label và prompt ngắn, bước tiếp cụ thể do Router giao sau TurnEnd đầu tiên
> - `internal/store/` thêm các phương thức hỗ trợ `HasArcReview` / `HasArcSummary` / `HasVolumeSummary` / `CheckConsistency`
> - Sửa luôn bug trạng thái agent của `observer.go` dừng ở working

---

## 1. Bối cảnh

### 1.1 Định vị dự án

```
agentcore       — khung agent tổng quát
litellm         — gateway LLM tổng quát
ainovel-cli     — agent dọc về sáng tác tiểu thuyết (dự án này)
```

Không gian quyết định của agent dọc là **đóng**: sơ đồ luồng cố định, nhánh hữu hạn, dựa trên sự thật. Triết lý thiết kế của agent tổng quát ("đặt cược vào năng lực model") áp vào kịch bản dọc có mùi quá thuần túy.

### 1.2 Mục tiêu người dùng (theo ưu tiên)

1. **Ổn định** — viết liên tục không dừng, không gián đoạn vì lỗi định tuyến
2. **Ăn lợi ích từ nâng cấp LLM** — kiến trúc không chống lại năng lực model
3. **Tận dụng đầy đủ năng lực đa agent** — phân chia chức năng rõ ràng

Đề xuất này làm **cải thiện Pareto** giữa ba mục tiêu (không hy sinh mục nào để đổi mục khác).

---

## 2. Khảo sát hiện trạng

### 2.1 Phân loại điểm quyết định của Coordinator

Trích từng điểm quyết định trong `coordinator.md`:

| # | Điểm quyết định | Bản chất | Tần suất |
|---|---|---|---|
| 1 | Chọn architect_long / short lúc khởi động | Phán định (hiểu ngữ nghĩa) | 1 lần một cuốn sách |
| 2 | Mở rộng đầu vào (tự bổ sung khi <20 chữ) | Phán định (sáng tạo) | một cuốn sách 0-1 lần |
| 3 | Vòng bổ sung quy hoạch | Định tuyến (dựa sự thật) | 1-3 lần |
| 4 | Bước tiếp sau mỗi lần commit chương | **Định tuyến** | **mỗi chương 1-2 lần** |
| 5 | Thực thi từng bước thẩm duyệt cuối cung | Định tuyến | mỗi cung 3-5 lần |
| 6 | Phân nhánh verdict thẩm duyệt | Định tuyến (đã mã hóa, xem §2.3) | mỗi cung 1 lần |
| 7 | Xử lý can thiệp người dùng | Phán định (bắt buộc LLM) | tùy ý |
| 8 | Giao lại khi subagent báo lỗi | Định tuyến | thỉnh thoảng |
| 9 | Xuất tổng kết khi toàn sách hoàn thành | Định tuyến | 1 lần |

**Kết luận**: trong 9 điểm quyết định, 6 là định tuyến thuần (tra bảng), 3 thật sự cần LLM phán định. **Tần suất định tuyến cao hơn hẳn phán định** (mỗi chương 1-2 lần so với vài lần một cuốn sách).

### 2.2 Kênh Reminder đã là bán thành phẩm của việc mã hóa luồng

Các bộ sinh trong `internal/host/reminder/` mỗi vòng sinh **chỉ thị cụ thể tới từng hành động** dựa trên sự thật:

- `flow.go` → `"hiện flow=writing, next_chapter=37. Hãy gọi thẳng subagent(writer, \"viết chương 37\")..."`
- `queue_guard.go` → `"hiện flow=rewriting, hàng đợi chờ: [3,5]. Hãy gọi ngay writer viết lại từng chương..."`
- `book_complete.go` → `"toàn sách đã hoàn thành. Hãy xuất tổng kết toàn sách..."`

**Kiến trúc hiện tại có double dispatch**:
```
tầng luật: coordinator.md định nghĩa "nếu A thì B"
  ↓
tầng Reminder: mỗi vòng cụ thể hóa luật theo sự thật → sinh "giờ hãy làm B"
  ↓
tầng LLM: đọc reminder sinh tool_call (về cơ bản là thuật lại reminder)
  ↓
SubAgent thực thi
```

**Thực tế LLM chỉ đang "thực thi" chỉ thị mà Reminder đưa**. Khâu trung gian này vừa tốn token, vừa đưa vào bất định (LLM có thể không tuân đủ reminder, ví dụ lỗi định tuyến mid đã quan sát được).

### 2.3 Tầng công cụ từng gánh quá nhiều phán đoán ngữ nghĩa

- `save_review` bản cũ từng ghi đè verdict của Editor theo ngưỡng điểm cố định và trạng thái contract; nay đã xóa, phán định văn học thuộc Editor, công cụ chỉ kiểm giao thức và ánh xạ trạng thái nguyên tử
- `commit_chapter.CheckArcBoundary()`: tính ngay `arc_end / needs_expansion / needs_new_volume`
- `commit_chapter.applyCompletion()`: phán ngay `book_complete`
- `CommitResult` trả 17 trường sự thật

**Kết luận**: bất biến tất định về lưu trữ và giai đoạn ở lại tầng công cụ, phán đoán văn học và ngữ nghĩa giao cho model.

### 2.4 Chi phí thực tế của hiện trạng

Số vòng LLM của Coordinator mỗi chương:
- **mỗi chương 1-2 turn** (đọc system prompt ~3000 tokens + reminder ~200 tokens + lịch sử + CommitResult ~500 tokens → sinh tool_call ~50 tokens)
- truyện dài 200 chương khoảng **200-400 lần gọi LLM của Coordinator**
- trong đó **~90% là định tuyến thuần** (LLM thuật lại reminder), **~10% là phán định**

**Mỗi chương ~3500-7000 tokens tiêu vào quyết định của Coordinator, 95% là thừa** (Reminder đã tính ra đáp án).

---

## 3. Phương án thiết kế: Hybrid Coordinator

### 3.1 Ý tưởng cốt lõi

**Chuyển quyết định luồng từ LLM sang Host, nhưng giữ Coordinator làm nút phán định và kênh thực thi chỉ thị**.

```
┌──────────────────────────────────────────────────────────┐
│                   Entry (TUI / headless)                   │
└────────────────────────────────┬─────────────────────────┘
                                 │ Start / Resume / Steer
┌────────────────────────────────▼─────────────────────────┐
│                            Host                            │
│                                                             │
│   ┌──────────────────────────────────────────────────┐     │
│   │  Flow Router (lõi mới thêm)                        │     │
│   │  ───────────                                      │     │
│   │  đăng ký sự kiện Coordinator: kích hoạt khi subagent tool trả về │
│   │  hàm thuần: route(Progress, Checkpoint, Boundary)  │     │
│   │      → NextInstruction                             │     │
│   │  có chỉ thị → coordinator.FollowUp(chỉ thị)         │     │
│   │  không chỉ thị (tình huống phán định) → không can thiệp, để LLM tự chủ │
│   └──────────────────────────────────────────────────┘     │
│                                                             │
│   giữ: API vòng đời / Observer / Usage Tracker              │
│   giữ: resume.go (đơn giản hóa, không đổi logic lõi)         │
└────────────────────────────────┬─────────────────────────┘
                                 │
┌────────────────────────────────▼─────────────────────────┐
│                    Coordinator Agent (LLM)                  │
│                                                             │
│   thu hẹp trách nhiệm còn hai loại:                            │
│   1. nhận chỉ thị FollowUp của Host → sinh tool_call tương ứng │
│   2. tự chủ phán định khi Steer của người dùng tới (truy vấn/đánh giá sửa đổi) │
│                                                             │
│   coordinator.md: 88 dòng → ~25 dòng                         │
│   MaxTurns: 1000 giữ (phản hồi steer người dùng + thực thi chỉ thị Host)      │
└────────────────────────────────┬─────────────────────────┘
                                 │
                                 ▼
         ┌──────────────────────┼───────────────────────┐
         ▼                      ▼                       ▼
    ┌────────┐             ┌────────┐             ┌────────┐
    │Architect│             │ Writer │             │ Editor │
    └────────┘             └────────┘             └────────┘
```

### 3.2 Phân chia lại trách nhiệm

| Tầng | Làm gì | Không làm gì |
|---|---|---|
| **Host / Flow Router** | Đọc sự thật → định tuyến hàm thuần → chỉ thị FollowUp | Tự gọi SubAgent (vẫn qua Coordinator) |
| **Coordinator** | Thực thi chỉ thị Host + phán định can thiệp người dùng + chọn kiến trúc sư lúc khởi động | Tự quyết "bước tiếp làm gì" |
| **SubAgent (A/W/E)** | Công việc riêng của từng cái | Không đổi |
| **Tầng công cụ** | Ghi nguyên tử xuống đĩa + trả sự thật | Không đổi |

**Bất biến then chốt**:
- ✅ Coordinator vẫn là một agent run liên tục, giữ "cảm nhận liên tục" toàn sách
- ✅ Steer của người dùng vẫn qua `coordinator.Inject()`, giữ năng lực ngắt tức thời
- ✅ SubAgentTool vẫn do LLM gọi (đi đường gốc của agentcore), luồng sự kiện / ContextManager / chuyển model đều không đổi
- ✅ agentcore không sửa gì

### 3.3 Logic cụ thể của Flow Router

```go
// internal/host/flow/router.go

type NextInstruction struct {
    Agent  string   // architect_long / architect_short / writer / editor
    Task   string   // mô tả nhiệm vụ giao cho subagent
    Reason string   // lý do cho Coordinator thấy (tùy chọn, tiện gỡ rối)
}

type RouterState struct {
    Progress        *domain.Progress
    LatestCheckpoint *domain.Checkpoint
    // ranh giới cung ở chế độ phân tầng (tính khi chương trước đã hoàn thành)
    LastCompleted   int
    ArcBoundary     *store.ArcBoundary
    HasArcReview    bool
    HasArcSummary   bool
    // thiếu mục trong thiết lập nền tảng
    FoundationMissing []string
}

// Route trả chỉ thị bước tiếp. Trả nil nghĩa là để Coordinator tự chủ phán định (tình huống phán định).
func Route(s RouterState) *NextInstruction {
    p := s.Progress

    // 0. Trạng thái cuối: để LLM xuất tổng kết, không định tuyến
    if p.Phase == domain.PhaseComplete {
        return nil
    }

    // 1. Giai đoạn quy hoạch: phán định (chọn kiến trúc sư) do LLM làm, không định tuyến
    if p.Phase != domain.PhaseWriting {
        return nil
    }

    // 2. Giai đoạn viết
    // 2a. Hàng đợi viết lại/gọt giũa ưu tiên
    if len(p.PendingRewrites) > 0 {
        ch := p.PendingRewrites[0]
        verb := "viết lại"
        if p.Flow == domain.FlowPolishing {
            verb = "gọt giũa"
        }
        return &NextInstruction{
            Agent:  "writer",
            Task:   fmt.Sprintf("%s chương %d", verb, ch),
            Reason: fmt.Sprintf("hàng đợi PendingRewrites còn %d chương", len(p.PendingRewrites)),
        }
    }

    // 2b. Đang thẩm duyệt: không định tuyến, để Coordinator đi theo phân nhánh verdict dựa trên kết quả save_review
    if p.Flow == domain.FlowReviewing {
        return nil
    }

    // 2c. Hậu xử lý cuối cung ở chế độ phân tầng
    if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
        b := s.ArcBoundary
        if !s.HasArcReview {
            return &NextInstruction{
                Agent:  "editor",
                Task:   fmt.Sprintf("thẩm duyệt cấp cung cho tập %d cung %d", b.Volume, b.Arc),
                Reason: "thẩm duyệt cuối cung chưa hoàn tất",
            }
        }
        if !s.HasArcSummary {
            return &NextInstruction{
                Agent:  "editor",
                Task:   fmt.Sprintf("sinh tóm tắt tập %d cung %d", b.Volume, b.Arc),
                Reason: "tóm tắt cung chưa hoàn tất",
            }
        }
        if b.NeedsExpansion {
            return &NextInstruction{
                Agent:  "architect_long",
                Task:   fmt.Sprintf("mở rộng tập %d cung %d (save_foundation type=expand_arc)", b.NextVolume, b.NextArc),
                Reason: "cung khung xương kế tiếp chờ mở rộng",
            }
        }
        if b.NeedsNewVolume {
            return &NextInstruction{
                Agent:  "architect_long",
                Task:   "đánh giá và thực thi save_foundation(type=append_volume) hoặc mark_final",
                Reason: "hết tập cần quyết định thêm tập mới",
            }
        }
    }

    // 2d. Viết tiếp bình thường
    next := p.NextChapter()
    return &NextInstruction{
        Agent:  "writer",
        Task:   fmt.Sprintf("viết chương %d", next),
        Reason: "viết tiếp",
    }
}
```

**Đặc tính hàm**:
- Hàm thuần (vào RouterState, ra NextInstruction)
- Unit test được (cho trạng thái, khẳng định kết quả định tuyến)
- **Trả nil là hợp lệ** — nghĩa là "đây là tình huống phán định, hãy để LLM tự chủ"

### 3.4 Thời điểm kích hoạt

Host đăng ký sự kiện `agentcore.EventToolExecEnd`:

```go
coordinator.Subscribe(func(ev agentcore.Event) {
    if ev.Type == agentcore.EventToolExecEnd && ev.Tool == "subagent" && !ev.IsError {
        // SubAgent vừa trả về → đọc trạng thái mới nhất → định tuyến
        h.flowRouter.Dispatch()
    }
})
```

```go
func (r *FlowRouter) Dispatch() {
    state := r.loadState()
    instruction := Route(state)
    if instruction == nil {
        return // tình huống phán định, để LLM tự chủ
    }
    msg := formatInstruction(instruction)
    _ = r.coordinator.FollowUp(agentcore.UserMsg(msg))
}

func formatInstruction(i *NextInstruction) string {
    return fmt.Sprintf(
        "[chỉ thị Host giao xuống] bước tiếp: gọi subagent(%s, %q)\n"+
        "lý do: %s\n"+
        "Đây là chỉ thị rõ ràng của tầng luồng, hãy thực thi ngay, đừng gọi novel_context trước, đừng xuất suy luận trước.",
        i.Agent, i.Task, i.Reason,
    )
}
```

### 3.5 Khả năng phản hồi và đồng thời

**Đường Steer của người dùng** (không đổi):
```
Steer → coordinator.Inject(UserMsg("[can thiệp người dùng] xxx"))
```

- Đang chạy: tin nhắn chèn vào hàng đợi run hiện tại
- Idle: dựng lại run
- Paused: xếp hàng

**Đồng thời giữa chỉ thị định tuyến + Steer**:
- Đều vào hàng đợi tin nhắn của Coordinator, xử lý theo thứ tự gốc của agentcore
- Nếu Host vừa gửi `FollowUp("[chỉ thị Host] viết chương 37")`, ngay sau đó người dùng Steer `"khoan, chỉnh văn phong"`
  - Coordinator xử lý chỉ thị Host trước? Hay xử lý Steer trước?
  - **Ngữ nghĩa của `Inject` là chen lên đầu hàng đợi hiện tại**, nên Steer được xử lý trước
  - Đây là hành vi mong muốn: can thiệp người dùng ưu tiên cao hơn điều phối định kỳ của Host

**Tránh xung đột giữa chỉ thị Host và Steer**:
- Flow Router tạm dừng vài turn sau khi nhận tín hiệu "Steer đã được tiêm" (để Coordinator xử lý xong Steer rồi mới định tuyến)
- Nhận biết kết quả xử lý Steer bằng cách đăng ký `agentcore.EventMessageEnd` + kiểm tra thay đổi trạng thái Progress

### 3.6 Ví dụ đơn giản hóa coordinator.md

Cắt từ 88 dòng xuống khoảng 25 dòng:

```markdown
Bạn là tổng điều phối viên sáng tác tiểu thuyết.

## Chế độ làm việc

**Trục chính**: Host sẽ giao tin nhắn `[chỉ thị Host giao xuống]` sau mỗi lần subagent trả về, cho bạn biết bước tiếp gọi subagent nào làm gì. Nhận chỉ thị là sinh tool_call tương ứng ngay, đừng gọi novel_context suy luận trước, đừng thuật lại.

**Phán định**: gặp các tình huống sau bạn phải tự phán đoán (Host không giao chỉ thị, bạn phải chủ động hành động):

### Lúc khởi động: chọn kiến trúc sư

- Mặc định → `architect_long`
- Chỉ khi người dùng yêu cầu rõ truyện ngắn/đơn tập/tiểu phẩm và dung lượng giới hạn trong 25 chương → `architect_short`

Nếu đầu vào người dùng < 20 chữ, hãy bổ sung hướng khác biệt hóa, độc giả mục tiêu, ít nhất một móc câu truyện không thông thường vào mô tả task trước khi giao.

### Steer của người dùng

Định dạng: `[can thiệp người dùng] xxx`

- **Loại truy vấn** (hỏi trạng thái/thiết lập): xuất thẳng câu trả lời bằng văn bản, không cần gọi công cụ nữa; Host sẽ tiếp tục giao việc.
- **Loại sửa đổi** (yêu cầu đổi thiết lập/viết lại/chỉnh văn phong): đánh giá phạm vi ảnh hưởng:
  - liên quan đổi thiết lập → gọi architect_* làm `save_foundation(type=...)`
  - liên quan chương đã viết → để công cụ tự ghi chương mục tiêu vào `PendingRewrites` (có thể nêu ý định viết lại khi gọi writer lần nữa)
  - chỉ ảnh hưởng văn phong phía sau → mô tả ngắn yêu cầu rồi đính vào mô tả task của writer lần nhận chỉ thị Host kế tiếp

## Công cụ

- `subagent(agent, task)`: gọi subagent
- `novel_context`: chỉ dùng khi truy vấn người dùng cần, đừng gọi trước khi chỉ thị Host tới

## Subagent

- `architect_long` / `architect_short` / `writer` / `editor`

## Cấm

- Gọi novel_context trước rồi mới hành động khi chỉ thị Host tới
- Tự quyết bước tiếp khi không có Steer người dùng và không có chỉ thị Host
```

### 3.7 Kênh Reminder thu gọn lớn

**Xóa**:
- `flow.go` (Host FollowUp đã giao chỉ thị cụ thể, nhắc định tuyến của Reminder mất giá trị)
- `queue_guard.go` (hàng đợi do Host Router đảm bảo)
- `book_complete.go` (Host giao chỉ thị xuất tổng kết khi Phase=Complete)

**Giữ**:
- `subagent_guards.go` (StopGuard của Writer/Architect/Editor, đảm bảo subagent không kết thúc tay không)
- Thêm một `foundation_reminder.go` nhẹ: giai đoạn quy hoạch báo Coordinator biết còn thiếu mục (đây là **thông tin phán định cần**, không phải chỉ thị định tuyến)

**Giữ StopGuard**:
- Giữ StopGuard của Coordinator (chặn end_turn khi `Phase != Complete` làm lưới đỡ)
- Thêm tiêm nhắc khi "nhận chỉ thị Host nhưng lượt này chưa gọi subagent tương ứng"

### 3.8 resume.go đơn giản hóa chút

Hiện `buildResumePrompt` sinh chỉ thị ngôn ngữ tự nhiên chính xác tới từng bước theo checkpoint (121 dòng).

Kiến trúc mới:
- Khi Resume, trước tiên đọc Progress, Flow Router tính ra `NextInstruction`
- Coordinator nhận một resume prompt **rất ngắn**, rồi chờ chỉ thị FollowUp của Host

```
[khôi phục] sách 「xxx」 đã hoàn thành N chương, vào giai đoạn XX.
Hãy chờ chỉ thị tiếp theo của Host, hoặc xử lý can thiệp người dùng có thể còn sót trong lúc dừng máy.
```

Gần như mọi logic phân nhánh hạ xuống Flow Router (Router vốn phải định tuyến theo trạng thái, Resume không cần đường riêng).

---

## 4. Đánh giá mức đạt mục tiêu

### 4.1 Ổn định

| Rủi ro | Hiện tại | Kiến trúc mới |
|---|---|---|
| Coordinator chọn sai architect | đã từng xảy ra (lỗi định tuyến mid) | khởi động vẫn là phán định, nhưng prompt từ ba mức còn hai (đã làm), phạm vi lỗi thu hẹp lớn |
| Coordinator không tuân "chỉ nói viết chương N" | đã từng xảy ra | Host giao chỉ thị định dạng cố định, không cần LLM sinh mô tả task nữa |
| Coordinator bỏ sót kiểm queue_drained | đã từng xảy ra | Host Router ép đi theo thứ tự |
| Sau commit cuối cung Coordinator quên gọi editor | có thể | Host Router phát hiện IsArcEnd && !HasArcReview là giao ngay |
| Bỏ sót nhánh khôi phục sau sập | lỗ hổng đã biết | máy trạng thái của Flow Router bao phủ tự nhiên mọi nhánh |
| StopGuard chặn liên tiếp 5 lần nâng lên fatal | đang có | chỉ thị Host rõ ràng thì LLM khó chặn liên tiếp (trừ khi prompt hỏng nặng) |

### 4.2 Lợi ích từ nâng cấp LLM

| Chiều | Mức giữ |
|---|---|
| Nâng model Writer → chất lượng viết | 100% |
| Nâng model Editor → thẩm duyệt chính xác | 100% |
| Nâng model Architect → quy hoạch tinh tế | 100% |
| **Nâng model Coordinator → phán định chính xác hơn** | **100%** (giữ tình huống phán định) |
| ~~Nâng model Coordinator → định tuyến chính xác hơn~~ | bỏ (tỷ lệ lỗi định tuyến vốn phải bằng 0, không cần LLM thông minh hơn) |

**Giữ quan trọng**: đánh giá can thiệp người dùng, chọn kiểu kiến trúc sư, phán đoán biên verdict và các tình huống phán định khác vẫn do LLM xử lý, nâng cấp model hưởng lợi trực tiếp.

### 4.3 Năng lực đa agent

- Số SubAgent, chức năng, cách lắp ghép **hoàn toàn không đổi**
- Model dị thể (cấu hình độc lập cho coordinator/architect/writer/editor) **hoàn toàn không đổi**
- Coordinator vẫn là run liên tục, giữ "góc nhìn toàn sách"
- Phương tiện cộng tác (sản phẩm trong Store) không đổi

### 4.4 Khả năng phản hồi

- Năng lực ngắt Steer của người dùng qua `coordinator.Inject` **giữ hoàn toàn**
- Host Router giao chỉ thị khi SubAgent trả về, đi cùng kênh tin nhắn với Steer người dùng
- Ưu tiên của Inject cao hơn FollowUp (ngữ nghĩa `Inject` là chen hàng), Steer không bị chỉ thị Host chen mất

### 4.5 Chi phí token

Hiện tại mỗi chương: Coordinator ~3500-7000 tokens × 1-2 turn = 3500-14000 tokens

Kiến trúc mới mỗi chương:
- prompt Coordinator nén từ ~3000 tokens xuống ~800 tokens
- mỗi chương vẫn cần 1 turn (Coordinator đọc chỉ thị FollowUp + sinh tool_call)
- tổng ~1000-1500 tokens

**Tiết kiệm 60-80%**. Truyện dài 200 chương tiết kiệm khoảng 400k-1M tokens (không bằng 100% của phương án cấp tiến, nhưng không hy sinh khả năng phản hồi và góc nhìn toàn sách).

---

## 5. Ảnh hưởng lên docs/architecture.md

### 5.1 Điều chỉnh §2 nguyên tắc cốt lõi

**Nguyên tắc một** (LLM dẫn dắt vòng lặp chính) → điều chỉnh thành:
```
LLM dẫn dắt sáng tác và phán định, Host dẫn dắt định tuyến luồng.

- sáng tác và phán định (quyết định cần hiểu ngữ nghĩa, phán đoán chất lượng, nhận diện ý định) vẫn để LLM
- định tuyến luồng (đọc sự thật → tra bảng → gửi chỉ thị) do mã Host gánh
- Host không vòng qua Coordinator gọi SubAgent trực tiếp, mà giao chỉ thị rõ ràng qua FollowUp,
  giữ Coordinator làm kênh thực thi chỉ thị và nút phán định
```

**Nguyên tắc hai** (đặt cược vào năng lực model, không đặt cược vào cứng hóa) → điều chỉnh thành:
```
Đặt cược vào model ở chiều sáng tác và phán định (năng lực phán định của Writer/Editor/Architect/Coordinator),
thể hiện bằng mã ở chiều định tuyến luồng (không gian quyết định của agent dọc là đóng, nhiệm vụ tra bảng không có lợi ích LLM).
```

### 5.2 Điều chỉnh §13 danh sách cấm

- §13.13 "không làm mặt điều khiển tất định kiểu Host đọc file tín hiệu → tiêm chỉ thị bước tiếp" →
  **sửa cách nói**: "không dùng file tín hiệu làm IPC (đọc thẳng Progress + Checkpoint là đủ), Host đọc sự thật rồi giao chỉ thị gọi subagent rõ ràng qua `coordinator.FollowUp`, là định tuyến dọc hợp lý"
- §13.14 "không làm máy trạng thái cứng hóa chuyển Flow" →
  **sửa cách nói**: "nhãn Flow vẫn chỉ do công cụ cập nhật (không viết máy trạng thái 'nếu A thì SetFlow(B)' trong Host), nhưng Flow Router được phép dựa trên Flow và sự thật khác để quyết bước tiếp gọi ai"

### 5.3 Điều chỉnh §7 lắp ghép Agent

- Giữ lắp ghép Coordinator
- `coordinator.md` cắt từ 88 dòng xuống ~25 dòng
- Kênh Reminder thu gọn (xóa flow/queue_guard/book_complete, giữ foundation/subagent_guards)
- Thêm package `internal/host/flow/`

---

## 6. Điểm yếu đã biết (liệt kê trung thực)

### 6.1 Tiến hóa dài hạn của Flow Router

- Khi thêm kịch bản mới (trạng thái flow mới, hậu xử lý cuối cung mới), switch-case của Router sẽ dài ra
- Cần ràng buộc nghiêm: **chỉ xử lý định tuyến, không xử lý logic nghiệp vụ**; luật quyết định viết unit test
- Cảnh báo kiểu `handleSubAgentDone` của v0.0.1 luôn có giá trị; nhưng phương án này tránh trượt thành god object bằng "hàm thuần + unit test + chỉ gọi sự thật thuần"

### 6.2 Độ phức tạp của can thiệp người dùng

- Thiết kế hiện tại giao hoàn toàn Steer cho LLM của Coordinator phán định
- Nhưng một số Steer vắt qua nhiều loại (như "sửa rõ nhân vật A mấy chương trước + sau này thêm tuyến phụ cho anh ta")
- Cần dựa vào năng lực LLM để tách nhỏ, prompt phải cho hướng dẫn rõ
- **Phần này nâng cấp model hưởng lợi trực tiếp** (so với enum phân loại cứng của InterventionAgent, LLM phán định linh hoạt khớp kịch bản thật hơn)

### 6.3 Phụ thuộc tiên quyết vào tính nhất quán tầng sự thật

- Router quyết định dựa trên Progress + Checkpoint, tầng sự thật phải đáng tin
- `withWriteLock` một file + tmp/rename đảm bảo thay thế nguyên tử; bước xuyên file của `commit_chapter` được khôi phục bằng payload đầy đủ của PendingCommit, ảnh chụp chính văn và phát lại idempotent theo giai đoạn; thao tác cấu trúc thì sửa bản chiếu dẫn xuất theo cùng tham số; đều không tuyên bố giao dịch nguyên tử kiểu cơ sở dữ liệu
- Nhưng nếu tầng sự thật mất nhất quán (ví dụ Progress nói chương 3 hoàn thành mà chapters/ không có), Router sẽ quyết sai
- Đề xuất: thêm một **kiểm tra nhất quán tầng sự thật** lúc khởi động (nếu phát hiện Progress.CompletedChapters không khớp thư mục chapters/ thì báo warning)

### 6.4 Coordinator vẫn còn khả năng LLM định tuyến

- Kể cả chỉ thị rõ ràng, LLM có thể "sáng tạo" không thực thi (ví dụ sinh một đoạn suy nghĩ rồi mới gọi công cụ)
- StopGuard đỡ lưng: nhận chỉ thị Host mà lượt này chưa gọi subagent thì tiêm nhắc
- Đây là lưới đỡ, không phải cấm — model mạnh thỉnh thoảng "suy nghĩ thêm một bước" cũng không tệ

### 6.5 Yêu cầu bao phủ test tăng

- Flow Router là hàm thuần, bắt buộc có unit test đầy đủ (bao phủ mọi tổ hợp Phase × Flow × Boundary)
- Test tích hợp: mô phỏng chuỗi đầy đủ "commit → router → FollowUp → Coordinator phản hồi → subagent"
- Test khôi phục sau sập: kill tiến trình rồi resume, khẳng định Router suy ra đúng bước tiếp

---

## 7. Lộ trình triển khai

### Giai đoạn 1: tăng cường tầng sự thật (khoảng 0.5 ngày)

- Bổ sung kiểm tra nhất quán ở §6.3: quét một lần lúc khởi động/Resume, sinh warning
- Đảm bảo API `store.HasArcReview(vol, arc)` và `HasArcSummary(vol, arc)` dùng được (chưa có thì thêm)

### Giai đoạn 2: đưa khung Flow Router vào (khoảng 1 ngày)

- Tạo package `internal/host/flow/`:
  - `route.go` — hàm thuần `Route(state) → *NextInstruction`
  - `dispatcher.go` — đăng ký sự kiện + giao qua FollowUp
  - `route_test.go` — unit test bao phủ mọi nhánh
- Điều khiển kích hoạt bằng công tắc config `flow_driven: true/false`
- Mặc định tắt (false), chạy đối chứng trước

### Giai đoạn 3: kích hoạt và kiểm chứng (khoảng 1 ngày)

- Bật `flow_driven: true`
- Chạy một cuốn 30-50 chương, so các chỉ số:
  - số lần gọi LLM của Coordinator
  - số lỗi định tuyến (phải bằng 0)
  - khả năng phản hồi (ngắt steer có bình thường không)
- Sửa bug, điều chỉnh luật Router

### Giai đoạn 4: đơn giản hóa coordinator.md + thu gọn Reminder (khoảng 0.5 ngày)

- Sửa coordinator.md theo §3.6
- Xóa `reminder/flow.go / queue_guard.go / book_complete.go`
- Giữ foundation reminder cần thiết
- Cập nhật StopGuard của subagent nếu cần (thường không cần)

### Giai đoạn 5: đơn giản hóa resume.go (khoảng 0.5 ngày)

- Xóa phần lớn phân nhánh của `buildResumePrompt`
- Thay bằng thông điệp ngắn gọn chung "[khôi phục] hãy chờ chỉ thị Host"
- Sau Resume, Router suy ra tự nhiên hành động tiếp

### Giai đoạn 6: cập nhật tài liệu kiến trúc (khoảng 0.5 ngày)

- Sửa `docs/architecture.md` §2 / §13 / §7 theo §5
- Đổi trạng thái tài liệu đề xuất này thành "đã tiếp thu", lưu trữ vào `docs/history/`

### Giai đoạn 7: giai đoạn quan sát (2-4 tuần)

- Chạy liên tiếp 2-3 truyện dài (mỗi cuốn 100+ chương)
- Ghi mọi lỗi định tuyến (nếu có), vấn đề khả năng phản hồi, hành vi bất ngờ của Coordinator
- Tinh chỉnh luật Router và coordinator.md theo quan sát

**Tổng khoảng 4 ngày triển khai + giai đoạn quan sát**.

---

## 8. Bảng đối chiếu

| Chiều | Kiến trúc hiện tại | Hybrid (phương án này) | Phương án cấp tiến (phụ lục A) |
|---|---|---|---|
| Ổn định | trung bình (LLM thỉnh thoảng định tuyến sai) | **cao** | cao |
| Khả năng phản hồi | cao | **cao** | **thấp** (Host gọi SubAgent thẳng không ngắt được) |
| Lợi ích LLM | 100% | **100%** | 85% (bỏ chiều định tuyến) |
| Tiết kiệm token | 0 | ~70% | ~95% |
| Góc nhìn toàn sách | có | **có** | không (mỗi SubAgent độc lập) |
| Chi phí triển khai | - | trung bình (khoảng 4 ngày) | cao (khoảng 1 tuần + sửa agentcore) |
| Cập nhật tài liệu | - | nhỏ (tinh chỉnh §2/§13) | lớn (viết lại nguyên tắc §2) |
| Cần sửa agentcore | - | không | có thể (gọi SubAgent thẳng) |
| Độ khó rollback | - | thấp (công tắc config) | cao |

---

## 9. Điểm quyết định

1. **Có tiếp thu đề xuất này (Hybrid Coordinator) không?** [ ] tiếp thu · [ ] sửa rồi tiếp thu · [ ] không tiếp thu
2. Giai đoạn 3 có làm thành PR độc lập để lên sóng kiểm chứng trước không? [ ]
3. Điều chỉnh §2 / §13 của `docs/architecture.md` có xử lý luôn trong lần này không? [ ]
4. Độ dài giai đoạn quan sát: [ ] 2 tuần · [ ] 4 tuần · [ ] lâu hơn

---

## Phụ lục A: phương án cấp tiến đã được đánh giá (xóa hoàn toàn Coordinator)

> Bản thảo đầu. Bị hạ xuống thành tham khảo vì khả năng phản hồi thoái hóa, tính khả thi kỹ thuật còn nghi ngờ, và Coordinator mất góc nhìn toàn sách.

Lõi của phương án cấp tiến: Host gọi thẳng `SubAgentTool.Execute`, không qua LLM của Coordinator.

**Vấn đề đã nhận diện**:

1. **Khả năng phản hồi thoái hóa**: `SubAgentTool.Execute` là lệnh gọi đồng bộ chặn, Steer người dùng phải chờ SubAgent hiện tại trả về mới xử lý được. Kiến trúc hiện tại `Inject` ngắt được ngay.
2. **Tính khả thi kỹ thuật còn nghi ngờ**:
   - Host gọi thẳng SubAgentTool vi phạm quy ước dùng agentcore
   - luồng sự kiện (`Subscribe` Event) có thể không nổi bọt đúng cho observer
   - đường `ContextManagerFactory` / callback `OnMessage` của SubAgent chưa rõ
   - cần sửa agentcore hoặc đại tu observer
3. **Coordinator mất góc nhìn toàn sách**: mỗi SubAgent run độc lập, không có "người canh LLM liên tục". Trong chạy dài, trôi dạt văn phong, nhân vật đứt gãy và các vấn đề khác mất đi một lớp canh ngầm.
4. **Đơn giản hóa InterventionAgent quá mức**: phương án cấp tiến dùng enum (query/modify_setting/rewrite_chapters/adjust_style/noop) phân loại ý định người dùng, Steer thật có thể vắt qua nhiều loại, ép schema sẽ phân loại sai.
5. **Khối lượng viết lại tài liệu kiến trúc lớn**: nguyên tắc cốt lõi §2 bị lật, 30% luận thuật của tài liệu bị ảnh hưởng.
6. **FlowDriver sẽ lớn thành god object**: một vòng lặp nhét hết logic định tuyến, thêm kịch bản nào cũng phải sửa, cùng hình dạng với `handleSubAgentDone` của v0.0.1.

Phương án Hybrid tránh được 4 vấn đề đầu, vấn đề 5 hạ thành tinh chỉnh, vấn đề 6 được kiểm soát bằng "hàm thuần + unit test".

---

## Phụ lục B: chi tiết vị trí của từng điểm quyết định

| Điểm quyết định | Vị trí hiện tại | Vị trí kiến trúc mới | Loại |
|---|---|---|---|
| Chọn kiến trúc sư | coordinator.md L26-29 | LLM Coordinator phán định (lúc khởi động) | phán định |
| Mở rộng đầu vào | coordinator.md L31 | LLM Coordinator phán định (lúc khởi động) | phán định |
| Vòng bổ sung quy hoạch | coordinator.md L36-38 | nhánh Phase=Premise/Outline của Host Router (trả nil để LLM tự chủ hoặc FollowUp architect rõ ràng) | hỗn hợp |
| Bước tiếp mỗi chương | coordinator.md L46-51 + reminder/flow | **nhánh 2d của Host Router** (FollowUp writer) | định tuyến |
| Thẩm duyệt cuối cung | coordinator.md L78-82 | **nhánh 2c của Host Router** (FollowUp editor/architect) | định tuyến |
| Phân nhánh verdict | coordinator.md L59-61 + công cụ save_review | tầng công cụ đã mã hóa, Router chỉ đọc Flow | định tuyến (đã xong) |
| Can thiệp người dùng | coordinator.md L67-70 | LLM Coordinator phán định (khi nhận tin nhắn Inject) | phán định |
| Giao lại khi kiến trúc sư báo lỗi | coordinator.md L40 | Host Router phát hiện FoundationMissing không đổi, đếm số lần thử lại | định tuyến |
| Tổng kết kết thúc sách | coordinator.md L63-65 + reminder/book_complete | Host Router phát hiện Phase=Complete → FollowUp "xuất tổng kết" | định tuyến |

---

## Phụ lục C: vị trí mã nguồn tham khảo

- `assets/prompts/coordinator.md` — chờ đơn giản hóa
- `internal/host/reminder/flow.go` / `queue_guard.go` / `book_complete.go` — chờ xóa
- `internal/host/reminder/subagent_guards.go` — giữ
- `internal/host/reminder/stop_guard.go` — giữ + thêm kiểm "nhận chỉ thị Host phải thực thi"
- `internal/host/resume.go` — đơn giản hóa lớn
- `internal/host/observer.go` — đăng ký mới EventToolExecEnd để kích hoạt Router
- `internal/host/flow/` — package mới thêm
- `internal/tools/commit_chapter.go` L220-280 — 17 trường của CommitResult đã đầy đủ
- `internal/tools/save_review.go` — ánh xạ nguyên tử verdict của Editor sang Flow/hàng đợi làm lại
- `internal/store/outline.go` `CheckArcBoundary` — API sự thật ranh giới cung