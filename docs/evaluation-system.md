# Hệ thống đánh giá của ainovel-cli

> Đánh giá không phải dựng thêm một bộ script kiểm tra mới, mà là lấy **bộ chẩn đoán sự thật sẵn có của dự án (`diag`), bộ thống kê văn phong toàn sách (`stylestat`), và thẩm duyệt bảy chiều nguyên bản (`ReviewEntry`) làm bộ đánh giá**, rồi bọc thêm một tầng harness chạy hàng loạt offline. Một định nghĩa sự thật, hai chỗ không còn trôi dạt.

---

## 0. Vì sao cần thiết kế lại

Tính ổn định đã chạy thông: truyện dài 235 chương / 127 vạn chữ viết trọn một lần, vòng khép quy hoạch cuộn thành lập (xem `architecture.md` §9.1). Nút cổ chai đã chuyển dịch — **chất lượng lặp được**:

- Sau khi sửa một prompt, luồng có còn ổn định không? Chuỗi công cụ, tiến triển trạng thái, sự thật lưu trữ có còn đúng không?
- Chất lượng chính văn, đại cương, thẩm duyệt thật sự nâng lên, hay chỉ là lần này rút thăm được kết quả tốt?
- Trong truyện dài, nhân vật, dòng thời gian, phục bút, ngữ cảnh có liên tục đáng tin không?
- **Sự cố định văn phong ở cấp toàn sách** (tic câu trung bình vài chục lần mỗi chương, hình thái cuối chương đồng dạng, thuật lại từng chữ xuyên chương) có tốt lên hay xấu đi? Đây là hung thủ thật của điểm 6.5/10 trong thực chứng 196 chương, mà thẩm duyệt đơn chương mù tự nhiên với nó.

Hiện những phán đoán này dựa vào "cảm giác + đọc mẫu thủ công". Hệ thống đánh giá phải biến việc sửa prompt từ cảm tính thành một quy trình kỹ thuật **có hồi quy, có bằng chứng, có đọc mẫu thủ công**.

Nhưng dự án này không cần, cũng không nên bê nguyên nền tảng eval phổ thông của ngành (dataset / experiment / scorer / cơ sở dữ liệu / Web UI). Lý do đơn giản: **lõi của những năng lực đó — kiểm tra tất định và tín hiệu chất lượng — đã tồn tại trong dự án, viết bằng Go, và chia sẻ cùng một mô hình sự thật với runtime.**

---

## 1. Luận điểm cốt lõi: bộ đánh giá đã tồn tại

Bốn loại bộ đánh giá của hệ thống đánh giá, ba loại đã được hiện thực trong codebase, chỉ chưa từng được gọi với tư cách "bộ đánh giá":

| Bộ đánh giá | Năng lực sẵn có của dự án | Cửa vào | Đầu ra |
|---|---|---|---|
| **Chẩn đoán sự thật tất định** | một nhóm luật sản phẩm + luật runtime trong `internal/diag` | `diag.Diagnose(store)` | `Report{Stats, Findings}`, Finding kèm Severity/Evidence |
| **Hồi quy văn phong cấp toàn sách** | `internal/stylestat` | `stylestat.Compute(input)` | trung bình mẫu câu mỗi chương, câu lặp xuyên chương, tỷ lệ câu ngắn cuối chương, trộn tiêu đề |
| **Phán định chất lượng (rubric)** | rubric có phiên bản (ban đầu dẫn xuất từ bảy chiều của `editor.md`) | LLM Judge (thước cố định làm A/B) | consistency/character/pacing/continuity/foreshadow/hook/aesthetic |
| **Xuất khử nhạy cảm hành vi** | phần xuất của `internal/diag` | `diag.WriteExport(store, rep, rc)` | bộ khung hành vi, để đọc mẫu thủ công và lưu trữ |

`diag.Analyze(s *store.Store)` nhận một Store là sinh ra `Report` đầy đủ — **vốn đã chạy offline được trên mọi thư mục đầu ra**. `stylestat.Compute` là hàm thuần. Nghĩa là hệ thống đánh giá không phải hiện thực lại "chương có ghi xuống đĩa không, progress có tiến triển không, checkpoint có tồn tại không, có tàn dư pending không, luồng có vòng lặp vô hạn không" — diag làm hết rồi, và mỗi luật đều ứng với một cái hố thật đã giẫm (`PhaseFlowMismatch`, `OrphanedSteer`, `OutlineExhausted`, `repeatedErrors`/`stuckStep` ứng với các sự cố lịch sử như idleResume / livelock cạn đại cương / gọi công cụ in ra như văn bản).

> **Việc của hệ thống đánh giá không phải tạo ra kiểm tra, mà là: chạy hàng loạt + cho bộ đánh giá sẵn có chạy trên sản phẩm + ánh xạ Finding/thống kê thành cổng + tổng hợp báo cáo.**

---

## 2. Nguyên tắc thiết kế

### 2.1 Bộ đánh giá tức bộ chẩn đoán, tuyệt đối không tái tạo kiểm tra tất định

