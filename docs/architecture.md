# Kiến trúc lúc chạy của ainovel-cli

> Tầng sự thật tất định, tầng ngữ nghĩa tự chủ: một Engine tất định tuần tự, ba Worker tự chủ, một vài hàm Arbiter gọi theo nhu cầu, một tầng sự thật trên hệ thống file.
>
> 2026-07-12, việc thay mặt điều khiển hoàn tất: vòng lặp dài LLM của Coordinator về hưu, do Engine (vòng lặp tất định) + Arbiter (hàm phán định ngữ nghĩa) tiếp quản. Quyết định thiết kế và bản ghi phản biện xem `docs/engine-arbiter.md`, RFC xem `docs/engine-rfc.md`.

---

## 1. Mục tiêu (theo ưu tiên)

1. **Ổn định**: một câu đầu vào, viết trọn cuốn tiểu thuyết một cách ổn định (200~500 chương). Giữa chừng không tự gián đoạn vì vấn đề kiến trúc.
2. **Chất lượng lặp được**: prompt / tài liệu tham khảo / chiều thẩm duyệt / chiến lược ngữ cảnh chỉnh được độc lập, không kéo theo kiến trúc.
3. **Khôi phục được**: sau sập, mất mạng, tạm dừng có thể tiếp tục từ checkpoint gần nhất.
4. **Quan sát được**: tiến độ, sản phẩm, thời gian của từng bước mỗi chương đều tra được.

"Ổn định" là tiền đề, "chất lượng" là tầng trên. Mọi quyết định kiến trúc phục vụ tính ổn định trước.

---

## 2. Nguyên tắc cốt lõi

### 2.1 Phép chia ba: quyết định về đúng chỗ theo bản chất

- **Chuyển trạng thái vét cạn được → mã**. "Viết xong một chương thì giao cho ai" là tra bảng đọc sự thật: `flow.Route` hàm thuần + kiểm tra đặc tả vét cạn hàng vạn tổ hợp, tỷ lệ lỗi tiến về 0, chi phí LLM bằng không.
- **Phán đoán ngữ nghĩa có biên rõ ràng → hàm LLM (Arbiter)**. Chọn kiến trúc sư, phân loại can thiệp người dùng, lối thoát cho thất bại/bế tắc: sự thật vào, quyết định có cấu trúc ra, kiểm tra cơ học đỡ lưng, mỗi lần phán định ghi xuống đĩa và phát lại được.
- **Sáng tác mở → vòng lặp LLM (Worker)**. Trong phạm vi một chương, một lần thẩm duyệt, một lần quy hoạch, architect/writer/editor hoàn toàn tự chủ.

Đối xứng hai mặt phẳng là kỷ luật xuyên suốt — mọi điểm quyết định mới trong tương lai đi theo hình dạng này, không phát minh chế độ mới:

```
mặt tất định:  flow.LoadState   → flow.Route     → Instruction   (kiểm tra đặc tả vét cạn)
mặt ngữ nghĩa: arbiter.Collect* → arbiter.Decide* → XxxDecision   (decisions.jsonl + hồi quy eval)
              └── thu thập sự thật (IO) ──┘└── lõi (phát lại offline được) ──┘└── Engine thực thi ──┘
```

### 2.2 Công cụ là cửa vào duy nhất của tầng sự thật

Mọi tương tác với hệ thống file, Progress, Checkpoint đều do công cụ thực hiện. Một file đơn lẻ dùng thay thế nguyên tử `temp + fsync + rename`; ghi tuần tự xuyên file không giả danh giao dịch cơ sở dữ liệu: commit chương dùng Saga `PendingCommit` lưu trữ bền vững, ghi cấu trúc dùng phát lại idempotent tất định và lộ lỗi tường minh. Mỗi bước đều phải kiểm tra lỗi; chỉ luồng đã lưu trữ bền vững ý định khôi phục mới được cam kết khôi phục theo nguyên payload qua các lần khởi động lại.

### 2.3 Tầng quan sát chỉ quan sát

UI, chẩn đoán, log sự kiện đều là bên tiêu thụ thụ động, chiếu ra từ luồng sự kiện / sản phẩm chỉ đọc. Đọc sự thật, không sinh sự thật, không ảnh hưởng luồng điều khiển.

Dữ liệu quan sát chia nghiêm ngặt ba tầng: `agentcore.ProgressPayload` là tầng truyền tải, văn bản lỗi phải đầy đủ và không được chứa chiến lược cắt của UI; `host.Event.Summary` là ngữ nghĩa hiển thị ngắn, `Detail` là chẩn đoán đầy đủ; log ghi file ưu tiên ghi `Detail` đầy đủ, TUI chỉ đọc `Summary` và cắt theo chiều rộng terminal khi kết xuất cuối. Logger ghi file do `Host` nắm giữ: trước tiên lấy thuê thư mục tiểu thuyết, rồi lập phiên log, sau đó mới lắp ghép Store, model và Engine; nhờ đó không vòng qua độc chiếm một sách, mà vẫn bao phủ toàn bộ lúc lắp ghép và lúc đóng log. "Log đầy đủ" nghĩa là chuỗi lỗi, tham số gốc không hợp lệ và metadata vòng đời không mất, không phải chép lại chính văn tiểu thuyết đã sinh thành công vào `tui.log`; nội dung lớn vẫn do sản phẩm Store và `meta/sessions` gánh.

**`internal/diag` là hệ thống con khả quan sát duy nhất của engine** — hạ tầng hạng nhất, nhưng không phải lõi sản phẩm. Nó đọc chéo gần như mọi sản phẩm + session + log + checkpoint, gánh hai vai: ① **chẩn đoán chất lượng sáng tác** (luật → Finding, báo cáo trên màn `/diag`); ② **dò lỗi lúc chạy + xuất đã khử nhạy cảm** (bóc chính văn khỏi bộ khung hành vi + gộp vòng lặp → `meta/diag-export.md` ghi đè).

**Kỷ luật quan sát viên (không được nới)**: diag được chẩn đoán, được đề xuất, nhưng **không bao giờ tự tay làm** — không tự sửa, không chạy tiếp, không đổi luồng (bài học lịch sử xem §10 mục 5).

### 2.4 Tầng sự thật phẳng

Chỉ có ba loại sự thật:

- **Progress** — chỉ mục tiến độ (đã viết tới chương mấy, danh sách chờ viết lại)
- **Checkpoint** — bản ghi tiến triển theo bước (plan / draft / commit / review / arc_summary)
- **Artifact** — chính văn chương, đại cương, nhân vật, tóm tắt v.v.

Không đưa vào trừu tượng WorkflowInstance / TaskInstance / Command. Sự thật phụ (bể phản hồi đại cương, bản ghi vi phạm cơ học, audit phán định) cũng là jsonl phẳng, mỗi cái có đúng một bên sản xuất và một bên tiêu thụ.

### 2.5 Bốn thiết luật

**Thiết luật một: công cụ chỉ trả sự thật, không trả chỉ thị điều phối xuyên tầng**. `commit_chapter` trả các trường có cấu trúc như `arc_end` / `needs_expansion`; không kèm chuỗi chỉ thị kiểu `[hệ thống]`. Trường `next_step` bên trong subagent là chỉ dẫn nội tuyến của phát biểu sự thật ("tôi vừa lưu plan, bước tiếp là draft"), không tính là vi phạm — xem §6.3.

