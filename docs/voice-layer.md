# Thiết kế tầng văn phong (Voice Layer)

> Trạng thái: thiết kế chốt v2 (2026-07-12, đã hấp thụ phản biện bên ngoài: bổ sung ngữ nghĩa ghi đè, ngữ nghĩa đường dẫn, thứ tự lắp ghép, cửa vào eval, giao thức thống kê đánh giá), **khả thi để triển khai**.
> Ưu tiên: trước khi tiến hóa mặt điều khiển (docs/engine-arbiter.md) — mùi AI là nỗi đau người dùng đang thật sự gặp.

## 1. Bối cảnh và định nghĩa vấn đề

Người dùng phản hồi nội dung sinh ra "nặng mùi AI". Sau khi chẩn đoán, kết luận: **vấn đề không phải tri thức văn phong bị gắn quá chặt vào luồng, mà là vòng lặp cải tiến đứt ở hai chỗ**:

1. **Mỗi lần sửa phải biên dịch lại** — toàn bộ tài sản ngữ nghĩa văn phong (anti-ai-tone.md, tiêu chuẩn viết trong writer.md, styles/*.md) đều `go:embed`, sửa một cách diễn đạt là phải build và phát hành lại;
2. **Không có vòng đo lường dành riêng cho văn phong** — sửa xong chỉ biết dựa vào cảm nhận khi đọc, không có đối chiếu trước/sau khách quan, việc tối ưu biến thành huyền học.

## 2. Điểm qua hiện trạng (tài sản liên quan văn phong gồm năm tầng)

| Tầng | Vị trí | Hiện trạng | Người dùng chỉnh được |
|----|------|------|---------|
| Phán đoán ngữ nghĩa | `assets/references/anti-ai-tone.md` | writer né tránh + editor dẫn chứng dùng chung, năm loại: cấu trúc/dùng từ/miêu tả/đối thoại/nhịp điệu | ❌ nhúng trong binary |
| Tiêu chuẩn viết | `assets/prompts/writer.md` §Tiêu chuẩn viết | Trộn cùng giao thức thực thi trong một file nhúng | ❌ |
| Preset văn phong | `assets/styles/*.md` (4 file) | cfg.Style chọn một điểm, gắn thêm vào prompt writer | ❌ và không thêm mới được |
| Luật cơ học | `internal/rules` | Từ nhàm/từ cấm/số chữ, commit kiểm tra bắt buộc | ✅ đã có ba tầng ghi đè (lưu ý: tầng "cấp dự án" của nó bám **cwd**, xem 3.4) |
| Sở thích lúc chạy | hành động `rules` của Arbiter | Ngôn ngữ tự nhiên → có cấu trúc, hiệu lực qua các lần khởi động lại | ✅ |

Còn hai hạ tầng then chốt: **stylestat** (thống kê tic câu ở cấp toàn sách, đưa ngược cho writer làm "gương soi cửa miệng", thuần mã và không ảo giác) và **`OverridePrompt` của eval** (hạ tầng A/B prompt đã có sẵn).

Kết luận: khả năng chỉnh ở tầng cơ học và nguyên liệu đo lường đã sẵn sàng; thiếu sót tập trung ở **tầng ngữ nghĩa không ghi đè được** và **vòng đo lường chưa hướng vào văn phong**.

## 3. Thiết kế

### 3.1 Nguyên tắc cốt lõi

**Tách "viết thế nào" (văn phong) khỏi "phối hợp thế nào" (giao thức): cái trước dữ liệu hóa, ghi đè được; cái sau giữ nguyên nhúng trong binary.**

### 3.2 Tách writer.md: điền lại tại chỗ bằng placeholder

Mục tiêu chuẩn viết trong writer.md nằm ở **giữa** file (sau giao thức thực thi, trước tính liên tục nhân vật phụ), nên không thể gắn nối đơn giản ở đuôi. Phương án dùng placeholder:

- `writer.md` (giao thức, nhúng): giữ phần giao thức thực thi / chạy tiếp từ điểm dừng / viết lại và gọt giũa / contract chương / phần giải thích cơ chế sở thích người dùng / **toàn bộ mục số chữ (gồm cả gợi ý cách viết)** / tính liên tục nhân vật phụ / tham số commit; vị trí mục tiêu chuẩn viết cũ được thay bằng **một** placeholder `{{VOICE}}`
- `voice.md` (văn phong, ghi đè được): toàn bộ mục tiêu chuẩn viết (khử mùi AI / đa dạng kiểu câu / không thuật lại tình tiết cũ)

Gợi ý cách viết theo số chữ được giữ lại trong file giao thức (tiếp thu từ phản biện 2026-07-12): nó gắn chặt với việc thực thi contract số chữ, tách ra sẽ cần placeholder thứ hai và biến Voice thành định dạng nhiều mảnh — không đáng cho một đoạn kỹ thuật rất ít người muốn ghi đè; sở thích số chữ của người dùng đi qua user_rules. Tên file vẫn giữ `writer.md` (eval `OverridePrompt` lấy tên file làm khóa, đổi tên chỉ thêm dây nối).

**Thứ tự lắp ghép phải tương thích từng byte với hiện trạng**. Hiện trạng là `writer.md → simulationGuidance → style` (assets/load.go:84 + agents/build.go:247), nên hàm lắp ghép duy nhất phải là:

```go
// Cửa vào duy nhất cho production, eval và test; điền lại {{VOICE}} tại chỗ đảm bảo việc tách không mất mát
func BuildWriterPrompt(protocolTemplate, voice, simulationGuidance, style string) string
// = replace(protocolTemplate, "{{VOICE}}", voice) + simulationGuidance + style
```

Bài học tiền lệ: phần chú thích của `WithSimulationGuidance` từng ghi lại cái bẫy "baseline có bọc, variant không có → A/B không tương đương"; đường lắp ghép rẽ nhánh là mảnh đất của loại sự cố đó, nên hội tụ về một hàm duy nhất.

### 3.3 Mô hình ghi đè: ngữ nghĩa theo từng tài sản (không mập mờ)

| Tài sản | Ngữ nghĩa ghi đè | Lý do |
|------|---------|------|
| `voice.md` | **Nối thêm**: bản tích hợp giữ nguyên, bản toàn cục/theo sách được nối thêm như đoạn có đánh dấu | Thay cả file sẽ khiến người dùng mãi đứng ở bản tích hợp cũ; nhu cầu thường gặp là chỉnh nhẹ chứ không viết lại |
| `anti-ai-tone.md` | **Nối thêm** (như trên) | Nhu cầu thường gặp là bổ sung phán đoán; số người muốn lật ngược phán đoán tích hợp là rất ít, không thiết kế cho họ |
| `styles/<name>.md` | **Thay cả file cùng tên**; tên file mới tức là thêm style mới | Văn phong là một giọng tổng thể, gộp hai style vô nghĩa |
| `genres/<name>/style-references.md` | Thay cả file cùng tên; style tùy chỉnh không có reference thì **cho phép thiếu, không lùi về default** (tham chiếu sai còn tệ hơn không có) | Như trên |
| user_rules | Ưu tiên cao nhất lúc chạy (hiện trạng không đổi) | — |

Việc lắp ghép theo ngữ nghĩa nối thêm có dấu mốc biên rõ ràng:

```
## Văn phong mặc định của dự án
...
## Ghi đè văn phong toàn cục của người dùng (các yêu cầu dưới đây ưu tiên hơn mặc định dự án)
...
## Ghi đè văn phong của cuốn sách này (các yêu cầu dưới đây ưu tiên hơn tất cả những phần trên)
...
```

**Biên giới trung thực**: dưới ngữ nghĩa nối thêm, "cái sau thắng" là chỉ thị ưu tiên dành cho LLM, không phải bảo đảm cơ học — văn phong là nội dung mang tính gợi ý, điều đó chấp nhận được; ràng buộc cần bảo đảm cơ học thì đi qua tầng rules (ở đó mới là ghi đè thật). Biên giới này được ghi vào tài liệu người dùng.

`arc-templates.md` thuộc mặt quy hoạch (định hình cấu trúc truyện chứ không phải giọng văn), **không vào danh sách trắng v1**, ghi lại để bàn sau.

### 3.3b Tầng mức nội dung (`content_rating`): nối thêm, không ghi đè

`content_rating` là khai báo của người vận hành (người lớn) về mức nội dung tác phẩm của
chính mình: `general` (mặc định) / `mature` / `explicit`, nhận bí danh (`adult`/`16+`/
`người lớn` → mature; `18+`/`nsfw` → explicit). Cấu hình nằm ở khoá `content_rating` trong
`config.json`; `bootstrap.NormalizeContentRating` chuẩn hoá ở `FillDefaults()`.

Đây **không phải** một tầng ghi đè văn phong mà là một đoạn chỉ thị được **nối thêm** vào
system prompt, cùng chỗ với voice:

```go
writerPrompt := bundle.WithRating(
    assets.BuildWriterPrompt(bundle.Prompts.Writer, bundle.Voice, bundle.Styles[cfg.Style]),
)
```

Bốn vai trò đều đi qua `bundle.WithRating`: `writer`, `editor`, `architect_short`,
`architect_long`. Writer là nơi cần nhất (nó viết ra câu chữ), nhưng editor phải biết mức
để không tự đánh giá thấp cảnh người lớn hợp lệ, và hai kiến trúc sư phải biết để không quy
hoạch một câu chuyện lệch tông.

**Vì sao để LLM chứ không để code**: hệ thống chỉ ghi mức vào prompt để model không tự
đoán mức trần; nó **không** kiểm duyệt thay người dùng. Ranh giới bất khả thương lượng vẫn
nằm trong chính đoạn chỉ thị, giống nhau ở cả hai mức:

- không nội dung tình dục liên quan trẻ vị thành niên, dù là gợi ý, hồi tưởng hay ẩn dụ;
- không miêu tả bạo lực như một bản hướng dẫn có thể làm theo ngoài đời;
- đồng thuận phải tồn tại trong truyện.

Mức `general` nối thêm **chuỗi rỗng** — tư thế mặc định vẫn là tư thế thận trọng. Đây là
lựa chọn có chủ ý: config không khai thì hệ thống không tự mở nội dung người lớn.

### 3.4 Ngữ nghĩa đường dẫn: cấp sách bám outputDir, không bám cwd

```
cấp sách   <outputDir>/style/     >   toàn cục   ~/.ainovel/style/   >   mặc định tích hợp (embed dự phòng)
```

- Bám outputDir khiến Voice **đi theo sách**: đổi thư mục để khôi phục cùng một cuốn sách thì nạp cùng một bản văn phong; phân giải đường dẫn nhất quán giữa Docker/headless/TUI; nhiều sách chung cwd không giẫm lên nhau
- Chữ ký `assets.Load` nhận tường minh gốc phân giải (outputDir), **bên trong không đọc cwd**
- Lưu ý khác biệt với tầng rules: `./.ainovel/rules` của rules bám cwd (quy ước sẵn có trong internal/rules/loader.go, thiết kế này không động tới); tài liệu người dùng nói rõ hai ngữ nghĩa khác nhau — rules là "cấp dự án", voice là "cấp sách"

Cấu trúc đầy đủ của thư mục người dùng:

```
<outputDir>/style/            (~/.ainovel/style/ cùng cấu trúc)
  voice.md                    đoạn nối thêm
  anti-ai-tone.md             đoạn nối thêm
  styles/
    xianxia.md                thêm mới hoặc thay thế cùng tên
  genres/
    xianxia/
      style-references.md     tùy chọn
```

Tên style chính là tên file, kiểm tra `[a-z0-9-]+`, từ chối ký tự đường dẫn.

### 3.5 Vì sao mở cho người dùng là an toàn

Mọi bất biến của giao thức đều nằm ở **tầng sự thật**: draft trước check, commit kiểm tra luật cơ học bắt buộc, chặn vượt biên số chữ, checkpoint idempotent — không nằm trong prompt. Người dùng có sửa voice.md lệch lạc đến đâu, guard và tiền điều kiện của công cụ vẫn có hiệu lực; kết quả xấu nhất là văn phong khó đọc, máy trạng thái không hỏng được.

### 3.6 Thời điểm có hiệu lực và cửa vào eval

- v1 phân giải lúc khởi động, **khởi động lại mới có hiệu lực** (khôi phục từ điểm dừng chính xác tới từng bước, chi phí khởi động lại gần như bằng không; không làm hot reload)
- eval thêm **cửa vào variant độc lập cho voice** (như `Bundle.OverrideVoice(raw)`), bên trong đi cùng đường `BuildWriterPrompt` — cấm A/B văn phong bằng cách ghi đè cả writer.md (sẽ kéo theo giao thức, và giao thức baseline/variant có thể không tương đương)

## 4. Vòng đo lường: bộ đánh giá văn phong

```
sửa voice/anti-ai-tone
  → bộ đánh giá văn phong (ca cố định, eval voice-variant A/B)   ← phần thêm mới duy nhất
  → so sánh chỉ số stylestat (chỉ số cứng tất định)
  + LLM judge theo từng phán đoán của anti-ai-tone chấm điểm kèm dẫn chứng (giai đoạn đầu chỉ báo cáo, chưa làm cổng cứng)
```

Giao thức thống kê (đầu vào cố định chỉ bảo đảm **so sánh được**, không bảo đảm tái lập được):

- baseline/variant khóa cùng model và cùng tham số suy luận
- mỗi ca lặp N≥3 lần, báo cáo giá trị trung bình, phương sai và mẫu thô
- judge chấm mù (không lộ danh tính baseline/variant)
- ca bao phủ theo thể loại × kiểu chương (mở đầu/đẩy đưa thường nhật/cao trào/thu kết)

## 5. Những gì dứt khoát không làm (chống thiết kế quá mức)

- Không mở prompt giao thức cho người dùng cuối (`OverridePrompt` giữ làm năng lực nội bộ của eval)
- Không hot reload lúc đang chạy
- Không mở các mẫu regex của stylestat thành cấu hình người dùng (cửa vào mở rộng tầng cơ học đã có: fatigue_words/forbidden_phrases của rules)
- Không làm chợ trao đổi/chia sẻ văn phong (copy thư mục style là đã chia sẻ được tự nhiên)
- arc-templates không vào danh sách trắng v1

## 6. Các bước triển khai và nghiệm thu

1. Tách writer.md (placeholder `{{VOICE}}`) + hàm lắp ghép duy nhất `BuildWriterPrompt`
2. Bộ phân giải ba tầng: `assets.Load(outputDir, style)` + ngữ nghĩa theo từng tài sản (bảng 3.3) + gộp liệt kê styles; unit test bao phủ ưu tiên/lùi dự phòng khi thiếu/dấu mốc nối thêm
3. Cửa vào `OverrideVoice` cho eval
4. Tài liệu người dùng: cấu trúc thư mục, ngữ nghĩa từng tài sản, khác biệt ngữ nghĩa đường dẫn giữa rules và voice, ví dụ
5. Bộ đánh giá văn phong (có thể để lại thành nhiệm vụ riêng)

**Tiêu chí nghiệm thu**: ① khi không có file ghi đè nào, `BuildWriterPrompt` sinh ra **giống từng byte** với trước khi tách; ② ưu tiên ba tầng và ngữ nghĩa nối thêm/thay thế có unit test dạng bảng; ③ thêm `styles/xianxia.md` rồi `style: xianxia` là dùng được ngay; ④ eval voice A/B đi cùng đường lắp ghép với production (có test chứng minh); ⑤ toàn bộ test và hồi quy sim xanh.

## 7. Quan hệ với tiến hóa mặt điều khiển

Hoàn toàn trực giao (mặt nội dung so với mặt điều khiển), không phụ thuộc triển khai. Thứ tự đã thống nhất: **tầng văn phong → bộ đánh giá văn phong → Engine/Arbiter (tiến theo nghị quyết §8 của tài liệu tương ứng)**.