Kiểm tra tất định chỉ gọi `diag.Diagnose`, không phân tích lại `progress.json` / `checkpoints.jsonl` / `sessions/*.jsonl` ở tầng đánh giá. Lý do là thiết luật DRY của dự án này: **"thế nào là trạng thái hợp lệ" chỉ được có một định nghĩa.** Nếu đánh giá dùng Python phân tích lại checkpoint để phán commit có thiếu không, là có hai định nghĩa "commit hoàn thành", runtime sửa luật diag mà đánh giá không theo thì cổng lập tức sai lệch.

→ Harness đánh giá dùng **Go**, gọi `diag` và `stylestat` in-process, chia sẻ `internal/domain` và `internal/store` với runtime. Đây là khác biệt căn bản nhất giữa thiết kế này và bản trước.

### 2.2 Hồi quy văn phong cấp toàn sách là tín hiệu chất lượng số một

LLM Judge đơn chương thấy chương nào cũng "bình thường", nhưng nút cổ chai lại đúng là sự cố định xuyên chương. Nên **xương sống tất định của hồi quy chất lượng là `stylestat`, không phải LLM Judge**.

**Tiền đề: `stylestat.Compute` dưới 5 chương trả nil thẳng** (`stylestat.go` `minChapters=5`, mẫu quá nhỏ thì tần suất vô nghĩa). Vì thế hồi quy văn phong **chỉ có hiệu lực ở tầng Quality / Longform từ ≥5 chương**, Smoke 1 chương không lấy được tín hiệu văn phong — điều này quyết định chi phí và chính sách mặc định ở dưới. Chỉ số gồm:

- số lần mẫu câu trung bình mỗi chương của variant so với baseline (`patterns[].per_chapter`)
- tỷ lệ kết thúc bằng câu ngắn cuối chương (`ending.short_ratio` tiến gần 1 là bệnh)
- số câu lặp từng chữ xuyên chương (`repeated_sentences`)
- trộn định dạng tiêu đề (`title_formats`)
- tỷ lệ từ chỉ thời gian ở mở đầu (`opening_time_rate`)

Đây là những chỉ số chi phí LLM bằng không, tất định, và đánh trúng nút cổ chai chất lượng. **LLM Judge là bổ sung, delta stylestat là trục chính.**

### 2.3 LLM Judge căn theo rubric bảy chiều nguyên bản, không dựng lại từ đầu

Judge không phát minh chiều chấm điểm mới — chiều đúng bằng bảy mục của `domain.DimensionScore`, làm so sánh baseline/variant.

**Nhưng rubric phải có phiên bản, cố định được**, lưu thành ảnh chụp `evals/rubrics/*.json`, không đọc `editor.md` lúc chạy. Lý do: khi đối tượng bị đo đúng là `editor.md`, nếu trọng tài thay đổi cùng `editor.md` thì chuẩn đánh giá trôi mất — trọng tài và bị đo cùng nguồn sẽ khiến "sửa editor là tốt hay xấu" không phán được. Nên rubric ban đầu **dẫn xuất** từ bảy chiều của editor (bảo đảm khẩu độ nhất quán), sau đó **tiến hóa độc lập, bump phiên bản tường minh**; báo cáo ghi rõ dùng phiên bản rubric nào.

### 2.4 Finding tất định quyết cổng, LLM và con người chỉ phán định chất lượng

Căn theo thiết luật kiến trúc "thống kê thuộc mã, phán định thuộc LLM":

- **Chỉ bằng chứng tất định mới chặn được việc hợp nhất**: Finding `SevCritical` của `diag`, khẳng định contract case khai báo thất bại.
- **LLM Judge và đọc mẫu thủ công sinh cảnh báo cùng đầu mối sắp xếp**, không tự quyết hợp nhất.
- Một câu: `Finding.Severity` ánh xạ thẳng thành cấp cổng, không đưa vào phân loại mức nghiêm trọng mới.

### 2.5 Đánh giá chỉ quan sát, không xen vào luồng điều khiển

Đánh giá dùng lại `diag`, nhưng **bỏ `Action` và `Planner` của diag** — đó là thứ của luồng điều khiển lúc chạy. Trong ngữ cảnh đánh giá, `diag.Report` chỉ lấy `Stats` và `Findings`, Action bỏ hết. Đánh giá không tự sửa prompt, không tự rollback, không chạy tiếp. Đây là phần mở rộng của kỷ luật quan sát viên (`architecture.md` §2.3) trong ngữ cảnh đánh giá.

### 2.6 Thất bại lộ ra tường minh

Không mock thành công, không nuốt lỗi, không dùng mẫu giả để qua. Model, công cụ, cấu hình, hệ thống file, phân tích, judge — bất kỳ cái nào thất bại đều ghi rõ lý do trong báo cáo. **Bản thân thất bại cũng là kết quả đánh giá** — một case chạy sập thì cổng là FAIL, không phải "bỏ qua".

### 2.7 Mỗi lần chỉ kiểm chứng một biến

Ràng buộc cứng của A/B: cùng nhu cầu, cùng cấu hình, cùng model/provider, cùng style, thư mục đầu ra cách ly. Baseline = prompt chính thức hiện tại, Variant = chỉ thay file prompt cần kiểm chứng lần này. Một thí nghiệm đừng đổi đồng thời Writer/Architect/Editor/Arbiter.

---