**Thiết luật hai: định tuyến luồng do Flow Router gánh, thực thi do Engine gánh**. `Route(state) → *Instruction` trong `internal/flow/router.go` là hàm thuần (đóng đinh bằng kiểm tra đặc tả vét cạn hàng vạn tổ hợp); mỗi vòng Engine đọc sự thật từ store, Route suy ra chỉ thị, **chạy Worker trực tiếp theo kiểu lập trình** (`subagent.Runner.Run`, tham số/kết quả/chuỗi lỗi có kiểu), không có tầng chuyển tiếp công cụ qua LLM. Trả nil nghĩa là tình huống ngữ nghĩa (thu kết kết thúc sách/chờ can thiệp) hoặc dừng tự nhiên. **Bế tắc có giới hạn tường minh** (RFC §5): sau vòng trước, Route vẫn sinh cùng một `Agent+Task`, tức hậu điều kiện định tuyến chưa thoả; 3 lần tư vấn Arbiter, 5 lần ngắt cứng tạm dừng. Checkpoint trung gian bên trong Worker không reset bộ đếm, Engine tất định không cho phép chạy không tải vô hạn.

**Thiết luật ba: phán định ngữ nghĩa đi Arbiter, mỗi lần phán định ghi xuống đĩa**. Chọn kiến trúc sư lúc khởi động, phân loại can thiệp người dùng, lối thoát cho thất bại/bế tắc đều do các hàm Decide theo từng tình huống trong `internal/arbiter` phán định: sự thật vào, quyết định có cấu trúc ra, kiểm tra cơ học đỡ lưng, audit decisions.jsonl (phát lại offline để hồi quy). Ba Worker giữ `CheckpointDeltaGuard` riêng (lan can sự thật: sản phẩm chưa ghi xuống đĩa thì không được kết thúc).

**Thiết luật bốn: cứng hóa ranh giới, không cứng hóa phán đoán ngữ nghĩa không vét cạn được**. Mã chỉ cố định bất biến chứng minh được (quyền, giai đoạn, thứ tự, idempotent, toàn vẹn cấu trúc) và cung cấp cho model sự thật đầy đủ cùng không gian thao tác đủ rộng; chọn lựa sáng tác, phán đoán chất lượng, kế hoạch thích ứng chính văn thế nào và các câu hỏi mở phải để lại cho Worker / Arbiter. Cấm dùng từ khóa, ngưỡng điểm, liệt kê độ lệch hay bảng luật thay cho sự hiểu của model, cũng cấm thu hẹp không gian quyết định hợp pháp của nó vì lo model sai. Trước khi thêm một luật mã mới phải chứng minh không gian quyết định đóng và kết quả kiểm cơ học được; nếu không thì nên cải thiện ngữ cảnh và khả năng biểu đạt của công cụ, để lợi ích từ việc model nâng cấp được thu về mà không phải đổi vỏ.

---

## 3. Toàn cảnh kiến trúc

```
[Entry: TUI / headless]
        │ prompt / steer
[Vỏ Host]
   ├── observer            chuyển tiếp tiến độ Worker + sự kiện Engine giao việc → chiếu ra UI/log
   ├── engine              vòng lặp tất định: LoadState → Route → kiểm tiên quyết → chạy Worker → ranh giới sentinel
   ├── đường can thiệp      Steer/Continue → Arbiter phán định → thực thi hành động (tức thời/commit ở ranh giới)
   └── usage / ngân sách / điểm dừng / quản lý model
        │ gọi subagent.Runner.Run theo kiểu lập trình (tiến độ qua chuyển tiếp ctx ToolProgress)
[architect_short/long · writer · editor] (mỗi cái run + context + model riêng)
        │ gọi công cụ
[Tools]  novel_context · read_chapter · plan_chapter · draft_chapter · edit_chapter
         check_consistency · commit_chapter · save_review · save_arc_summary
         save_volume_summary · save_foundation
        │ nguyên tử một file + phát lại idempotent (commit dùng Saga lưu trữ bền vững)
[Store: hệ thống file (tmp + rename)]
   Progress · Checkpoints · Outline · Drafts · Summaries · Characters · World
   · Signals · Decisions(audit phán định) · bể phản hồi · bản ghi vi phạm
```

| Tầng | Làm gì | Không làm gì |
|---|---|---|
| Entry | Hiển thị, nhận đầu vào | Quyết định nghiệp vụ |
| Host/Engine | Vòng đời, thực thi Route, chạy Worker, ranh giới sentinel, điều phối can thiệp | Phán đoán văn học; ghi sự thật sáng tác (hành động trạng thái điều khiển qua lõi công cụ) |
| Arbiter | Phán định ngữ nghĩa (quyết định có cấu trúc) | Tự sáng tác; thực thi hành động |
| Workers | Suy nghĩ, viết, thẩm duyệt | Đọc/ghi Store trực tiếp (bắt buộc qua công cụ) |
| Tools | IO nguyên tử một file + lỗi tường minh + idempotent; commit dùng Saga | Chỉ thị điều phối xuyên subagent |
| Store | Ghi xuống đĩa trên hệ thống file | Logic nghiệp vụ |

Phụ thuộc một chiều: `entry → host → agents/arbiter → tools → store → domain`; `flow` là package chiến lược thuần tầng trên (trên store, dưới host). Độc lập ngang: `errs/` được mọi tầng tham chiếu, `diag/` đăng ký luồng sự kiện host + chỉ đọc `store/`.

---

## 4. Mô hình dữ liệu

### 4.1 BookMetadata và Progress

`BookMetadata` là nguồn sự thật duy nhất của tên sách và giới thiệu hướng độc giả, lưu vào `meta/book.json`; `book.md` chỉ là bản chiếu dễ đọc. Premise không lưu lại tên sách, Progress cũng không gánh thông tin tác phẩm.

```go
type BookMetadata struct {
    Title    string
    Synopsis string
}
```

Progress (`internal/domain/runtime.go`) chỉ ghi trạng thái vận hành:

```go
type Progress struct {
    Phase             Phase           // init / premise / outline / writing / complete
    CurrentChapter    int
    TotalChapters     int
    CompletedChapters []int
    TotalWordCount    int
    ChapterWordCounts map[int]int
    InProgressChapter int             // chương đang viết
    Flow              FlowState       // writing / reviewing / rewriting / polishing / steering
    PendingRewrites   []int
    StrandHistory     []string        // chuỗi dominant_strand
    HookHistory       []string        // chuỗi hook_type
    CurrentVolume, CurrentArc int     // phân tầng truyện dài
    Layered           bool
}
```

Logic điều khiển chỉ đọc các trường sự thật trên, không phụ thuộc bất kỳ "dấu thời gian cập nhật" nào — thông tin thời gian do `OccurredAt` của checkpoint gánh.

RunMeta (`meta/run.json`) gánh **ý định vận hành của người dùng** (không phải sự thật sáng tác): PlanningTier, PlanStart (cố định phán định khởi động, căn cứ duy nhất để khôi phục khi sập ở giai đoạn quy hoạch), PendingSteer (bảo vệ sập cho can thiệp, ô một can thiệp đang trên đường), AdvanceMode / AdvancePermitChapter (chính sách nghiệm thu từng chương và giấy phép chương chính xác), AdvanceHold (tạm dừng một lần do can thiệp ký). `RunMeta.Init` giữ toàn bộ trường ý định qua các lần khởi động lại.

### 4.2 Checkpoint (`internal/domain/checkpoint.go`)

```go
type Scope      struct { Kind ScopeKind; Chapter, Volume, Arc int }
type Checkpoint struct {
    Seq        int64       // tự tăng đơn điệu
    Scope      Scope       // chapter / arc / volume / global
    Step       string      // plan / draft / commit / review / arc_summary / ...
    Artifact   string
    Digest     string
    OccurredAt time.Time
}
```