## 3. Toàn cảnh kiến trúc

```text
[Cases]  evals/cases/*.json —— tập khẳng định tầng sự thật, không phải dòng dataset phổ thông
   │
[Runner]  internal/eval —— lắp host in-process để dẫn dắt (dừng theo giới hạn số chương), ghi đè trong bộ nhớ bundle.Prompts để làm variant
   │       chạy baseline ┐
   │       chạy variant  ┘  mỗi cái cách ly thư mục output
   ▼
[Collectors]  thu thập trên mỗi thư mục đầu ra:
   ├── diag.Diagnose(store)      → Report{Stats, Findings}      (sự thật + runtime)
   ├── stylestat.Compute(input)  → thống kê văn phong toàn sách   (xương sống hồi quy chất lượng)
   ├── khẳng định contract case  → checkpoint/phase/contract công cụ mong đợi (thứ diag không phủ)
   ├── usage / cost / token      → đọc từ meta/usage.json
   └── tool_calls                → đọc lệnh gọi công cụ thật từ meta/sessions/*.jsonl
   ▼
[Graders]
   ├── cổng tất định: Finding.Severity + khẳng định contract → hard_fail / regression
   ├── delta stylestat: chênh chỉ số văn phong variant vs baseline
   ├── LLM Judge (tùy chọn): so sánh A/B theo rubric bảy chiều
   └── Human: đọc sản phẩm baseline/variant thủ công
   ▼
[Report]  report.json (máy đọc) + report.md (người đọc) + xuất khử nhạy cảm hành vi
   └── Gate: PASS / WARN / FAIL
```

Hướng phụ thuộc: `eval → host → agents → tools → store → domain`, dùng lại ngang `diag` / `stylestat`. Tầng đánh giá **không phụ thuộc ngược** vào luồng điều khiển lúc chạy, chỉ đọc Store và bộ đánh giá chỉ đọc.

> **Hiện thực hiện tại phủ trục tất định**: không có `--variant` thì `mode=single`; truyền `--variant` thì `mode=ab`, cùng case chạy cách ly baseline và variant, sinh delta. Collectors đã nối `diag.Diagnose`, contract case, `stylestat.Compute`, `meta/usage.json`, đếm tool call trong session; Graders đã nối cổng tất định, delta diag baseline/variant, delta cost/token/tool call, delta stylestat. Runner lắp trực tiếp bằng `host.New` và tự có dừng theo giới hạn số chương, **không dùng lại `headless.Run` không có giới hạn số chương**. LLM Judge và Human vẫn là tầng tùy chọn về sau, không tham gia cổng tất định hiện tại.

---

## 4. Vì sao là Go in-process, không phải shell + Python

| Chiều | shell copy mã + Python phân tích (đường cũ) | Go in-process (thiết kế này) |
|---|---|---|
| Kiểm tra tất định | Python phân tích lại JSON, hai định nghĩa với luật diag | gọi thẳng `diag.Diagnose(store)`, một định nghĩa |
| Chuyển variant | copy cả cây mã + `go build` lại hai binary | `bundle.OverridePrompt(...)` ghi đè trong bộ nhớ rồi lắp host, zero copy zero biên dịch lại |
| Hồi quy văn phong | phải viết lại logic tách câu tiếng Trung của stylestat trong Python | gọi thẳng `stylestat.Compute` |
| Rubric Judge | chiều rải rác trong Python | dùng lại `domain.DimensionScore`, cùng nguồn với online |
| Rủi ro trôi dạt | cao: runtime đổi mô hình sự thật, đánh giá không theo | thấp: đổi trường sẽ lộ ngay lúc biên dịch |

`prompt_ab.sh` cũ phải copy mã rồi biên dịch lại là vì prompt được nhúng vào binary (`go:embed`). Nhưng `assets.Bundle.Prompts` là struct thường, **runner sửa một trường trong bộ nhớ là làm được variant**, hoàn toàn không cần copy mã. Đây là đơn giản hóa lớn nhất có được khi viết harness bằng Go.

> **Ràng buộc hiện thực**: `assets.Load` qua `loadPrompts` gắn thống nhất hậu tố `WithSimulationGuidance` cho prompt Worker (architect/writer/editor). Nếu variant chỉ nhét văn bản trần vào `bundle.Prompts.Writer` thì mất hậu tố hồ sơ mô phỏng mà baseline có, A/B không tương đương.
>
> Cách đúng là ghi đè qua `assets.OverridePrompt`, bên trong đi đúng bọc giống `Load`; eval không sao chép logic bọc.

> Bản tài liệu trước giữ `prompt_ab.sh` / `prompt_ab_report.py` và "trích xuất năng lực dần". Thiết kế này bỏ đường đó: vấn đề chúng giải quyết (chạy cách ly + tổng hợp chỉ số) là tập con trong harness Go in-process, dùng lại gượng thì còn cõng keo giao diện ba ngôn ngữ shell/Python/Go. **Harness Go là đường chính duy nhất**; harness Go hiện đã phủ chạy cách ly baseline/variant, tổng hợp repeat và delta tất định. Các script cũ (`scripts/prompt_ab.sh`, `scripts/prompt_ab_report.py`) cùng sổ tay `docs/prompt-ab.md` đã bị xóa khi thiết kế này lên sóng, không giữ lại.

---

## 5. Case Manifest

Case là đơn vị nhỏ nhất của đầu vào đánh giá, và cũng là một nhóm **khẳng định tầng sự thật**. Mô tả bằng JSON, tránh luật rải rác trong tham số dòng lệnh.

```json
{
  "id": "writer_first_chapter_xianxia",
  "category": "smoke",
  "role": "writer",
  "description": "Kiểm chứng chất lượng chính văn chương 1 của Writer và tính ổn định chuỗi công cụ",
  "prompt": "Viết một truyện tu tiên dài kỳ, nhân vật chính từ tạp dịch biên thành khởi đầu, nhờ ký ức dị thường phá án cũ của tông môn rồi cuốn vào cục diện trường sinh.",
  "style": "fantasy",
  "max_chapters": 1,
  "target_prompts": ["writer.md"],
  "rubric": "writer_chapter",

  "expect": {
    "phase": "writing",
    "min_completed_chapters": 1,
    "required_checkpoints": ["chapter:1:plan", "chapter:1:draft", "chapter:1:commit"],
    "no_pending": ["pending_commit", "pending_steer"]
  },

  "gate": {
    "max_severity": "warning",
    "max_cost_delta_ratio": 0.3,
    "max_tool_call_delta_ratio": 0.3,
    "stylestat_regression": "warn"
  }
}
```

**Ngữ nghĩa trường**:

- `expect`: khẳng định contract ở cấp case, **chỉ khai báo kỳ vọng mà luật chung của diag không phủ được và gắn chặt với case này** (ví dụ "case smoke này bắt buộc sinh đúng chapter:1:commit"). Cái chung như "không tàn dư pending / phase-flow nhất quán / không hụt chương" giao cho diag, không khai lại trong case.
- `category`: tầng đánh giá ∈ `smoke` / `workflow` / `quality` / `longform` / `recovery` / `steering`. Quyết định chạy bộ cổng nào và mặc định có bật stylestat/Judge không.
- `role`: vai bị đo ∈ `writer` / `architect` / `editor`. Trực giao với `category` — tầng quyết "kiểm tới độ sâu nào", vai quyết "kiểm Worker nào". Tầng Workflow chọn tập khẳng định theo `role`.
- `max_severity`: mức nghiêm trọng cao nhất diag Finding được phép có. Vượt là hard fail.
- `gate.max_cost_delta_ratio` / `gate.max_tool_call_delta_ratio`: ngưỡng mức tăng chi phí và lệnh gọi công cụ của variant so với baseline; bỏ trống mặc định `0.3`, `0` tường minh nghĩa là không cho phép tăng, số âm nghĩa là tắt cổng delta đó.
- `rubric`: bật bảng chấm LLM Judge có phiên bản nào. Bỏ trống thì không chạy Judge.
- `gate.stylestat_regression`: `block` / `warn` / `off`, điều khiển hồi quy văn phong có chặn không (chỉ có hiệu lực với case ≥5 chương).

---

## 6. Phân tầng đánh giá

Mỗi tầng nói rõ **dùng bộ đánh giá sẵn có nào**, tránh "tầng đánh giá lại tự viết một lượt phán đoán".

### 6.1 Smoke (bắt buộc chạy mỗi lần đổi prompt, tập tối thiểu)

Chỉ phán hệ thống còn chạy ổn định không, không phán văn. 1 chương / giai đoạn quy hoạch là đủ lộ.

| case | mục tiêu | bộ đánh giá chính |
|---|---|---|
| `writer_first_chapter` | Writer hoàn thành chương 1 và commit | `expect.required_checkpoints` + diag |
| `architect_short` | quy hoạch truyện ngắn lưu đủ premise/outline/characters/world_rules | kiểm foundation cùng nguồn `MissingSummaries` của diag + `expect` |
| `architect_long` | quy hoạch truyện dài lưu layered_outline/compass, cung đầu mở rộng | `OutlineExhausted`/`CompassDrift` của diag + `expect` |
| `editor_review` | tới điểm thẩm duyệt thì Editor lưu review (đủ bảy chiều) | khẳng định trường `ReviewEntry` |

Chi phí: 1 chương × baseline+variant, cấp giây tới cấp phút, không bật Judge, không chạy stylestat (số chương dưới 5, `Compute` trả nil). CI mặc định chỉ chạy tầng này.

### 6.2 Workflow (kiểm chứng hành vi Agent khớp contract kiến trúc)

**Kỷ luật then chốt: khẳng định contract, không khẳng định chuỗi công cụ chính xác.** Kiến trúc đặt cược vào việc LLM tự chủ quyết định luồng (`architecture.md` §2.1), đóng đinh thứ tự công cụ sẽ tái đưa vào tầng đánh giá cái "viết cứng cho hành vi LLM" mà §10.13 đã từ chối. Nên ở đây chỉ khẳng định **sự thật tất yếu**:

- Writer: checkpoint `chapter:N:commit` tồn tại; sau commit subagent kết thúc lượt này (không có chính văn đuôi quá dài); checkpoint draft trước commit. **Không** khẳng định "phải theo đúng thứ tự novel_context→read_chapter→plan→draft→check→commit".
- Architect: trong giai đoạn viết outline chỉ tăng không ghi đè toàn bộ (checkpoint của `expand_arc`/`append_volume`, không có lần ghi toàn bộ `layered_outline` thứ hai); sau khi mở rộng, số chương của outline phẳng và layered khớp nhau.
- Editor: `ReviewEntry.Verdict` hợp lệ (accept/polish/rewrite); rewrite/polish phải sinh affected chapters; cuối cung có checkpoint `arc_summary`, cuối tập có `volume_summary`.
- Engine giao việc: chỉ thị Route khớp Worker thật sự chạy (đọc từ session trace, `repeatedErrors` của diag đỡ lưng vòng lặp); phán định ngữ nghĩa đối chiếu `meta/decisions.jsonl`.

Phần lớn những cái này được luật diag + khẳng định checkpoint phủ trực tiếp, một số ít (chính văn đuôi sau commit) cần thêm một kiểm tra trace nhẹ trong collector.

### 6.3 Quality (chạy sau khi luồng đã qua, đánh giá chất lượng nội dung)

Hai chân:

1. **Delta stylestat (tất định, trục chính)**: chênh chỉ số văn phong variant vs baseline. Đây là bằng chứng cứng của hồi quy chất lượng. **Yêu cầu case chạy đủ ≥5 chương** (nếu không `Compute` trả nil, mục này đánh `insufficient_sample`), nên case Quality thuần 1 chương không lấy được hồi quy văn phong, phải đặt `max_chapters` từ 5 trở lên.
2. **LLM Judge (bổ trợ)**: rubric bảy chiều A/B (xem §8).

Chỉ case đã qua §6.1/§6.2 mới vào Quality — luồng còn sai thì bàn chất lượng vô nghĩa.

### 6.4 Longform & Recovery (thay đổi lớn / nightly)

Không cần chạy mỗi lần. Bao phủ tính ổn định truyện dài và năng lực khôi phục, đúng sân nhà của luật runtime và luật context của diag:

- viết liên tục 3 chương / 5 chương đầu → `GhostCharacter`/`TimelineGaps`/`RelationshipStagnation`/`ChapterGaps` của diag + lặp xuyên chương của stylestat.
- thẩm duyệt cuối cung + mở rộng cung kế → `OutlineExhausted`/`StaleForeshadow`/`CompassDrift`.
- người dùng can thiệp giữa chừng (case steering) → user_rules có rơi vào `meta/user_rules.json` không, các chương sau có tuân không.
- khôi phục sau sập: chạy tới draft chương N rồi kill → Resume → diag xác nhận `checkpoints.jsonl` không lặp bước, không viết lại draft đã ghi xuống đĩa, `pending_commit` cuối cùng về 0.
- phình lệnh gọi công cụ / chi phí bất thường → `repeatedErrors`/`stuckStep`/`streamIdleStorm` của diag + delta usage.

---

## 7. Cổng tất định

Cấp cổng dẫn xuất trực tiếp từ **Severity của diag Finding** + **khẳng định contract của case**, không lập phân loại mới.

### 7.1 Hard Fail (chặn hợp nhất)

- tiến trình panic / headless trả error.
- diag sinh Finding `SevCritical` (`InvalidPendingRewrites` / `PhaseFlowMismatch` v.v.).
- khẳng định contract `expect` của case thất bại: thiếu checkpoint commit, phase chưa đạt kỳ vọng, pending đã khai báo chưa về 0.
- số lỗi / số Finding critical của variant nhiều hơn baseline (hồi quy sang xấu hơn).

### 7.2 Regression (mặc định warning, chặn hay không do gate của case quyết)

- diag thêm Finding `SevWarning` (variant nhiều hơn baseline).
- mức tăng tool calls / cost / input token / output token vượt ngưỡng case (mặc định 30%).
- **hồi quy stylestat**: số lần mẫu câu trung bình mỗi chương tăng, tỷ lệ câu ngắn cuối chương tăng, câu lặp xuyên chương nhiều lên, trộn tiêu đề xuất hiện — theo `gate.stylestat_regression` quyết warn/block.
- số chữ chương dưới 60% hoặc trên 180% baseline (ngưỡng cùng nguồn `WordCountAnomaly` của diag).

### 7.3 Quality Gate (lưới đỡ thủ công)

- LLM Judge chỉ làm bổ trợ và sắp xếp.
- Judge phán variant rõ ràng kém hơn → bắt buộc đọc mẫu thủ công xác nhận.
- đọc mẫu thủ công xác nhận thoái hóa → chặn.
- Judge phán variant tốt hơn nhưng tất định có hard fail → vẫn chặn.

### 7.4 Điều kiện hợp nhất khuyến nghị

Sửa prompt thường ngày: Smoke toàn qua + Workflow của vai mục tiêu toàn qua (Smoke 1 chương không gồm hồi quy văn phong; nếu có chạy case Quality ≥5 chương thì stylestat không thoái hóa rõ rệt).
Thay đổi lớn: thêm 2-3 case Quality + 1-2 case Longform + đọc mẫu thủ công.

---

## 8. LLM Judge