Lưu trữ: `meta/checkpoints.jsonl`, chỉ ghi nối thêm. Ghi lại cùng `Scope+Step+Digest` coi là idempotent, không sinh dòng mới.

### 4.3 Artifact và sự thật phụ

Artifact ở `store/outline.go` `drafts.go` `summaries.go` `characters.go` `world.go`.

- **Signals**: `PendingCommit` (khôi phục khi commit gián đoạn). Đọc lúc khởi động/khôi phục, không đọc lúc chạy.
- **Decisions** (`meta/decisions.jsonl`): bản ghi audit mỗi lần Arbiter phán định (facts+input+decision), phát lại offline được; **không phải nguồn dữ liệu khôi phục** (khôi phục chỉ dựa vào Progress/Checkpoint/RunMeta).
- **Sự thật thế giới dạng tăng trưởng**: dòng thời gian và thay đổi trạng thái nhân vật lần lượt nối thêm vào `timeline.jsonl`, `meta/state_changes.jsonl`; trong tiến trình duy trì chỉ mục khử trùng lặp, commit bình thường chỉ ghi phần gia tăng của chương này. Mảng JSON phiên bản cũ được di trú ở lần nối thêm tiếp theo theo giao thức idempotent "ghi log mới nguyên tử trước, xóa file cũ sau", `timeline.md` là bản chiếu dễ đọc có thể dựng lại.
- **Bể phản hồi đại cương** (`meta/outline_feedback.jsonl`): phản hồi thường của writer được tiêu thụ ở thao tác cấu trúc kế tiếp; nếu tu chỉnh chính văn từ bên ngoài ảnh hưởng cốt truyện thì ưu tiên giao cho architect trước khi viết tiếp, xử lý xong thì làm rỗng.
- **Bản ghi vi phạm cơ học** (`meta/rule_violations.jsonl`): kết quả kiểm theo user_rules khi commit, thẩm duyệt của editor tiêu thụ qua `novel_context(chapter=N)`; metadata chất lượng best-effort, không mạnh-đồng-bộ ngang hàng với commit.

### 4.4 Đại cương phân tầng và thu hẹp kết thúc sách (tập kết thúc)

Quy hoạch cuộn (mỏ neo compass + khung tập + cung mở theo nhu cầu) giải quyết "mở và cuộn", nhưng biến "khi nào kết thúc" từ một con số thành phán định mở ở cuối mỗi tập — việc thu hẹp kết thúc sách phải được thiết kế tường minh, nếu không sẽ xuất hiện hai loại bế tắc: viết hết sổ sách mà không thu được đuôi (vòng lặp viết tiếp vượt biên, đã được sửa bằng lan can cấu trúc) và viết hết tự sự mà sổ sách không cho dừng (estimated_scale ước cao + ngưỡng kết thúc phủ quyết cứng → châm nước hoặc ngắt cầu).

**Tập kết thúc là khái niệm hạng nhất của việc thu hẹp**, kết thúc sách = một lần phán định hướng + một đoạn trượt tất định:

- **Tuyên bố (Arbiter phán định ngữ nghĩa)**: kiến trúc sư cuối tập chọn một trong ba — append_volume (tiếp)/ append_volume kèm `"final": true` (tập kết thúc)/ complete_book (điều kiện hiện tại đã thoả hết). estimated_scale trong phán định kết thúc là **bằng chứng, không phải quyền phủ quyết**.
- **Thực thi (mã tra bảng sự thật)**: sự thật tập kết thúc = `domain.FinaleVolume`. Cấu trúc tập cuối viết xong (`layeredStructurallyComplete`) **và bộ ba thu kết cuối tập đầy đủ (thẩm duyệt cung/tóm tắt cung/tóm tắt tập)** thì tự động MarkComplete — kết thúc không cướp trước cổng chất lượng của editor. Sách chưa tuyên bố vẫn đi theo `layeredBookComplete` cấp chất lượng (phục bút + tuyến dài về 0).
- **Gỡ bỏ (suy ra từ dữ liệu, không có công cụ hoàn tác)**: sau khi tuyên bố mà lại thêm một tập mới không đánh dấu → trạng thái thu hẹp tự gỡ. Trạng thái luôn suy ra được từ layered_outline.
- **Việc giao phán định kết thúc**: cuối tập do nhánh 10 của Route giao architect_long đi theo danh sách tiêu chí kết thúc — quyền phán định kết thúc nằm ở kiến trúc sư (một Worker), không ở mặt điều khiển.

---

## 5. Quy ước công cụ

Công cụ là điểm tương tác duy nhất giữa tầng sự thật và Agent.

### 5.1 Công cụ đọc

`novel_context(scope)` / `read_chapter(n)` — gọi được bất cứ lúc nào, không phụ thuộc trạng thái tiên quyết, trả dữ liệu đủ để LLM quyết định độc lập. `novel_context(chapter=N)` tiêm thêm vi phạm cơ học của chương đó (nếu có); đường architect tiêm tóm tắt tập đã hoàn thành/tóm tắt cung của tập hiện tại, ảnh chụp nhân vật, bể phản hồi đại cương và trạng thái foundation. Khi mở rộng cung, nội dung đã diễn ra là sự thật, khung xương chỉ là kế hoạch; Architect có thể đồng thời tu chỉnh title/goal của cung mục tiêu trong `expand_arc` và mở rộng chương.

### 5.2 Công cụ ghi (nguyên tử một file + ngữ nghĩa khôi phục phân cấp)

Ghi một file là nguyên tử; bước xuyên file không cam kết tính nguyên tử kiểu cơ sở dữ liệu. Commit thường và commit làm lại của `commit_chapter` dùng chung `PendingCommit`, tiến theo "ý định đầy đủ → artifact/trạng thái → Progress → checkpoint → xóa ý định"; khôi phục chỉ dùng payload chuẩn hóa ghi xuống đĩa lần đầu cùng ảnh chụp chính văn, cấm dùng tham số model sinh lại sau khởi động lại hoặc draft đã bị ghi đè. Các thao tác cấu trúc như `expand_arc` / `append_volume` không có ý định lưu trữ bền vững, chỉ cam kết phát lại idempotent cùng tham số, sửa bản chiếu dẫn xuất và trả lỗi tường minh.