Judge là bổ trợ chất lượng, bản chất là **dùng rubric có phiên bản (ban đầu dẫn xuất từ bảy chiều editor.md) so sánh baseline/variant offline**. Rubric là thước cố định, tiến hóa độc lập với `editor.md` online (lý do xem §2.3), báo cáo ghi phiên bản rubric đã dùng.

### 8.1 Đầu vào (kiểm soát kích thước, tuyệt đối không nhét cả cuốn sách)

- yêu cầu gốc của người dùng + đại cương/contract chương hiện tại.
- chính văn **cùng một chương** của baseline và variant.
- tóm tắt 1-2 chương gần nhất + tóm tắt trạng thái nhân vật (đọc từ store).
- lát cắt stylestat liên quan của chương đó (để Judge thấy sự thật kiểu "câu này lặp 7 lần trong toàn sách").

### 8.2 Đầu ra (có cấu trúc, căn bảy chiều)

```json
{
  "scores": {
    "consistency": 8, "character": 7, "pacing": 8, "continuity": 8,
    "foreshadow": 7, "hook": 7, "aesthetic": 6
  },
  "winner": "variant",
  "confidence": "medium",
  "reasons": ["variant đẩy hành động tập trung hơn", "baseline thuật lại tình tiết cũ nhiều hơn"],
  "risks": ["variant dựng nền động cơ nhân vật phụ hơi ít"]
}
```

- chiều đúng bằng bảy mục của `domain.DimensionScore`, mỗi mục 0-10.
- `winner` ∈ baseline/variant/tie; `confidence` ∈ low/medium/high.
- mỗi dòng `reasons`/`risks` ≤ 80 chữ, trích nguyên văn phải ngắn.

### 8.3 Ranh giới

Judge **không được**: quyết luồng có qua không, sửa sản phẩm, tự sửa prompt, làm căn cứ hợp nhất duy nhất, sinh trích đoạn chính văn dài.
Judge **được**: sắp xếp cho người thẩm định, đánh dấu thoái hóa rõ ràng, tổng kết khác biệt A/B, phơi tác dụng phụ của thay đổi prompt.

---

## 9. Báo cáo

Mỗi thí nghiệm sinh `report.json` (máy đọc, dựng lại markdown được) + `report.md` (người đọc) + `artifacts/{case_id}/{baseline,variant}/` (sản phẩm gốc). Khi `--repeat N` thì đường dẫn là `artifacts/{case_id}/rN/{baseline,variant}/`.

### 9.1 Delta chỉ số

Báo cáo hiển thị khác biệt của variant so với baseline, giá trị tuyệt đối và tỷ lệ song song:

```text
completed: baseline=5 variant=5   ← từ 5 chương, chỉ số văn phong mới có nghĩa
tool_calls: baseline=12 variant=16  +4 (+33.3%)
cost_usd: baseline=0.42 variant=0.55  +0.13 (+31.0%)
output_tokens: baseline=8200 variant=9100  +900 (+11.0%)
critical_findings: baseline=0 variant=0
warning_findings: baseline=1 variant=2  +1
stylestat.pattern_top_per_chapter: baseline=3.1 variant=5.4  +2.3   ← hồi quy văn phong
stylestat.ending_short_ratio: baseline=0.42 variant=0.71  +0.29     ← cuối chương đồng dạng nặng thêm
```

### 9.2 Tổng hợp Repeat

`--repeat N` không chỉ xem lần cuối, hiện thực hiện tại hiển thị tỷ lệ qua, số hard fail, số warning, min/avg/max của cost/tool_calls. Sau khi nối Judge sẽ thêm phân bố winner, tránh trộn nhiễu trọng tài model vào báo cáo tất định mặc định.

```text
writer_first_chapter_xianxia repeat=3
- pass_rate: 3/3
- cost_usd: avg=0.41 min=0.38 max=0.44
- tool_calls: avg=13 min=12 max=15
- stylestat.pattern_top_per_chapter: avg delta=+0.4 (không thoái hóa rõ rệt)
```

### 9.3 Báo cáo tối thiểu khả dụng

```text
Gate: FAIL

Hard Fail:
- writer_first_chapter_xianxia: missing checkpoint chapter:1:commit

Warnings:
- writer_dialogue_density: tool_calls +35%
- writer_anti_ai_tone: ending_short_ratio +0.28 (hồi quy văn phong)

Quality:
- writer_anti_ai_tone: judge prefers variant, confidence=medium

Artifacts:
- workspace/evals/20260629-120000/report.json
```

---

## 10. Cấu trúc thư mục và lệnh

```text
internal/eval/
  case.go        cấu trúc Case manifest + nạp
  eval.go        điều phối CLI: single / A/B / repeat
  runner.go      lắp host để dẫn dắt (dừng theo giới hạn số chương + drain tới Done), ghi đè trong bộ nhớ bundle.OverridePrompt
  collect.go     chạy diag.Diagnose + stylestat.Compute + usage/tool_calls + khẳng định contract trên thư mục đầu ra
  grade.go       ánh xạ Finding→cổng + delta baseline/variant + quyết định cổng stylestat
  report.go      report.json + report.md

cmd/ainovel-cli  cửa vào subcommand eval

evals/
  cases/         smoke/ workflow/ quality/ longform/ recovery/ steering/
  rubrics/       writer_chapter.json / architect_outline.json / editor_review.json
  variants/      writer-anti-ai-tone/writer.md v.v. (mỗi thư mục chỉ để prompt cần thay)
  reports/       lưu trữ báo cáo lịch sử
```

Lệnh:

```bash
# chạy hàng loạt nhiều case (CI mặc định chỉ chạy smoke, không bật judge)
ainovel-cli eval --cases evals/cases/smoke \
  --variant evals/variants/writer-anti-ai-tone \
  --out workspace/evals/writer-anti-ai-tone --ci
```

**Tham số đã hiện thực đợt này**: `--cases` (thư mục hoặc một manifest), `--variant` (thư mục prompt biến thể; truyền vào là tự chạy A/B baseline+variant), `--repeat N` (mỗi case chạy lặp N lần), `--config`, `--out`, `--max-chapters N` (ghi đè mặc định của case), `--timeout` (giới hạn thời gian tường mỗi case), `--ci` (chặn đầu ra từng sự kiện; mã thoát khác 0 tức hard fail, không truyền cũng có hiệu lực).

**Đang quy hoạch (chưa hiện thực, đừng dùng trên dòng lệnh, nếu không sẽ báo flag chưa định nghĩa)**: `--judge`/`--no-judge` (LLM Judge giai đoạn 3). Thay đổi prompt lớn hiện có thể dùng trước A/B tất định + repeat:

```bash
# thay đổi prompt lớn: A/B + repeat giảm ngẫu nhiên
ainovel-cli eval --cases evals/cases/quality \
  --variant evals/variants/writer-anti-ai-tone \
  --repeat 3 --ci
```

---

## 11. Những gì dứt khoát không làm

Vi phạm là đánh giá lệch định vị.

1. **Không sao chép logic chẩn đoán chung của diag ở tầng đánh giá** — phán đoán chung (tàn dư pending, phase/flow nhất quán, hụt chương, vòng lặp vô hạn) đều đi `diag`, phán đoán sự thật chỉ có một định nghĩa. Khẳng định contract cấp case (`expect.required_checkpoints` v.v.) được phép đọc thẳng `store`/API checkpoint, nhưng chỉ làm **khẳng định mỏng** — kiểm kỳ vọng cụ thể gắn chặt với case này, tuyệt đối không viết lại một lượt luật chung diag đã có.
2. **Không hiện thực lại luật tất định** — diag đã có một nhóm luật sản phẩm + luật runtime. Thiếu luật thì thêm vào diag, tầng đánh giá chỉ tiêu thụ.
3. **Không viết lại logic văn phong tiếng Trung của stylestat trong Python** — gọi thẳng package Go.
4. **Không để LLM Judge quyết luồng có qua không** — cổng chỉ nhận bằng chứng tất định.
5. **Không để đánh giá xen vào luồng điều khiển** — bỏ Action/Planner của diag, không tự sửa prompt, không rollback, không chạy tiếp, không phát hành.
6. **Không khẳng định chuỗi lệnh gọi công cụ chính xác** — chỉ khẳng định contract (commit xảy ra, checkpoint tồn tại), bảo vệ đặt cược "LLM dẫn dắt luồng".
7. **Không đưa vào cơ sở dữ liệu / Web UI / nền tảng đánh giá online** — giai đoạn hiện tại cần là hồi quy cục bộ lặp được, lên sóng được, chi phí thấp.
8. **Không copy mã biên dịch lại để làm variant** — ghi đè trong bộ nhớ `bundle.Prompts`.
9. **Không mock thành công, không nuốt lỗi** — mọi khâu thất bại ghi rõ, case chạy sập là FAIL.
10. **Case không đổi theo prompt liên tục** — case là tập kiểm tra ổn định; sửa case để variant qua là gian lận.

---

## 12. Lên sóng theo giai đoạn

### Giai đoạn 1 · Runner + cổng tất định (MVP, chứng minh giả định trước)

- `internal/eval`: cấu trúc Case + runner (headless in-process + ghi đè bundle) + collect (gọi `diag.Diagnose`) + grade (Finding→cổng + contract `expect`).
- `evals/cases/smoke/` đặt 3-4 case.
- Báo cáo ra `report.json` + markdown tối thiểu trước.

**Nghiệm thu**: một lệnh chạy hết smoke; Writer bỏ qua commit, tàn dư pending, thiếu checkpoint, phase không khớp **đều bị cổng chặn được** (những cái này diag vốn tra được, kiểm chứng là harness nối đúng).

### Giai đoạn 2 · A/B + repeat + hồi quy stylestat (đã hiện thực)

- `--variant` tự chạy baseline và variant, xuất artifacts cách ly.
- `--repeat N` tổng hợp pass rate, hard fail runs, warning runs, min/avg/max của cost/tool_calls.
- collect thêm `stylestat.Compute`, grade thêm delta văn phong.
- Báo cáo hiển thị so sánh baseline-variant của trung bình mẫu câu / tỷ lệ câu ngắn cuối chương / câu lặp xuyên chương / trộn tiêu đề.