| Công cụ | Artifact | Step |
|---|---|---|
| `save_book` | meta/book.json + book.md | book |
| `plan_chapter` | drafts/chXX.plan.json | plan |
| `draft_chapter` | drafts/chXX.draft.md | draft |
| `edit_chapter` | drafts/chXX.draft.md | edit |
| `check_consistency` | không (chỉ đọc, trả nội tuyến) | consistency_check |
| `commit_chapter` | chapters/chXX.md + Progress (+ bể phản hồi/bản ghi vi phạm best-effort) | commit |
| `save_review` | reviews/chXX.json (global là chXX-global.json) | review |
| `save_arc_summary` | summaries/arc-vNNaNN.json | arc_summary |
| `save_volume_summary` | summaries/vol-vNN.json | volume_summary |
| `save_foundation` | foundation/*.json (expand_arc/append_volume/update_compass thành công là tiêu thụ bể phản hồi) | premise / outline / layered_outline / characters / world_rules / expand_arc / append_volume / update_compass / complete_book |

`commit_chapter` gánh việc phát hiện hoàn thành cung/tập/toàn sách, trả sự thật có cấu trúc; `save_review` không phán định ngưỡng văn học, chỉ kiểm sự thật thẩm duyệt và ánh xạ nguyên tử verdict Editor đưa ra thành Flow và hàng đợi làm lại.

`edit_chapter` là lớp bọc mỏng của `agentcore.EditTool`, chỉ cho sửa chương đã hoàn thành và nằm trong `PendingRewrites`; bản thảo sơ khởi chương mới phải ghi đè cả chương qua `draft_chapter(mode="write")`.

### 5.3 Phân tầng lỗi

| Loại lỗi | Tầng xử lý | Hành động |
|---|---|---|
| Timeout mạng / EOF streaming | Tools | Thử lại 3 lần |
| provider 429/503 | litellm | failover sang provider dự phòng |
| Xác thực / model không tồn tại | Tools | terminal, ném lên |
| Thiếu artifact tiên quyết | Tools | conflict, ném lên, LLM gọi `novel_context` rồi thử lại |
| Tham số công cụ không hợp lệ | Tools | validation, ném lên, LLM sửa tham số |
| retryable (stream-idle v.v.) | tầng subagent | MaxRetries=7 thử lại tại chỗ, không ra khỏi Worker |
| Worker thất bại (guard nâng mức/hard_stop v.v.) | Engine | lỗi tất định tạm dừng ngay; còn lại thử lại cùng chỉ thị một lần → Arbiter phán định retry/reroute/abort |
| Bế tắc (cùng một chỉ thị định tuyến lặp lại) | Engine | 3 lần tư vấn Arbiter, 5 lần ngắt cứng tạm dừng |
| Phản hồi streaming rỗng / suy nghĩ dài | litellm (`StreamIdleTimeout=5min`) | watchdog kích hoạt thử lại |

### 5.4 Idempotent

Mỗi công cụ ghi kiểm checkpoint trước khi thực thi: nếu `Step+Digest` của checkpoint mới nhất trong scope hiện tại giống lần này thì trả thẳng sản phẩm sẵn có. Thử lại và giao việc trùng sau khôi phục sập đều an toàn — đây cũng là nền tảng để mô hình khôi phục của Engine (đọc store chạy tiếp) đứng vững.

---

## 6. Lắp ghép Worker

> Một Prompt khổng lồ duy nhất + một Agent chạy hết cuốn sách về lý thuyết là khả thi, nhưng ba thứ chặn tính ổn định: **bùng nổ ngữ cảnh** (200 chương thì nén giỏi đến đâu cũng thoái hóa), **nhiễu trách nhiệm** (quy hoạch nghiêm ngặt / viết tưởng tượng / thẩm duyệt phê phán pha loãng nhau trong cùng một prompt), **mất lợi ích model dị thể** (quy hoạch/viết/thẩm duyệt chọn model độc lập là không gian tối ưu chi phí/chất lượng đáng kể). Topology nhiều Worker vì thế là cần thiết.

### 6.1 Lắp ghép và vận hành

`agents.BuildWorkers` (`internal/agents/build.go`) lắp ba loại Worker thành một `subagent.Runner`: Engine gọi thẳng `Run(agent, task)`, mỗi lần gọi là một `agentcore.AgentLoop` đầy đủ (context độc lập, model độc lập, thử lại độc lập). Toàn bộ lắp ghép có hiệu lực một lần: model theo vai trò + failover, prompt cache key (#seq tự tăng mỗi lần spawn), ThinkingLevel, UsageRecorder/SessionLogger (OnMessage), Writer ContextManagerFactory (cửa sổ tự dựng lại khi /model đổi), RestorePack, StopGuardFactory, StopAfterTools.

Chuyển tiếp tiến độ Worker đi qua **callback ToolProgress của ctx**: Engine gọi `Runner.Run` với `agentcore.WithToolProgress(ctx, relay)`, sự kiện công cụ/chính văn streaming/thinking/retry/context của subagent đi qua relay vào observer — cùng hình thái ProgressPayload như thời Coordinator, tầng quan sát dùng lại được.

```
Engine ── Runner.Run(agent, task) ──▶ architect_short/long · writer · editor
                                          │ gọi công cụ
                                        Store (phương tiện cộng tác, Worker không giao tiếp trực tiếp)
```

`bootstrap.ModelSet` hỗ trợ model theo vai trò: architect/writer/editor mỗi cái cấu hình độc lập + failover provider. Writer chạy Sonnet thay vì Opus trên truyện dài 200 chương tiết kiệm được một bậc chi phí. Arbiter dùng thống nhất model Default (tính phí qua usageTrackedModel), hiện không mở cấu hình vai trò riêng.

### 6.2 Ba mô thức cộng tác

Worker không giao tiếp trực tiếp, mọi thông tin chảy qua sản phẩm có cấu trúc trong Store:

**Mô thức A · bàn giao tuần tự (trục chính)**: Route giao Architect quy hoạch → Writer chương 1..N → Editor thẩm duyệt cuối cung → Writer viết lại. "Bước tiếp giao cho ai" ở mỗi bước do Route suy từ sự thật.

**Mô thức B · vòng khép phản hồi**: Writer báo độ lệch đại cương trong commit → bể phản hồi ghi xuống đĩa (chỉ sách phân tầng) → thao tác cấu trúc kế tiếp của Architect tham chiếu qua novel_context → thao tác thành công là tiêu thụ và làm rỗng. Writer không gọi thẳng Architect, phản hồi chảy qua tầng sự thật.

**Mô thức C · mở rộng khung xương (quy hoạch cuộn)**: sau commit, sự thật cho thấy cung kế vẫn là khung xương → Route (hoặc precheck của Engine) giao architect_long mở rộng → Writer viết tiếp. Năng lực "quy hoạch cuộn" của truyện dài chính là vòng khép này.

### 6.3 Ràng buộc mã cho luồng Worker (không dựa vào nạng prompt)

> Luồng writer ban đầu dựa vào ràng buộc "nghiêm ngặt tiến theo thứ tự sau" trong `writer.md`. LLM thường vi phạm — bỏ qua plan viết draft luôn, chỉ viết chính văn vào chat mà không ghi xuống đĩa. **Ràng buộc luồng bằng prompt không ổn định**, model nâng cấp còn có thể khiến nó "không tuân theo một cách sáng tạo".

Bốn tầng ràng buộc mã (có hiệu lực đồng thời):

| Tầng | Điểm neo | Tác dụng |
|---|---|---|
| `StopAfterTools` / `StopAfterToolResult` | `agents/build.go` SubAgentConfig | Công cụ then chốt thành công là thoát run Worker (thoát ở trạng thái cuối vẫn hỏi StopGuard, xem bài kiểm contract). Writer `commit_chapter` trúng là dừng; `save_review`/`save_arc_summary`/`save_volume_summary` của Editor, thu kết cung/tập của Architect đi `StopAfterToolResult` |
| `CheckpointDeltaGuard` | `agents/guard/subagent_guards.go` | Lấy checkpoint đường cơ sở làm ranh giới, trước khi kết thúc lượt này phải thấy checkpoint step tương ứng, nếu không từ chối `end_turn`; chặn liên tiếp 3 lần thì nâng mức terminate (lưới đỡ cho model yếu lặp vô hạn). Guard của Editor nhận biết nhiệm vụ: được giao sinh tóm tắt thì chỉ phúc tra không tính là hoàn thành |
| `next_step` nội tuyến trong công cụ | trường giá trị trả về của từng công cụ | Mỗi sự thật tự mang "gợi ý bước tiếp", LLM thấy sự thật là biết bước sau |
| Kiểm tra thuộc về/tiên quyết trong công cụ | `edit_chapter` `commit_chapter` v.v. | Chặn vật lý ở tầng dữ liệu: sửa tại chỗ bản thảo sơ khởi, sửa chương đã hoàn thành chưa vào hàng đợi, commit rỗng đều bị từ chối, `ConcurrencySafe=false` chặn tranh chấp đồng thời |

writer.md chỉ gánh: giao thức thực thi, mô hình nhận thức chạy tiếp từ điểm dừng, diễn giải contract chương; tiêu chuẩn viết nằm ở tầng văn phong (placeholder `{{VOICE}}` điền lại, người dùng ghi đè được, xem `docs/voice-layer.md`). **Đây chính là tiền đề để tầng văn phong dám mở cho người dùng: bất biến nằm ở tầng công cụ, prompt sửa hỏng thế nào cũng không làm hỏng máy trạng thái.**

### 6.4 Phụ thuộc agentcore

`../agentcore` là thư viện Agent tổng quát tự có của dự án này (liên kết qua go.work). Nguyên thủy Engine dùng: `subagent.Runner.Run` (gọi thẳng theo kiểu lập trình, kết quả và chuỗi lỗi có kiểu — phân loại như `errors.Is(err, subagent.ErrUnknownAgent)` không phụ thuộc văn bản lỗi), ctx `ToolProgress` (chuyển tiếp sự kiện), `subagent.Config`, `StopGuard`/`StopAfterTools`. `subagent.Tool` chỉ để host nào cần đưa Runner cho model dùng qua `Runner.AsTool()`, AINovel không đi qua tầng này.

**Ranh giới sửa đổi**: được vào agentcore — chiến lược ContextManager mới, adapter provider mới, loại sự kiện mới; không vào agentcore — model nghiệp vụ và công cụ nghiệp vụ. Tiêu chí phán đoán: giả sử agentcore tương lai sẽ được coding agent / agent chăm sóc khách hàng dùng, năng lực mới vẫn có ý nghĩa trong kịch bản đó mới cho vào. **Cấm viết bản vá đỡ ở tầng ứng dụng** — thiếu năng lực thì sửa thẳng thượng nguồn.

**Bài kiểm contract** (`internal/agents/agentcore_contract_test.go`, 6 mục, đều được dẫn qua `Runner.Run`): đóng đinh hành vi khung mà dự án này phụ thuộc thành khẳng định chạy được (thoát trạng thái cuối hỏi StopGuard, Error/Aborted không chạm guard, chuỗi lỗi Escalate khớp được bằng `errors.Is`, `ErrUnknownAgent` có kiểu của `Run`, tiến độ lỗi công cụ đầy đủ và là văn bản thuần). **Trước khi bump agentcore bắt buộc phải xanh hết** — chú thích sẽ lỗi thời, test thì không (kỷ luật này đã từng bắt được một giả định hết hiệu lực và tiết kiệm một workaround).

### 6.5 Cache prompt

Đòn bẩy thứ hai của chi phí chạy dài (thứ nhất là chọn model). Bản giảng giải đầy đủ xem `docs/prompt-cache-design.md`. Phân công ba tầng: **litellm chỉ dịch giao thức**, **agentcore quyết định vị trí và định danh cache**, **ainovel tích hợp bằng một dòng cấu hình**.

Tiền đề của lợi ích cache là **byte tiền tố yêu cầu ổn định**, do ba kỷ luật đảm bảo (đều ở agentcore):

1. **Tools tất định byte** — Description/Schema dựng lại mỗi lần, mọi vòng lặp map đều sắp xếp trước
2. **Lịch sử append-only** — tin nhắn chỉ nối thêm không viết lại; nén ngữ cảnh là giao dịch tường minh kiểu "trả một lần miss sạch để đổi cửa sổ", bản chiếu bắt buộc `CommitOnProject`
3. **Nội dung động vào đuôi** — phong bì/chỉ thị đều nối ở đuôi, không bao giờ ghi ngược vào tin nhắn cũ

Cấu hình theo "một sách một gốc, một vai một tên, một phiên một key": họ OpenAI `PromptCacheKey = nvl-<hash sách>-<vai>#<số thứ tự spawn>` để gắn ái lực định tuyến (mặc định chỉ gửi cho endpoint chính thức, trung chuyển có thể bật tường minh); họ Claude `CacheLastMessage: "ephemeral"` điểm ngắt cuộn + điểm ngắt sàn system. **Đường đỏ kiểu chốt cửa**: mọi lượng đi vào key cache, lần tính đầu trong phiên là đóng băng, thà cũ còn hơn phá cache. Phát hiện đứt chuỗi (`host/usage.go noteCacheBreak`) thuần quan sát không sửa, số đếm vào `usage.json cache_breaks` và bảng cache của TUI.

---

## 7. Engine và Arbiter

### 7.1 Vòng lặp Engine (`internal/host/engine.go`)

```
for {
    áp dụng hành động trạng thái điều khiển của can thiệp (làm rỗng; hold+dispatch thì lập sự thật làm lại trước)
    advanceGate.HandleBoundary() // tiêu thụ hold + đối chiếu giấy phép review
    inst := giao việc theo can thiệp ?? Route(LoadState) ?? planStartFallback
    inst == nil → return          // kết thúc sách / dừng ngữ nghĩa, chờ Continue
    precheck(inst)                // hóa thân tất định của ToolGate cũ: giai đoạn kết thúc thì hủy giao việc;
                                  // chương mục tiêu của writer chưa mở rộng → đổi sang architect mở rộng
    advanceGate.Allow(inst)       // chỉ chặn chương mới thuận chiều chưa có giấy phép
    trackDeadlock(inst)           // cùng Agent+Task lặp liên tiếp: 3 lần hỏi Arbiter, 5 lần ngắt cầu
    runWorker(inst)               // subagent.Runner.Run + chuyển tiếp tiến độ + sự kiện DISPATCH
    phân loại lỗi: lỗi tất định → tạm dừng; thất bại đầu thử lại một lần; thất bại lại → Arbiter (retry/reroute/abort)
    ranh giới chính sách: budget → advanceGate
}
```

Một goroutine tuần tự; cancel `ctx` = tạm dừng (checkpoint đảm bảo không mất). **Trạng thái điều khiển chỉ đổi ở ranh giới vòng lặp**: hold/reopen/dispatch của can thiệp xếp hàng tới ranh giới để commit (tổ hợp hold+dispatch thì thực thi giao việc lập hàng đợi trước, rồi mới cho Gate tiêu thụ hold); answer/rules thực thi tức thời. Chế độ `review` chỉ ràng buộc chương mới thuận chiều, không chặn làm lại, thẩm duyệt, bảo trì cấu trúc và khôi phục commit. Trước khi thực thi việc Arbiter giao thì đối chiếu Expect (các trường ngữ nghĩa Phase/Flow/QueueHead; CheckpointSeq chỉ audit không đối chiếu — lúc can thiệp worker phần lớn đang chạy, seq ắt đổi), không khớp thì hủy và gửi can thiệp nguyên gốc **đồng bộ** trở lại đường phán định đầy đủ để hỏi lại.

### 7.2 Arbiter (`internal/arbiter/`)

Bốn tình huống, mỗi tình huống một cặp `Collect*Facts` (ranh giới IO)/ `Decide*` (không IO ngoài yêu cầu model do bộ thực thi chung quản, phát lại offline được) + kiểu Decision riêng (hành động không khớp tình huống không biểu diễn được ở tầng kiểu):

| Tình huống | Kích hoạt | Kiểu quyết định |
|---|---|---|
| `plan_start` | sách mới khởi động | chọn kiến trúc sư short/long + mở rộng nhu cầu quá ngắn |
| `intervention` | người dùng can thiệp | tổ hợp answer/rules/hold/reopen/dispatch (thứ tự thực thi do Engine cố định) |
| `worker_failure` | Worker báo lỗi và phân loại tất định không có lối ra | retry / reroute / abort |
| `deadlock` | cùng chỉ thị lặp lại không tiến triển | retry / reroute / abort |

Đường thất bại: bộ thực thi có cấu trúc chung chọn JSON Schema gốc hay contract prompt theo năng lực; lỗi định dạng/Schema ở chế độ prompt và lỗi kiểm nghiệp vụ ở cả hai chế độ đều phản hồi lý do chính xác cho model sửa tiếp, cho tới khi thành công hoặc `context` kết thúc, không đặt giới hạn số lần. Vi phạm contract gốc, từ chối trả lời, cắt cụt, kết thúc lỗi và lỗi yêu cầu không thử lại được đều trả về tường minh ngay; can thiệp không sinh ghi nào, khởi động báo lỗi tường minh, failure/deadlock tạm dừng thận trọng. **Đầu ra Arbiter cũng bất khả tín như mọi đầu ra LLM** — sau khi kiểm JSON Schema, `Validate` tiếp tục kiểm cơ học theo sự thật (ràng buộc phase, reopen chỉ giới hạn ở kết thúc sách, chương vượt biên). Lượng dùng qua `usageTrackedModel` vào hệ thống ngân sách và usage.

### 7.3 Vỏ Host (`internal/host/host.go`)

Vòng đời (`StartPrepared`/`Resume`/`Continue`/`Steer`/`Abort`/`Close`), điều phối can thiệp (tuần tự FIFO + bảo vệ sập PendingSteer), chiếu sự kiện, quản lý model. Kênh quan sát `Events`/`Stream`/`Done`, UI tổng hợp `Snapshot()`, cửa vào mở rộng (nhập/xuất/đồng sáng tác/mô phỏng/chuyển model).

`runEnded` (callback engine.onDone) định trạng thái cuối theo sự thật store: Phase=Complete → completed + tổng kết kết thúc sách tất định (không tốn lệnh gọi LLM); còn lại → idle/paused. **Cấm mọi logic "tự chạy tiếp" xuất hiện ở đây** (bài học lịch sử §10 mục 5).

---

## 8. Khởi động, khôi phục và can thiệp

### 8.1 Tạo mới

```
Người dùng: "yêu cầu một câu"
  → StartPrepared(raw)
    → Progress.Init / Checkpoints.Reset
    → StartPrompt cố định vào RunMeta (sự thật đầu vào ghi xuống đĩa trước khi phán định)
    → Arbiter plan_start phán định (chọn kiến trúc sư + mở rộng nhu cầu) → thất bại thì báo lỗi tường minh (audit kèm error)
    → PlanStartRecord cố định vào RunMeta (phán định ghi sự thật trước, khởi động thực thi sau)
    → engine.start(chỉ thị giao việc đầu tiên)
```

Phán định thất bại không phải bế tắc: StartPrompt đã ở đó, mọi lần khôi phục/tiếp tục sau đều do engine quyết tại chỗ (xem §8.2).

### 8.2 Khôi phục (khởi động lại sau sập)

```
tiến trình khởi động → resumeLabel (nhãn UI thuần) → cảnh báo nhất quán → AdvanceGate đối chiếu
  → PendingSteer tồn tại → đi đồng bộ qua đường phán định can thiệp (can thiệp có hiệu lực trước khi chạy tiếp) rồi mới dựng engine
  → nếu không: engine.start(nil): chỉ khôi phục sự thật, Route tính lại từ store và chạy tiếp
```

Không có phiên nào cần khôi phục. Sập ở giai đoạn quy hoạch (phán định đã ghi xuống đĩa, foundation đầu tiên chưa ghi) do `planStartFallback` tiếp tục giao việc theo PlanStartRecord, không làm lại phán định sẵn có. Nếu phán định khởi động **chưa từng hoàn tất** (model hỏng lúc khởi động), `planStartFallback` quyết tại chỗ dựa vào StartPrompt — đây là lần thử lại của phán định đầu, không vi phạm "khôi phục không phán định lại"; quyết tại chỗ thất bại thì tạm dừng tường minh và thông báo, không cho phép dừng máy im lặng. Giao việc trùng lặp an toàn nhờ idempotent của công cụ (§5.4).

### 8.3 Can thiệp người dùng

`Steer`/`Continue` thống nhất đi đường phán định Arbiter (`doIntervention`):

```
lưu PendingSteer (bảo vệ sập) → Collect facts → Decide (cấp giây)
  → ghi decisions.jsonl → answer hiển thị / rules ghi xuống đĩa tức thời
  → hold/reopen/dispatch vào hàng đợi commit ở ranh giới (khi engine dừng thì thực thi ngay và dựng engine theo ý định)
  → mọi hành động thành công → xóa PendingSteer nguyên tử (ClearHandledSteer)
```

Bảo vệ sập là **lưu trữ bền vững best-effort cho một can thiệp đang trên đường**: lần `SetPendingSteer` đầu thất bại sẽ báo lỗi tường minh và dừng phán định, tuyệt đối không tiếp tục thực thi khi không có bản ghi khôi phục; trong lúc phán định, hành động thất bại (giữ để phát lại), thoát bình thường/Abort (defer lưu lại việc giao việc còn sót) đều được bảo vệ. Vẫn có hai cửa sổ dứt khoát không bảo đảm — giao việc chuyển vào hàng đợi thực thi trong bộ nhớ rồi bị kill cứng (cấp mili giây), và đầu vào đồng thời đang chờ interMu. Người dùng có mặt thì cảm nhận được, gửi lại chỉ tốn vài giây.

**Tầng lưu trữ của can thiệp dài hạn**: quy tắc văn phong/chất lượng viết do hành động `rules` đã phán định chuẩn hóa qua `userrules.Service` vào ảnh chụp quy tắc của cuốn sách, `novel_context` tiêm `working_memory.user_rules` — có hiệu lực xuyên nén, xuyên khởi động lại (chi tiết xem [ảnh chụp quy tắc người dùng](user-rules-runtime.md)). Các lối thoát khác vốn đã rơi vào store (dung lượng/tình tiết → giao architect, sửa chương cũ → editor vào hàng đợi PendingRewrites, làm lại sau kết thúc → reopen).

### 8.4 Điều khiển đẩy chương

`ChapterAdvanceGate` thống nhất thực thi hai ý định người dùng ở hai thang thời gian khác nhau:

| Ý định | Nguồn | Ngữ nghĩa |
|---|---|---|
| `AdvanceMode=review` + permit chính xác | `/review on`, `/next` | chính sách bền vững: mỗi chương mới thuận chiều phải được cho qua riêng |
| `AdvanceHold` | Arbiter intervention | ý định một lần: tạm dừng ở ranh giới hiện tại, khi làm lại cạn, hoặc sau khi chương mục tiêu commit ổn định |

Giấy phép gắn với số chương. Chỉ được tiêu thụ khi chương mục tiêu vào CompletedChapters, PendingCommit rỗng và checkpoint commit tồn tại, nên sập ở bất kỳ cửa sổ nào của saga commit cũng không dùng cùng giấy phép cho chương kế tiếp. Bất biến chi tiết xem [Chapter Advance Gate](chapter-advance-gate.md).

---

## 9. Cấu trúc thư mục

```
internal/
  domain/         dữ liệu thuần: Phase / FlowState / Progress / Checkpoint / Scope / Story / Plan /
                  Review / StateChange / luật chuyển Phase-Flow
  store/          lưu trữ trên hệ thống file (tmp+rename + điều phối idempotent; commit có sự thật giai đoạn Saga): progress / checkpoints / outline /
                  drafts / summaries / characters / world / signals / run_meta / runtime /
                  session / decisions (audit phán định)
  tools/          11 công cụ Agent, công cụ ghi nguyên tử một file + lỗi tường minh + idempotent; commit dùng thêm Saga lưu trữ bền vững
  flow/           chiến lược định tuyến (hàm thuần + ranh giới IO): router.go (bảng quyết định Route) + state.go (LoadState)
                  + pause.go (phán định điểm dừng)
  arbiter/        tầng phán định ngữ nghĩa (LLM-as-function): plan_start / intervention / failure(deadlock)
                  cặp hàm Collect/Decide theo tình huống + kiểu Decision theo tình huống + kiểm cơ học
  agents/         build.go lắp ba Worker (subagent.Runner, Engine gọi thẳng theo kiểu lập trình); ctxpack/ chiến lược nén ngữ cảnh Writer
    guard/        subagent_guards.go (CheckpointDeltaGuard ×3, lan can sự thật Worker)
  host/           host.go (vòng đời/điều phối can thiệp) + engine.go (vòng lặp thực thi tất định) + observer*.go
                  + events.go + usage*.go + budget.go + advance_gate.go + resume.go + cocreate.go
    imp/          biên dịch ngữ nghĩa nhập tiểu thuyết bên ngoài: ingest → segment → analyze → synthesize → publish (suy trạng thái thuần + LLM như hàm)
    exp/          xuất chương đã hoàn thành: TXT / EPUB 3; thuần chỉ đọc
  entry/          tui (Bubble Tea) / headless / startup
  bootstrap/      config + ModelSet + failover provider + trình hướng dẫn thiết lập
  eval/           đánh giá offline (A/B prompt/voice, hồi quy)
  diag/ errs/ models/ notify/ rules/ userrules/ stylestat/ ...

assets/
  prompts/        arbiter-plan-start / arbiter-intervention / arbiter-failure / architect-short|long
                  / writer (mẫu giao thức, placeholder {{VOICE}}) / editor / import-* / simulation-*
  voice.md        tiêu chuẩn viết (mặc định tích hợp của tầng văn phong; ba tầng ghi đè xem docs/voice-layer.md)
  references/     thủ pháp viết + anti-ai-tone + mẫu theo thể loại v.v.
  styles/         mặc định/kỳ ảo/ngôn tình/trinh thám (người dùng ghi đè/thêm mới được)

../agentcore      khung Agent tổng quát (thư mục anh em qua go.work, thêm được năng lực chung, không thêm nghiệp vụ)
../litellm       gateway LLM
```

### 9.1 Cột mốc tiến hóa

| Thời điểm | Tái cấu trúc | Hiệu quả ròng |
|---|---|---|
| 2026-04-10 | `internal/orchestrator/` (6342 dòng) → `host/` + `agents/` | lõi runtime -74% |
| 2026-04-20 | Coordinator Hybrid: tạo mới `host/flow/`, thu định tuyến về hàm thuần | tỷ lệ lỗi định tuyến tiến về 0 |
| 2026-05-02 | agentcore sửa suy nghĩ chậm/streaming; xóa bản vá chạy tiếp `idleResumeCount` | mimo / suy nghĩ chậm chạy được streaming |
| 2026-06-05 | vòng khép quy hoạch cuộn + `/import` suy ngược viết tiếp | lần đầu chạy thông 200+ chương |
| 2026-07-12 | **thay mặt điều khiển Engine + Arbiter**: vòng lặp dài Coordinator và hệ sinh thái bảy bản vá về hưu; tầng văn phong ba tầng ghi đè; năm vòng phản biện đối kháng gia cố | mỗi ranh giới tiết kiệm một lần chuyển tiếp LLM; mặt điều khiển 100% kiểm offline được; phán định ngữ nghĩa phát lại được |
| 2026-07-15 | **đường ống biên dịch ngữ nghĩa `/import`**: luật phân tách cứng về hưu, đổi thành biên dịch theo giai đoạn ingest→segment→analyze→synthesize→publish; suy trạng thái thuần (`NextAction(Facts)`) + sản phẩm ràng buộc theo vân tay đầu vào, toàn tuyến khôi phục idempotent được | việc phân tách tăng tự nhiên theo năng lực model; không có liệt kê giai đoạn trôi dạt; gián đoạn tiếp được, mặt điều khiển kiểm offline được |

Thực đo: hy3-preview free 12 chương / 73 phút, mimo-v2.5-pro 10 chương / 8.4 vạn chữ, đều chạy trọn một lần; truyện dài gpt-5.4 `Phàm Cốt` (tên gốc tiếng Trung) 235 chương / 127 vạn chữ chạy thông vòng khép quy hoạch cuộn (dữ liệu thời Coordinator, lần chạy đầu thời Engine còn chờ bổ sung).

---

## 10. Những gì dứt khoát không làm

Vi phạm là kiến trúc lệch hướng.

1. **Không đưa vào khái niệm Task / Job / WorkItem**. "Nhiệm vụ hiện tại" UI hiển thị là chiếu của luồng sự kiện, không phải sự thật.
2. **Không phát minh bộ điều phối thứ hai ngoài Route**. Mọi "bước sau giao cho ai" phải qua bảng quyết định Route (đóng đinh bằng đặc tả vét cạn) hoặc Arbiter phán định (audit ghi xuống đĩa), không cho phép if-else giao việc rải rác.
3. **Không làm cơ chế "chạy tiếp khi rảnh"**. Vòng lặp Engine kết thúc = Host vào trạng thái cuối; muốn động lại chỉ có người dùng `Continue` hoặc khởi động lại `Resume`.
4. **Không thêm răn dạy hành vi vào prompt**. Cần lan can hành vi tức là phân tầng sai — bất biến vào tiền điều kiện công cụ, phán đoán vào Arbiter, luồng vào Route.
5. **Không thêm bản vá tự chạy tiếp cho dừng máy bất thường ở Host**. `idleResumeCount` xưa trong lần chạy dài duy nhất thật sự kích hoạt thì 100% không cứu được gì, mà còn che mất nguyên nhân thật ở tầng agentcore (chi tiết xem `feedback_no_host_resilience.md`).
6. **Không suy đoán hoàn thành nhiệm vụ dựa trên "tool exec end"**. Bằng chứng duy nhất của hoàn thành là checkpoint ghi xuống đĩa.
7. **Không làm mô hình bốn tầng kiểu WorkflowInstance / Command + Apply**. Tầng sự thật chỉ có Progress + Checkpoint + Artifact.
8. **Không hỗ trợ Worker song song**. Một vòng lặp Engine hoạt động đơn độc, một cuốn sách đẩy tuần tự. Nhiều tiểu thuyết thì dùng nhiều tiến trình.
9. **Không gọi LLM ở tầng công cụ** (trừ chính công cụ Agent). Thuần IO + kiểm chứng + idempotent.
10. **Không để UI đọc Store trực tiếp**. Chỉ được đăng ký sự kiện hoặc đọc `Snapshot()` của Host.
11. **Không viết máy trạng thái Flow ở phía Host**. Nhãn Flow chỉ do công cụ cập nhật, Route chỉ đọc không ghi.
12. **Không viết cứng đỡ lưng cho "ảo giác LLM"**. Hãy tối ưu prompt, cải thiện giá trị trả về của công cụ, để novel_context trình bày sự thật rõ hơn.
13. **Không để diag / tầng quan sát xen vào luồng điều khiển**. Chẩn đoán chỉ đọc; tự sửa / chạy tiếp / đổi luồng đều dứt khoát không làm.
14. **Chính sách ngân sách và đẩy chương không vào Route/tầng công cụ**. `BudgetSentinel` / `ChapterAdvanceGate` là thành phần chính sách ở ranh giới Engine (thực thi chỉ thị người dùng đã ký trước, không đánh giá hành vi văn học); `notify` thuần quan sát.
15. **Đổi mặt điều khiển phải sửa đặc tả vét cạn trước, sửa hiện thực sau**; **trước khi bump agentcore phải qua bài kiểm contract**.
16. **Không làm DSL luồng công việc tổng quát, event sourcing, State Digest toàn cục**. Route là một lĩnh vực một bảng, tổng quát hóa tức là thiết kế quá mức.

---

## 11. Chiến lược kiểm chứng

### 11.1 Danh sách tài sản kiểm tra

| Tầng | Tài sản | Bao phủ |
|---|---|---|
| Đặc tả mặt điều khiển | `flow/router_exhaustive_test.go` | bảng quyết định Route 12 vạn tổ hợp vét cạn + tính chất hàm thuần/tất định/bảo toàn |
| Contract khung | `agents/agentcore_contract_test.go` | 5 giả định hành vi agentcore, dẫn qua `Runner.Run` (bắt buộc chạy trước khi nâng cấp) |
| Đầu-cuối Engine | `host/engine_test.go` | model giả + công cụ thật: viết trọn sách / phán định thất bại / phán định bế tắc / thời điểm nghiệm thu làm lại / dừng ngay khi có boundary hold / bảo toàn tranh chấp lúc thoát / một giấy phép một chương |
| Phán định | `arbiter/arbiter_test.go` | phân tích/thử lại phản hồi/ma trận kiểm theo tình huống/thu thập sự thật |
| Contract đường ống sự thật | test store/tools | bể phản hồi xuyên khởi động lại, bản ghi vi phạm latest-wins/xóa khi viết lại/tiêm vào novel_context, PlanStart giữ qua Init |
| Tầng văn phong | `assets/load_test.go` | tách giống từng byte / ngữ nghĩa ba tầng ghi đè / eval cùng đường lắp ghép |
| Chất lượng ngữ nghĩa | `internal/eval` + decisions.jsonl | A/B prompt/voice, phát lại offline phán định (bộ hồi quy đang xây) |

### 11.2 Kịch bản ổn định

- **A chạy dài**: 80~200 chương chạy trọn một lần, Phase=complete. Cho phép failover provider, thử lại; cấm mọi tự chạy tiếp.
- **B khôi phục sau sập**: kill tiến trình sau bất kỳ bước nào → Resume → Route chạy tiếp từ sự thật, không viết lại sản phẩm đã ghi xuống đĩa, checkpoints không lặp bước. Sập giai đoạn quy hoạch đi theo PlanStartRecord.
- **C dao động provider**: 503 gián đoạn → litellm failover, Worker không cảm nhận.
- **D can thiệp người dùng**: Steer lúc chạy → phán định hiển thị cấp giây, hành động commit ở ranh giới; Steer khi dừng → phán định xong thì dựng engine theo ý định; sập → PendingSteer phát lại.

### 11.3 Tuân thủ (viết thành linter / test được)

- `flow.Route` phải là hàm thuần: cấm đọc Store / mọi IO
- Trong thân hàm `runEnded` không được xuất hiện lời gọi khởi động engine
- Tình huống phán định mới phải thêm cặp Collect/Decide + kiểu Decision + ghi xuống đĩa
- Mã liên quan khôi phục chỉ được xuất hiện ở `host/resume.go` và `engine.planStartFallback`

### 11.4 Lặp chất lượng

Đổi văn phong → sửa `<thư mục sách>/style/` (cấp người dùng) hoặc assets/voice.md (tích hợp), bộ đánh giá văn phong A/B kiểm chứng; thêm chiều thẩm duyệt → sửa editor.md (save_review nhận có cấu trúc); thêm tài liệu tham khảo → ba chỗ nối dây tường minh (`tools.References` + `loadReferences` + ánh xạ tiêm của novel_context).

**Thống kê văn phong cấp toàn sách (`internal/stylestat`)**: Host tạo `StyleStatsIndex` duy nhất cho mỗi cuốn sách, và tiêm tường minh vào `novel_context` cùng `commit_chapter`. Lần khởi động đầu khôi phục chỉ mục từ toàn bộ chương đã hoàn thành, sau đó cập nhật gia tăng cho chương thêm mới/viết lại (mẫu câu/ cụm từ tần suất cao/ câu lặp xuyên chương/ hình thái cuối chương), ở cùng trạng thái sách thì dùng lại ảnh chụp và tiêm `episodic_memory.style_stats`: editor phán định theo con số, writer dựa đó tự tránh. Eval offline vẫn gọi thẳng hàm thuần `Compute`. **Thống kê thuộc mã, phán định thuộc LLM**.

---

## 12. Tổng kết

> **Tầng sự thật tất định, tầng ngữ nghĩa tự chủ.** Model tự do ở chỗ không thể kiểm chứng (viết gì, viết thế nào, phán thế nào), bị ràng buộc ở chỗ kiểm chứng được (thứ tự, idempotent, giai đoạn).

Không có task queue, không có policy engine, không có phiên thường trú. Chỉ có:

- Một vòng lặp Engine tất định tuần tự (~500 dòng, đóng đinh bằng sáu đường đầu-cuối)
- Một bảng quyết định Route (hàm thuần, đặc tả vét cạn 12 vạn tổ hợp)
- Bốn hàm phán định Arbiter (sự thật vào, quyết định có cấu trúc ra, ghi xuống đĩa phát lại được)
- Ba loại Worker chức năng (context và model độc lập, lan can sự thật không quấy rầy)
- 11 công cụ nguyên tử một file, thất bại tường minh xuyên file/khôi phục idempotent; trong đó commit dùng Saga lưu trữ bền vững + một file checkpoint jsonl

Lợi ích từ việc model nâng cấp chảy về đâu thì rõ ràng: sáng tác tốt hơn (toàn bộ đầu ra của Writer/Architect/Editor), phán định chính xác hơn (bốn tình huống Arbiter), tóm tắt tốt hơn (ctxpack) — đổi model là được hết, vỏ không đổi một dòng. Mặt điều khiển không ăn lợi ích model, vì **tra bảng không cần trí tuệ**; nó cần được chứng minh đúng, và nó đã được chứng minh.

Độ cứng của luồng là cố ý, có định giá, và có cửa mở: muốn nới thứ tự công cụ của writer → nới một đoạn prompt giao thức (bất biến ở tầng công cụ đỡ lưng); muốn giao việc theo cung → Route thêm một nhánh; muốn mở rộng năng lực phán định → thêm một cặp Collect/Decide. Mỗi lần nới đều có trọng tài (đặc tả vét cạn, đánh giá văn phong, phát lại decisions) — **dùng bằng chứng để quyết định cho model bao nhiêu dây, chứ không dùng đức tin**.

Kỷ luật duy nhất: **khi ai đó muốn thêm một điểm quyết định, trước tiên phải qua phép chia ba — vét cạn được thì vào Route, biên rõ ràng thì vào Arbiter, mở thì vào Worker**. Quyết định không thuộc cả ba, hãy nghĩ lại xem nó có thật sự tồn tại không.