**Nghiệm thu**: dùng một case ≥5 chương + một variant "tic câu nặng thêm", bị hồi quy văn phong đánh dấu warning; case thiếu chương hiển thị rõ `insufficient_sample` thay vì phán nhầm là qua.

### Giai đoạn 3 · LLM Judge

- `evals/rubrics/` + `judge.go`, rubric bảy chiều A/B.
- Judge thất bại (JSON không hợp lệ) → báo cáo ghi thất bại, không ảnh hưởng kết quả tất định.

**Nghiệm thu**: đầu ra Judge vào json+md, và không làm nhiễm cổng tất định.

### Giai đoạn 4 · Longform & Recovery

- case viết liên tục 3-5 chương / thẩm duyệt cuối cung / can thiệp người dùng / replay pending_commit / áp lực nén ngữ cảnh.
- dùng lại luật context + runtime của diag.

**Nghiệm thu**: phát hiện được dòng thời gian lặp, tàn dư pending, thiếu tóm tắt cuối cung, vòng lặp công cụ.

---

## 13. Quy ước bảo trì Case

- **Tiết chế số lượng**: Smoke 3-5, Workflow mỗi vai 3-5, Quality 2-4, Longform/Recovery mỗi loại 2-3. Quá nhiều thì không ai muốn chạy.
- **Case tốt**: đầu vào ngắn và rõ, phủ rủi ro thật, lộ vấn đề trong ít chương, không phụ thuộc model sinh câu cố định, không viết quá chi tiết sở thích văn phong.
- **Case xấu**: đầu vào quá dài, đa mục tiêu cùng lúc, phải chạy vài chục chương mới phán được, chỉ dựa vào cảm nhận chủ quan.
- **Đặt tên Variant**: `writer-anti-ai-tone` / `architect-rolling-outline` / `editor-strict-review`, mỗi thư mục chỉ để prompt cần thay.

---

## 14. Rủi ro và biên giới

- **Ngẫu nhiên của model**: cùng prompt chạy nhiều lần vẫn đổi. Thay đổi quan trọng thì `--repeat 3` xem xu hướng.
- **Chi phí**: Judge và longform tốn tiền. Cục bộ mặc định chỉ chạy **smoke** (1 chương × baseline+variant, cổng diag tất định, không bật Judge, không chạy stylestat); **stylestat chỉ bật ở Quality/Longform từ ≥5 chương** (smoke thiếu chương, `Compute` trả nil, báo cáo đánh `insufficient_sample`); suite đầy đủ để dành cho thay đổi lớn.
- **Thiên lệch của Judge**: Judge cũng là model, thiên về văn bản giải thích gọn gàng, không hẳn bằng tiểu thuyết hay — nên chỉ làm bổ trợ, stylestat là trục chính tất định.
- **Chỉ số hóa quá mức**: số chữ/số lần công cụ/chi phí/thống kê văn phong đều là tín hiệu chứ không phải mục tiêu. Con số stylestat có thành bệnh không do con người phán theo thể loại, **ngưỡng không viết cứng** (nhất quán với editor.md).
- **Không tự rollback online**: công cụ hồi quy offline, không chịu trách nhiệm tự đổi prompt / phát hành online.

---

## 15. Tổng kết

Giá trị của hệ thống đánh giá này không phải tự phán chất lượng văn học, mà biến việc sửa prompt từ "cảm tính" thành "có hồi quy, có bằng chứng, có đọc mẫu thủ công".

Khác biệt căn bản với thiết kế bản trước chỉ một câu: **bộ đánh giá đã ở trong codebase rồi.** `diag` là bộ chẩn đoán sự thật tất định, `stylestat` là bộ hồi quy văn phong toàn sách, bảy chiều của `ReviewEntry` là rubric nguyên bản. Hệ thống đánh giá chỉ cần một tầng harness Go mỏng — chạy hàng loạt, thu thập, ánh xạ Finding và thống kê thành cổng, tổng hợp báo cáo — chứ không phải viết lại những phán đoán sự thật đó bằng ngôn ngữ khác.

Một định nghĩa sự thật, không bao giờ trôi dạt. Đây chính là kỷ luật xuyên suốt từ kiến trúc đến đánh giá của dự án này: **harness tối thiểu, tái sử dụng tối đa, tất định thuộc mã, phán định thuộc LLM và con người.**

---

## 16. Tham chiếu

Cấu trúc phổ thông của LLM eval trong ngành (dataset / experiment / scorer / trace / regression gate) là nguồn ý tưởng của thiết kế này, nhưng **cố ý không bê nguyên** — "scorer" của dự án này là `diag`/`stylestat` sẵn có, "trace" là tầng sự thật checkpoint/session sẵn có, "dataset" là case gắn khẳng định tầng sự thật.

- OpenAI Evals · https://developers.openai.com/api/docs/guides/evals (lưu ý: nền tảng Evals được host của họ đã công bố lộ trình nghỉ hưu, chỉ dẫn **ý tưởng** kiểm tra có cấu trúc/chấm điểm tự động/hiệu chỉnh thủ công, không dùng làm phụ thuộc tương lai)
- Braintrust · https://www.braintrust.dev/foundations/what-is-an-eval
- LangSmith · https://docs.langchain.com/langsmith/evaluation-concepts