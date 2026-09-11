# Đường ống nhập tiểu thuyết bên ngoài theo ngữ nghĩa

> Trạng thái: đã hiện thực (v1, `internal/host/imp`; vớt tiền tố bị cắt nằm ở giai đoạn ba·bổ sung)
> Ngày: 2026-07-15
> Mục tiêu: để việc nhập tiểu thuyết bên ngoài vừa liên tục hưởng lợi từ nâng cấp năng lực model, vừa có bảo đảm kỹ thuật về không mất toàn văn, chẩn đoán được khi thất bại, khôi phục được sau sập và kiểm chứng được khi phát hành.
> Sửa đổi: thứ tự SourceUnit theo thứ tự số `(Line, Part)` (§7.3/§8.3); vớt tiền tố bị cắt hạ cấp thành tối ưu hiệu quả có thể để sau và yêu cầu quan sát được (§9.5/§13.3/§19); mở mức model của hàm ngữ nghĩa thành núm vặn (§13.1/§17).
> Sửa đổi 2026-07-16: núm vặn mức model lên sóng thành cấu hình roles `import_segment/import_analyze/import_synthesize` (§13.1); phân tách lại theo ngôn ngữ tự nhiên lên sóng thành `--guide` và đầu vào ngữ nghĩa `guidance.txt` trong workspace (§18.3); thất bại ngữ nghĩa lưu thống nhất phản hồi gốc vào failures/ (§14.2); xác nhận phân tách hỗ trợ `y` cho qua một lần trong panel (§8.4); nhập liệu chưa hoàn tất chủ động nhắc lúc khởi động (§18.2). Chế độ JSON Schema (bậc 1 của §13.2) chưa hiện thực, đánh dấu TODO chờ thống nhất cải tạo với các điểm gọi model khác trong repo.

## 1. Một câu

Nhập liệu không phải "dùng regex cắt văn bản, rồi để model nhả JSON cả cuốn sách một lần", cũng không phải một Import Agent chạy tự do; nó là một **đường ống biên dịch ngữ nghĩa theo giai đoạn**:

> Model chịu trách nhiệm hiểu ngữ nghĩa mở, mã chịu trách nhiệm tọa độ, bao phủ, kiểu, hash, thứ tự và idempotent; mọi sản phẩm ngữ nghĩa chỉ được phát hành vào trạng thái sách chính thức sau khi kiểm chứng xong trong workspace riêng.

```text
văn bản bên ngoài
  → đọc và chuẩn hóa tất định
  → LLM nhận diện ranh giới chương/tập/văn bản phụ trợ
  → mã kiểm chứng bao phủ toàn văn
  → người dùng xác nhận phân tách (có thể uỷ quyền tự động chấp nhận tường minh)
  → LLM trích sự thật từng chương theo lô chương liên tục
  → LLM tổng hợp ngữ nghĩa toàn sách theo tầng
  → mã lắp ghép và kiểm chứng Foundation
  → phát hành Foundation và chương theo kiểu idempotent
  → mặc định tạm dừng một lần; chỉ khi có `--continue` tường minh mới tiếp sức theo cổng chuẩn
```

## 2. Vì sao bắt buộc phải tái cấu trúc

Hiện thực hiện tại là:

```text
regex cắt chương
  → đưa toàn bộ chính văn các chương vào ReverseFoundation một lần
  → model xuất một lần premise / characters / world_rules / đại cương toàn bộ chương / compass
  → ghi thẳng vào Foundation chính thức
  → rồi đọc lại cùng chính văn theo từng chương, phân tích và commit
```

Nó có bốn vấn đề cấu trúc.

### 2.1 Cắt chương đang liệt kê ngữ nghĩa mở

Tiêu đề chương không có cú pháp đóng. Tiếp tục thêm các regex kiểu "Chương N", "Tập N", "Chapter N" chỉ phủ được định dạng đã thấy, không phủ được tiêu đề tác giả tự đặt, dàn trang trộn, phân cấp tập-chương và định dạng tương lai.

Nghiêm trọng hơn, cách cắt hiện tại khiến ranh giới không khớp biến mất thẳng khỏi kết quả, và có thể âm thầm bỏ văn bản trước tiêu đề đầu tiên, chương rỗng và nội dung bị phán là nhiễu đuôi. Mã không chứng minh được những nội dung đó nên bị bỏ.

### 2.2 Đầu vào và đầu ra của lệnh gọi Foundation đều tăng tuyến tính theo số chương

`ReverseFoundation` đồng thời gánh việc hiểu cả cuốn sách và sinh đại cương toàn bộ chương: đầu vào gồm toàn bộ chính văn, đầu ra gồm cấu trúc chi tiết từng chương. 54 chương đã có thể làm JSON bị cắt; tăng `max_tokens` chỉ đẩy điểm thất bại ra xa hơn ở sách dài hơn.

### 2.3 Trước khi thất bại đã sửa trạng thái chính thức

Foundation và chương vừa phân tích vừa phát hành. Khi bước sau thất bại, người dùng nhận được trạng thái sách chính thức nửa đã nhập xong, nửa chưa phân tích. `from=N` hiện tại chỉ giả định người dùng biết khôi phục từ đâu, không chứng minh được file nguồn, kết quả phân tách và các chương sẵn có vẫn nhất quán.

### 2.4 Nhiều kết luận ngữ nghĩa bị viết cứng

Phương án hiện tại còn cố định:

- chính văn nhập chỉ được là một tập;
- chỉ được chia thành 1-3 cung;
- theo ngưỡng 25/80 của số chương đã nhập mà chọn short/mid/long;
- thiên về ép tạo `open_threads` để cho phép viết tiếp;
- mỗi chương phải có nhân vật, số sự kiện cố định và móc câu loại cố định.

Đây đều không phải sự thật chứng minh cơ học được từ định dạng file, nên để model phán theo chính văn, hoặc để người dùng biểu đạt ý định rõ ràng.

## 3. Mục tiêu và phi mục tiêu

### 3.1 Mục tiêu

1. **Định dạng mở hiểu được**: không yêu cầu người dùng đổi tiểu thuyết sang định dạng tiêu đề tích hợp, cũng không yêu cầu viết regex.
2. **Toàn văn giải trình được**: mỗi đoạn văn bản nguồn khác rỗng đều phải thuộc một chương rõ ràng hoặc vùng phụ trợ, cấm mất im lặng.
3. **Quy mô kiểm soát được**: không còn lệnh gọi đọc toàn bộ chính văn và xuất toàn bộ đối tượng chương; phân đoạn, lô chương hai ngân sách và tổng hợp theo khoảng đều có biên đầu vào/đầu ra cục bộ, đầu ra toàn cục chỉ tăng theo độ phức tạp ngữ nghĩa thật như nhân vật, tập-cung.
4. **Thất bại không làm ô nhiễm**: không ghi trạng thái sáng tác chính thức trước khi phân tích ngữ nghĩa và kiểm chứng Foundation xong.
5. **Khôi phục chính xác**: khôi phục dựa vào ảnh chụp nguồn và `InputDigest` của sản phẩm, không phụ thuộc `from=N` hay trí nhớ người dùng.
6. **Lợi ích model đến thẳng**: model mạnh hơn trực tiếp cải thiện nhận diện ranh giới, trích sự thật, chia tập-cung và phán đoán viết tiếp, không cần thêm luật Go.
7. **Dùng lại ngữ nghĩa commit chính thức**: phát hành chương tiếp tục dùng PendingCommit, checkpoint và idempotent digest của `commit_chapter`.
8. **Quan sát đầy đủ**: tiến độ, danh tính model, lượng dùng, phản hồi thất bại gốc và lỗi cuối đều có chỗ neo rõ ràng.
9. **Tương tác và tự động song song**: mặc định để người dùng xác nhận ranh giới ngữ nghĩa rủi ro cao, đồng thời cung cấp uỷ quyền vận hành không người tường minh; đường tự động không dựa vào đoán mò im lặng.

### 3.2 Phi mục tiêu

- Không dựng Coordinator hay vòng lặp dài Agent tổng quát.
- Không dựng khung Workflow/PolicyEngine/đồ thị nhiệm vụ tổng quát.
- Không tự sửa hay viết lại nguyên văn người dùng.
- Không hiện thực cơ sở dữ liệu, truy hồi vector hay song song phân tán cho nhập liệu.
- Không hỗ trợ gộp mờ một tiểu thuyết khác vào sách đã có.
- Không hiện thực di trú trạng thái `from=N` cũ hay tương thích song đường.
- Không mở rộng EPUB/PDF trong RFC này; bản một vẫn chỉ nhận txt/md, tầng đọc giữ cục bộ, tương lai thay được mà không đổi contract phía sau.

## 4. Ranh giới trách nhiệm

| Câu hỏi | Thuộc về | Lý do |
|---|---|---|
| Giải mã byte, chuẩn hóa xuống dòng | Go | định dạng file và chuyển đổi tất định |
| Vị trí nguồn nào là tiêu đề chương, tiêu đề tập hay văn bản phụ trợ | LLM | ngữ nghĩa mở, không vét cạn được |
| Tiêu đề ứng với vị trí nguồn ổn định nào | Go | SourceUnit, điểm neo nguyên văn và khoảng byte kiểm cơ học được |
| Chương nào đã xảy ra chuyện gì | LLM | hiểu ngữ nghĩa văn học |
| Nhân vật, quy tắc thế giới, phục bút và quan hệ quy nạp thế nào | LLM | quy nạp ngữ nghĩa xuyên chương |
| Ranh giới tập-cung, truyện đã khép chưa, cấp quy hoạch | LLM | phụ thuộc hình dạng tự sự, không phụ thuộc ngưỡng cố định |
| Khoảng chương có tăng dần, không chồng lấn, bao phủ đầy đủ không | Go | bất biến chứng minh được |
| Kiểu JSON, enum đóng, số chương tham chiếu có hợp lệ không | Go | contract có kiểu |
| Có dùng lại được phân tích sẵn có không | Host/Workspace | chỉ dùng lại khi đầu vào ngữ nghĩa thật dựng lại được cùng `InputDigest` |
| Khi nào ghi trạng thái sách chính thức | Host/Store | giao thức phát hành và khôi phục sau sập |
| Có uỷ quyền tiếp theo phân tách hiện tại không | Người dùng/Intent | xác nhận tương tác hoặc `--yes` tường minh, không để mã trả lời lén |

Các lệnh gọi LLM ở đây không phải mặt điều khiển Arbiter, cũng không phải vòng lặp sáng tác Worker. Chúng là **hàm ngữ nghĩa** có biên rõ ràng: sự thật có kiểu vào, kết quả ngữ nghĩa có kiểu ra, Host kiểm chứng rồi thực thi.

## 5. Kiến trúc tổng thể

```text
[TUI / Headless]
       │ /import <path> / uỷ quyền tự động / xác nhận / hủy
[Host]
       │ vòng đời nhập độc chiếm, sự kiện, runtime model
[imp.Runner]
       ├── LoadState → NextAction (chỉ suy từ sự thật workspace)
       ├── Source     đọc, giải mã, chuẩn hóa, chụp nhanh
       ├── Segment    chiếu cấu trúc → LLM nhận diện ranh giới → kiểm bao phủ
       ├── Analyze    lô liên tục hai ngân sách → sự thật từng chương tạm lưu
       ├── Synthesize quy nạp theo tầng → BookSynthesis
       ├── Validate   lắp ghép và kiểm chứng Foundation đầy đủ
       └── Publish    Foundation chính thức → commit_chapter
               │
[workspace meta/import]        [Store chính thức]
ảnh chụp nguồn/phân tách/phân tích/tổng hợp   Progress/Checkpoint/Artifact/PendingCommit
```

Runner là điều phối giai đoạn tất định thường, không có năng lực quyết định tự do. Mỗi lần nó chỉ thực thi một hành động do `NextAction` suy ra, xong hành động thì đọc lại sự thật.

## 6. Workspace và suy trạng thái

Sự thật trong lúc nhập nằm trong thư mục sách:

```text
meta/import/
├── manifest.json
├── intent.json
├── source.txt
├── guidance.txt          # khi tồn tại: hướng dẫn phân tách ngôn ngữ tự nhiên của người dùng (--guide), là đầu vào ngữ nghĩa của segmentation
├── segmentation.json
├── confirmation.json
├── analyses/
│   ├── 000001.json
│   ├── 000002.json
│   └── ...
├── range-digests/
│   ├── 000001-000050.json
│   └── ...
├── synthesis.json
├── story-resolution.json
└── failures/
    ├── last.json
    └── last-response.txt
```

Bản một giữ lại workspace. Nó vừa là căn cứ khôi phục, vừa là bản ghi audit việc nhập; không thêm cơ chế tự dọn và lưu trữ lịch sử.

`intent.json` lưu uỷ quyền tường minh khi người dùng khởi động nhập (tự động xác nhận, chọn trước trạng thái truyện uncertain, có bỏ qua Hold hoàn tất không). Đây là ý định người dùng vẫn phải tuân sau khôi phục, không phải trạng thái giai đoạn đoán được từ sản phẩm; sau khi tạo thì Runner không sửa lại im lặng.

### 6.1 Manifest

```go
type ImportManifest struct {
	Version          int    `json:"version"`
	SourceName       string `json:"source_name"`
	RawSHA256        string `json:"raw_sha256"`
	NormalizedSHA256 string `json:"normalized_sha256"`
	Encoding         string `json:"encoding"`
	SizeBytes        int64  `json:"size_bytes"`
	CreatedAt        string `json:"created_at"`
}

type ImportIntent struct {
	Version             int    `json:"version"`
	AutoConfirm         bool   `json:"auto_confirm,omitempty"`
	StoryResolution     string `json:"story_resolution,omitempty"` // open / closed
	ContinueAfterImport bool   `json:"continue_after_import,omitempty"`
}
```

- `source.txt` là ảnh chụp cục bộ sau chuẩn hóa, khôi phục không còn phụ thuộc đường dẫn gốc vẫn tồn tại;
- Manifest không lưu đường dẫn nguồn tuyệt đối, tránh lộ thư mục máy và loại bỏ vấn đề khôi phục khi file bị di chuyển;
- Intent chỉ nhận giá trị tập đóng, lưu chính xác uỷ quyền người dùng trong lệnh khởi động; khi khôi phục không suy ngược ý định cũ từ advance mode hiện tại;
- khi phiên bản schema không khớp thì yêu cầu tường minh dùng phiên bản khớp để tiếp tục hoặc nhập lại, không đoán mò di trú.

Lần tạo đầu tiên ghi đủ và kiểm chứng manifest, intent, source trong thư mục tạm cùng cấp, rồi rename thư mục để phát hành thành `meta/import/`; `meta/import/` không tồn tại thì chưa tính là workspace hoạt động. Nhờ đó bộ ba ban đầu không vào `NextAction` ở hình thái nửa khởi tạo, và không cần thêm `stage=initializing` cho quá trình tạo. Lúc khởi động phát hiện thư mục khởi tạo còn sót phải nhắc tường minh và giữ thông tin chẩn đoán, không tự coi là workspace thành công, cũng không xóa im lặng.

### 6.2 Không lưu liệt kê giai đoạn có thể trôi dạt

Trạng thái lưu trữ bền vững không ghi các trường điều khiển như `stage=analyzing`, `current=37`. Hành động kế tiếp do sản phẩm suy ra:

```text
không có manifest/intent/source   → ingest
không có segmentation            → segment
không có confirmation khớp digest đầu vào segmentation → await_confirmation / auto_confirm
tồn tại phân tích chương thiếu hoặc digest đầu vào không khớp → analyze_first_missing
thiếu RangeDigest khớp đầu vào hoặc synthesis      → synthesize_first_missing
story_status=uncertain và chưa có chọn của người dùng khớp → await_story_resolution
sản phẩm chính thức không khớp synthesis                 → publish
mọi sản phẩm chính thức đã khớp                            → done
```

`Stage` trong sự kiện chỉ để UI hiển thị, không phải nguồn sự thật khôi phục.

### 6.3 Danh tính sản phẩm thống nhất

Không hiện thực đồ thị phụ thuộc. Mỗi sản phẩm ngữ nghĩa trong workspace dùng thống nhất một quy tắc danh tính:

```go
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}
```

`InputDigest` bao phủ toàn bộ **đầu vào ngữ nghĩa** mà hành động đó thật sự tiêu thụ, mã hóa theo thứ tự cố định rồi tính:

- segmentation: nội dung nguồn đã chuẩn hóa, chiếu SourceUnit, hướng dẫn người dùng và phiên bản prompt/schema phân đoạn;
- confirmation: nội dung segmentation và cách xác nhận;
- phân tích chương: khoảng chương của lô cùng chính văn, continuity ledger trước khi vào lô, phiên bản prompt/schema và hướng dẫn người dùng;
- RangeDigest/BookSynthesis: nội dung phân tích có thứ tự hoặc digest tầng dưới mà mỗi cái tiêu thụ, phiên bản prompt/schema tổng hợp;
- story resolution: nội dung synthesis và lựa chọn người dùng;
- phát hành: nội dung chuẩn hóa của đối tượng lĩnh vực cần phát hành.

Các sự thật thực thi như provider/model, usage, thinking ghi vào provenance/session, không tự động làm phân tích đã thành công mất hiệu lực khi cấu hình model đổi; khi người dùng yêu cầu phân tích lại thì xóa sản phẩm tương ứng tường minh. Phán đoán dùng lại cache chỉ nhìn xem hành động hiện tại có dựng lại được cùng `InputDigest` không.

`NextAction` đi theo đường ống tuyến tính cố định để tìm sản phẩm đầu tiên thiếu, phân tích thất bại hoặc `InputDigest` không khớp. Khi phân tách lại, sửa hướng dẫn người dùng hoặc đổi sự thật thượng nguồn, hạ nguồn tự nhiên không khớp; không viết luật vô hiệu kiểu "khi phân tách đổi thì xóa tay những file nào".

Khi phát hành thì đối chiếu từng mục sản phẩm chính thức với kết quả tổng hợp; giống thì bỏ qua idempotent, khác thì báo xung đột, không ghi đè đoán mò. Nhờ đó xóa `ResumeFrom`. Khôi phục chỉ cần chạy lại `/import`; Runner sẽ tiếp tục từ sự thật thiếu đầu tiên.

## 7. Đọc file nguồn

### 7.1 Giải mã

Bản một hỗ trợ:

- UTF-8 / UTF-8 BOM;
- GB18030 (phủ văn bản tiểu thuyết GBK thường gặp).

Kết quả giải mã phải trả về encoding đã chọn, và ghi vào Manifest cùng sự kiện tiến độ. Không được giấu "thử GB18030" thành lùi dự phòng im lặng. Không giải mã được đáng tin hoặc xuất hiện ký tự thay thế không chấp nhận được thì thất bại thẳng, lỗi kèm kết quả phát hiện.

### 7.2 Chuẩn hóa

Chỉ làm các chuyển đổi không đổi nội dung văn học:

- gỡ BOM;
- CRLF/CR thống nhất thành LF;
- giữ dòng trống, thụt lề, dòng tiêu đề và ký tự chính văn;
- không xóa văn bản đầu sách, chương rỗng, quảng cáo, thông tin bản quyền hay cái gọi là nhiễu đuôi.

Mọi quyết định loại trừ để lại cho kết quả ngữ nghĩa phân đoạn và hiển thị trong bản xem trước.

### 7.3 Tọa độ ổn định

Văn bản chuẩn hóa dựng bảng `SourceUnit` thống nhất:

```go
type SourceUnit struct {
	ID        string // L1257; dòng vượt ngân sách tách thành L1257.1, L1257.2
	Line      int
	Part      int
	StartByte int
	EndByte   int
	Text      string
}
```

- `ID` chỉ dùng hiển thị và model tham chiếu; mọi phán đoán thứ tự, bao hàm và tăng dần đều so theo bộ số `(Line, Part)`, cấm so từ điển trên chuỗi ID (`"L900"` theo từ điển sẽ lớn hơn `"L1000"`); JSON chiếu giữ id chuỗi, phía Go phân tích thành `(Line, Part)` rồi mới so;
- dòng thường ứng một unit, đường phổ biến vẫn là tọa độ số dòng trực quan;
- một dòng vượt ngân sách chiếu cấu trúc thì Go chỉ sinh nhiều **unit ảo** tại ranh giới ký tự UTF-8;
- phân mảnh ảo không ghi ngược vào `source.txt`, không chèn soft wrap, không đổi ký tự nguồn nào;
- khi trong cùng một unit có ranh giới, model trả unit ID và một điểm neo nguyên văn sao chép từng chữ; Go yêu cầu điểm neo duy nhất trong unit đó, rồi ánh xạ thành vị trí byte chính xác;
- điểm neo không tồn tại hoặc không duy nhất thì phản hồi lỗi cụ thể cho model, cấm đoán offset, cắt văn bản hoặc yêu cầu người dùng sửa bản thảo trước.

Nhờ đó văn bản chia chương thường giữ mô hình số dòng, còn cả đoạn không xuống dòng, một dòng chứa nhiều chương hay dòng dài bất thường cũng dùng cùng một kiểu tọa độ.

## 8. Phân tách ngữ nghĩa

### 8.1 Chiếu cấu trúc

Model thấy chiếu cấu trúc được chia khối theo ngân sách ngữ cảnh:

```json
{
  "owned_units": {"start": "L1200", "end": "L1800"},
  "context_units": {"start": "L1180", "end": "L1820"},
  "units": [
    {"id": "L1200", "line": 1200, "text": "Gió từ ngoài cổng thành thổi vào.", "blank_before": true},
    {"id": "L1257", "line": 1257, "text": "Tập hai · Bắc Cảnh", "blank_before": true, "blank_after": true}
  ],
  "user_guidance": ""
}
```

Vùng ngữ cảnh được phép chồng lấn, nhưng mỗi lần gọi chỉ được trả kết quả cho `owned_units`, nên không có bỏ phiếu chồng khối hay gộp xung đột. Kỷ luật tọa độ do Go thực thi (sửa đổi 2026-07-16): ranh giới model trả trong vùng ngữ cảnh không kích hoạt hỏi lại ngữ nghĩa — ranh giới đó thuộc khối kề (nó sẽ báo lại trong khoảng owned của chính nó), mã cắt thẳng và hiển thị giải thích; thử lại ngữ nghĩa chỉ để dành cho thất bại ngữ nghĩa thật (ID ảo giác ngoài chiếu, kind không hợp lệ v.v.). Hành vi cũ hỏi lại khi phản hồi vượt biên, model yếu thường tiêu hết cả 3 lần thử và kéo sập cả khối.

Kích thước chia khối tính theo context window và ngân sách dự trữ của model architect hiện tại, không chia theo số dòng cố định hay số chương cố định. Khi ngữ cảnh model mở rộng, số lần gọi tự nhiên giảm. Ngân sách quy hoạch không dùng hết hạn mức (sửa đổi 2026-07-16): chính văn owned chỉ là một phần yêu cầu, khi quy hoạch phải trừ độ dài thật của system prompt và hướng dẫn, rồi quy theo 3/4 để bù mức phình của JSON chiếu có bọc; vùng ngữ cảnh có thêm giới hạn byte (chunkBytes/8, sàn 4096), chặn phân mảnh ảo của dòng quá dài ăn hết ngân sách đầu vào. Đầu ra cũng có lưới đỡ đối xứng: khi JSON ranh giới một khối bị cắt do độ dài (nhiều chương ngắn) thì chia đôi khối và thử lại đệ quy — nửa khối có đường cache độc lập, thành quả thử lại không bị tính phí lại; vẫn bị cắt ở mức unit mới là thật sự thiếu dung lượng.

Quyết định ranh giới của từng khối được ghi xuống đĩa dưới dạng sản phẩm (`segment-chunks/chunk-*.json`, danh tính = danh tính phân tách + MaxUnitBytes + khoảng owned của khối — bảng unit do "nguồn chuẩn hóa + MaxUnitBytes" xác định duy nhất, khi đổi mức làm lại phân mảnh dòng quá dài thì cache tự nhiên không khớp, không dùng lại ranh giới cũ lệch chỗ), khối nào thất bại hoặc gián đoạn thì khi chạy lại khối đã xong được dùng lại với zero lệnh gọi — cùng triết lý với analyze từng chương và synthesize từng khoảng; sau khi segmentation cuối cùng ghi xuống đĩa thì cache cấp khối bị xóa. Khi tích hợp cuối cùng (resolve) thất bại cũng xóa cache khối và ghi ảnh chụp quyết định vào `failures/`: lúc đó digest cache luôn khớp, giữ nó sẽ khiến chạy lại đọc lại cùng một lô ranh giới với zero lệnh gọi và tái hiện tất định cùng một thất bại. Ranh giới chương có chính văn rỗng (tiêu đề giữ chỗ "đã khóa/trả phí" rất thường gặp trong nguồn tiểu thuyết mạng thật)…

### 8.2 Đầu ra của model

```go
type BoundaryDecision struct {
	UnitID    string   `json:"unit_id"`
	Anchor    string   `json:"anchor,omitempty"`
	Kind      string   `json:"kind"` // chapter / group / front_matter / back_matter
	Title     string   `json:"title,omitempty"`
	Uncertain bool     `json:"uncertain,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}
```

- `chapter` là đơn vị chính văn có thể commit, gồm cả phán đoán ngữ nghĩa xem mở đầu, dẫn nhập, ngoại truyện có tính là chương không;
- `group` là bằng chứng cấu trúc tầng trên như tập, bộ, thiên, không trực tiếp coi là chương;
- `front_matter` / `back_matter` đánh dấu vùng phụ trợ rõ ràng không vào chính văn chương;
- `anchor` phải từng chữ đến từ unit tương ứng; ranh giới ở điểm bắt đầu của unit thì được phép bỏ qua;
- `uncertain` chỉ dùng nhắc trong xem trước, mã không đặt ngưỡng tin cậy.

Không để model sinh regex. Regex vẫn sẽ ép ngữ nghĩa mở về cú pháp hữu hạn, và đưa vào giả định về escape, khớp cục bộ và định dạng thống nhất.

### 8.3 Mã kiểm chứng

Go chỉ kiểm:

1. Mọi unit ID tồn tại và nằm trong chiếu của lệnh gọi (owned + vùng ngữ cảnh; ngoài chiếu là ảo giác, hỏi lại kèm phản hồi);
2. Ranh giới vùng owned có kind thuộc tập đóng, anchor khác rỗng duy nhất trong unit tương ứng và ánh xạ tới ranh giới byte UTF-8, cùng vị trí không xung đột ngữ nghĩa (kind/tiêu đề khác nhau thì giữ cái nào không do Go phán, hỏi lại giao model; trùng hoàn toàn là thừa cơ học, cho qua rồi khử trùng lặng lẽ), khối đầu phải có ranh giới bọc điểm bắt đầu văn bản (đầu có phải lời nói đầu không do model phán, không để Go trả lời thay) — đều kiểm lúc gọi (sửa đổi 2026-07-16): giá trị xấu vào cache khối rồi tới lúc cuối mới phát hiện, digest luôn khớp sẽ khiến thất bại tái hiện tất định; ranh giới vùng ngữ cảnh chắc chắn bị cắt, không hỏi lại vì nó;
2a. **Tiêu đề hồi chiếu** (sửa đổi 2026-07-16, seg-v2): title của ranh giới chapter/group sau chuẩn hóa (bỏ khoảng trắng) phải thật sự tồn tại trong nguyên văn của unit ranh giới, nếu không thì hỏi lại lúc gọi — thực đo một nguồn crawl phân trang: trong 157 chương có 67 chương là ranh giới cùng tiêu đề do model bịa trên phần nối tiếp giữa chương (kỷ luật bao phủ mơ hồ ép mỗi khối đặt ranh giới ở đầu khối), tất cả đều bị mục kiểm sự thật này chặn. Phán đoán ngữ nghĩa vẫn thuộc model: nguồn thật không có quy ước tiêu đề thì đặt `uncertain=true` để giữ tiêu đề do model quy nạp (xem trước hiển thị dấu nghi ngờ); tiêu đề mô tả của front/back matter rủi ro thấp, không kiểm. prompt cũng siết đồng bộ: ranh giới chỉ rơi ở điểm phân tách cấu trúc thật, khi đầu khối là phần nối tiếp chương trước thì trả boundaries rỗng là đầu ra đúng (trừ trường hợp bọc phần đầu của khối đầu);
3. Thứ tự và trùng lặp ranh giới do Go sửa tất định chứ không phủ quyết (sửa đổi 2026-07-16): sắp ổn định theo byte đã phân tích để khôi phục thứ tự thật — thứ tự giữa khối do khoảng owned không chồng lấn đảm bảo, đảo thứ tự chỉ có thể xảy ra trong khối, sắp xếp không mất thông tin; trùng byte thì giữ cái xuất hiện trước và ghi vào `Notes`. Hành vi cũ yêu cầu tăng nghiêm ngặt nếu không thất bại toàn bộ, thực đo 319 ranh giới thua vì 1 chỗ đảo thứ tự trong khối, và cache khối khiến thất bại đó tái hiện tất định. Phán đoán thứ tự luôn theo thứ tự số `(Line, Part)`, không so từ điển ID;
4. Mỗi chương sinh ra có khoảng chính văn khác rỗng (tiêu đề giữ chỗ chính văn rỗng gộp vào đoạn trước, xem §8.1);
5. Mọi văn bản nguồn khác rỗng thuộc đúng một chương, một tiêu đề group hoặc front/back matter rõ ràng (văn bản khác rỗng đầu tiên chưa được gán chủ — giới thiệu đầu sách/quảng cáo bị bỏ sót ranh giới — do Go tất định thu thành front_matter và ghi `Notes` để xem trước xác nhận, không phủ quyết ở cuối);
5a. Chương trùng tên (tiêu đề sau bỏ khoảng trắng giống nhau) ghi vào `Notes` để người kiểm thủ công (sửa đổi 2026-07-16) — ở nguồn có quy ước tiêu đề, tên chương không nên trùng, trùng là tín hiệu tất định của "cùng một chương bị cắt nhầm"; có gộp không không do Go phán, `Notes` khác rỗng là chặn `--yes`;
6. Không có khoảng chồng lấn, vượt biên hoặc chưa được gán chủ;
7. group không bị tính nhầm vào tổng số chương.

"L1257 về mặt ngữ nghĩa có phải tiêu đề chương không" không do Go phán lại.

### 8.4 Người dùng xác nhận

Ở chế độ tương tác, trước khi xác nhận thì không gọi phân tích chương, cũng không ghi Store chính thức. Bản xem trước ít nhất hiển thị:

- số tập/group và số chương;
- toàn bộ tiêu đề chương, cuộn xem được;
- phạm vi và tóm tắt văn bản phụ trợ đầu và cuối;
- chương rỗng, chương dài bất thường và ranh giới model đánh dấu uncertain;
- dòng bắt đầu/kết thúc mỗi chương, tiện người dùng đối chiếu bản thảo.

Người dùng có thể:

- xác nhận (panel xem trước TUI nhấn `y`, bên trong chạy lại bằng AcceptSegmentation; cho qua một lần phân tách hiện tại, không ghi vào intent, confirmation ghi `method=user_confirmed`);
- nhập giải thích ngôn ngữ tự nhiên rồi nhận diện lại, ví dụ `/import --guide=Đoạn chuyển cảnh X cũng là chương độc lập`;
- hủy và giữ workspace (Esc).

`/import <path> --yes` là uỷ quyền không người tường minh: Runner sau khi kiểm bao phủ đạt thì ghi cùng sản phẩm confirmation, ghi `method=auto_authorized`, rồi phân tích tiếp. `--yes` kể cả khi có ranh giới uncertain cũng nghĩa là người dùng chọn tin lần phân tách này, nhưng uncertain vẫn giữ trong sản phẩm và log. **Ngoại lệ (sửa đổi 2026-07-16)**: khi phân tách có ghi chú dung sai (`Notes` khác rỗng — đã từng xảy ra hấp thụ chương rỗng, bọc đầu, khử trùng lặp), `--yes` không tự động cho qua mà vẫn dừng ở xem trước xác nhận — cấu trúc đã bị sửa tất định, uỷ quyền mù chưa xem xem trước không nên nuốt nó; `y` (AcceptSegmentation) đã xem xem trước không bị giới hạn này.

`--yes` chỉ bỏ qua xác nhận phân tách, không thay người dùng quyết `story_status=uncertain`, cũng không bỏ qua Hold hoàn tất nhập. Người dùng không cần viết regex hay điền tay `from=N`.

## 9. Trích sự thật từng chương theo lô liên tục

Sau khi xác nhận, bắt đầu từ phân tích thiếu đầu tiên, gom chương liên tục thành lô theo **hai ngân sách đầu vào và đầu ra** của model hiện tại. Bản một chạy tuần tự giữa các lô, không song song giữa cửa sổ: ID phục bút, bí danh nhân vật và thay đổi trạng thái có thứ tự thời gian, ledger cô đọng sinh ở lô trước là đầu vào của lô sau.

Tuần tự chỉ ràng buộc chiến lược thực thi bản một, không phải giới hạn kiến trúc vĩnh viễn; sản phẩm phân tích vẫn ghi xuống đĩa độc lập theo chương, tương lai có bằng chứng cho thấy gộp song song giữ được chất lượng ngữ nghĩa thì chỉ cần thay điều phối lô.

### 9.1 Đầu ra lô, sản phẩm từng chương

Bãi bỏ envelope trộn `=== TAG ===`. Mỗi lần gọi trả một đối tượng lô có cấu trúc, mỗi phần tử mảng vẫn là sự thật một chương:

```go
type ImportedChapterFacts struct {
	Chapter             int                        `json:"chapter"`
	Title               string                     `json:"title"`
	Summary             string                     `json:"summary"`
	KeyEvents           []string                   `json:"key_events"`
	CoreEvent           string                     `json:"core_event"`
	Hook                string                     `json:"hook"`
	Scenes              []string                   `json:"scenes"`
	Characters          []string                   `json:"characters"`
	CharacterEvidence   []ImportedCharacterFact    `json:"character_evidence,omitempty"`
	WorldEvidence       []ImportedWorldFact        `json:"world_evidence,omitempty"`
	TimelineEvents      []domain.TimelineEvent      `json:"timeline_events,omitempty"`
	ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates,omitempty"`
	RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes,omitempty"`
	StateChanges        []domain.StateChange       `json:"state_changes,omitempty"`
	HookType            string                     `json:"hook_type"`
	DominantStrand      string                     `json:"dominant_strand"`
}

type AnalysisBatchResult struct {
	Chapters []ImportedChapterFacts `json:"chapters"`
}

type ChapterAnalysisPayload struct {
	BatchStart int                  `json:"batch_start"`
	BatchEnd   int                  `json:"batch_end"`
	Facts      ImportedChapterFacts `json:"facts"`
}
```

Mỗi `analyses/NNNNNN.json` là `Artifact[ChapterAnalysisPayload]`. Các bản ghi chương của cùng một lô ghi xuống đĩa có cùng `BatchStart/BatchEnd`; `InputDigest` của nó dùng **ràng buộc từng chương**: danh tính phân tách (`InputDigest` của sản phẩm segmentation) + phiên bản prompt/schema + số chương + chính văn một chương. Ràng buộc theo từng chương chứ không theo lô vì ranh giới lô đổi theo năng lực đầu vào/đầu ra của model (đổi model mạnh hơn thì lô tự nhiên to ra); nếu đưa cách chia lô vào danh tính thì sau khi đổi model, phân tích đã thành công sẽ lệch toàn bộ, buộc tính lại và tính phí lặp. Ràng buộc danh tính phân tách thì bảo đảm khi "phân tách lại, đổi phiên bản prompt/schema, đổi nguồn" thì phân tích hạ nguồn tự nhiên không khớp, còn chỉ đổi model thì không bị vạ lây — đây mới là ngữ nghĩa vô hiệu mà khôi phục thật sự cần.

`ImportedCharacterFact` và `ImportedWorldFact` là quan sát cô đọng dùng cho tổng hợp toàn sách, không ghi thẳng vào nhân vật hay quy tắc thế giới chính thức. Chúng ít nhất mang số chương, để kết quả tổng hợp có nguồn ổn định.

### 9.2 Gom lô hai ngân sách

Quy hoạch lô đồng thời thoả:

```text
đầu vào ước tính + system/prompt/ledger + dự trữ suy luận + đầu ra khả kiến ước tính ≤ context window
đầu ra khả kiến ước tính ≤ giới hạn completion khả dụng của provider/model
```

- ước lượng đầu vào bao phủ tiêu đề, chính văn mỗi chương và ledger trước lô;
- ước lượng đầu ra gồm chi phí cấu trúc cố định của schema analyzer và dự trữ sự thật thận trọng mỗi chương, chỉ quyết định lần này nhét bao nhiêu chương, không cắt trường nào;
- model có reasoning token dùng chung ngân sách completion với JSON khả kiến thì phải trừ dự trữ suy luận trước;
- năng lực đầu ra provider/model càng mạnh thì lô tự nhiên càng lớn; không được viết luật cố định "mỗi lô 10/20 chương";
- khi đầu vào một chương không vào nổi context, hoặc đầu ra cấu trúc tối thiểu một chương cũng không vào nổi completion thì báo tường minh chương đó và dung lượng model, không cắt chính văn hay làm giả thành công rút gọn.

Nhờ đó tổng số chương tăng chỉ làm tăng số lô, không còn để bất kỳ phản hồi nào phình vô hạn theo quy mô toàn sách; đồng thời không bê #83 từ độ chi tiết toàn sách xuống độ chi tiết lô không bị ràng buộc đầu ra.

### 9.3 Ngữ cảnh lô

Một lệnh gọi lô chỉ gồm:

- nguyên văn và tiêu đề của khoảng chương liên tục hiện tại;
- bảng bí danh nhân vật cô đọng sinh từ các chương trước;
- ID phục bút đang hoạt động cùng trạng thái một câu;
- tóm tắt trạng thái gần đây cần thiết.

Model xử lý chương trong lô theo thứ tự mảng, có thể tiếp nối bí danh, phục bút và trạng thái trong nội bộ lô; sau khi lô kết thúc, Go cập nhật ledger cô đọng theo thứ tự sự thật đã kiểm chứng. Nó không phụ thuộc Premise toàn sách chưa sinh, cũng không đọc lại toàn bộ văn bản trước. Sự thật chương là đầu vào của Foundation, chứ không ngược lại tạo phụ thuộc vòng.

### 9.4 Kiểm chứng phản hồi đầy đủ

Mã kiểm hai tầng cấu trúc, miền giá trị và tham chiếu, không viết cứng chất lượng văn học:

- cấp lô: mảng chapters liên tục theo số chương mong đợi, không trùng, không hụt, phạm vi lô, `InputDigest` và schema version khớp;
- cấp chương: chapter/title khớp phân đoạn nguồn, summary/core_event khác rỗng, các trường tập đóng lĩnh vực chính thức như hook type, strand hợp lệ, trường dòng thời gian, phục bút và thay đổi trạng thái đúng kiểu.

Mã không yêu cầu "phải 3-6 sự kiện", "phải có nhân vật xuất hiện", "phải có ba phân cảnh". Chương tĩnh lặng, thư từ, chương môi trường hay chương không tên nhân vật đều là hình thái văn học hợp lệ.

Khi phản hồi đầy đủ có lỗi JSON hoặc lỗi kiểm ngữ nghĩa thì không commit bất kỳ chương mới nào trong đó; phản hồi lỗi cụ thể cho cùng model đó, đi thử lại tầng đầu ra ở §13.3. Model có thể viết lại các đối tượng phía trước sau khi sửa, nên lỗi kiểm thường không được tự tiện lưu một phần mảng.

### 9.5 Tiền tố liên tục khi bị cắt do độ dài

> Định vị triển khai: mục này là **tối ưu token trên đường lỗi**, không phải phụ thuộc tính đúng của khôi phục. Trong v1 (giai đoạn ba) bị cắt tức là "thất bại + thu nhỏ gộp lại lô", bản thân đã đúng và khôi phục được; vớt tiền tố liên tục hiện thực ở tiểu giai đoạn độc lập (giai đoạn ba·bổ sung), có công tắc riêng, nghiệm thu riêng.

Chỉ khi phản hồi đánh dấu rõ `StopReasonLength` và trả về văn bản phân tích được một phần, mới cho phép lưu **tiền tố hợp lệ liên tục dài nhất** từ phản hồi thất bại:

1. dùng JSON decoder streaming để vào mảng `chapters` tầng trên cùng;
2. từ chương đầu lô, đọc lần lượt các đối tượng JSON đã đóng đầy đủ;
3. mỗi đối tượng độc lập qua kiểm từng chương ở §9.4, và cùng các đối tượng trước tạo thành chuỗi liên tục từ chương đầu lô thì ghi nguyên tử ngay vào sản phẩm phân tích chương tương ứng;
4. gặp đối tượng không đầy đủ, không hợp lệ, nhảy số hoặc trùng đầu tiên thì dừng ngay, mọi byte sau đó không diễn giải;
5. cấm bù ngoặc, viết tiếp nửa JSON, đoán trường thiếu hay vớt đối tượng không liên tục từ vị trí sau;
6. phản hồi gốc, StopReason, phạm vi tiền tố đã lưu và chương thất bại đầu tiên đều ghi vào failure artifact, sự kiện và log;
7. `NextAction` gom lại lô từ phân tích thiếu đầu tiên, không làm lại tiền tố hợp lệ đã commit.

typed-call phải ghi lần này có lấy được văn bản một phần dùng được không: chế độ có cấu trúc không streaming như JSON Schema có thể không cho tiền tố phân tích được khi dừng do độ dài. Nếu provider không trả văn bản một phần, không chứng minh được rõ là bị cắt do độ dài, hoặc không hoàn tất được một đối tượng hợp lệ nào thì không lưu kết quả nào, phát sự kiện/log `prefix_salvage=unavailable` và lùi về "thất bại + thu nhỏ gộp lại lô", chứ không quay vô ích im lặng. Khi lô một chương vẫn bị cắt thì báo thẳng năng lực đầu ra của model không đủ, không thu nhỏ vòng lặp hay tạo sự thật rỗng.

Cắt do độ dài là lỗi dung lượng, không vào vòng tự sửa ngữ nghĩa kiểu "phản hồi lỗi kiểm cho cùng model", cũng không thử lại nguyên dạng cùng lô.

### 9.6 Khôi phục

Mỗi chương phân tích thành công là ghi nguyên tử `analyses/NNNNNN.json`. Sau sập:

- phân tích khớp `InputDigest` dùng lại thẳng, không tính phí lặp;
- phân tích thiếu hoặc không khớp đầu tiên trở thành điểm bắt đầu lô kế tiếp;
- sau khi đầu vào ngữ nghĩa thượng nguồn đổi, phân tích không dựng lại được cùng `InputDigest` tự nhiên mất hiệu lực;
- tiền tố hợp lệ liên tục đã commit khi bị cắt do độ dài và sản phẩm hoàn thành bình thường dùng cùng quy tắc khôi phục;
- không cho người dùng vượt qua một chương thất bại để tiếp tục sinh sự thật ngữ nghĩa phía sau không liên tục.

## 10. Tổng hợp theo tầng

### 10.1 Vì sao không thể làm đầu ra đơn lần cho cả sách nữa

Tổng hợp toàn sách cần hiểu xuyên chương, nhưng không cần đọc lại toàn bộ chính văn, cũng không nên xuất đối tượng chi tiết từng chương. Sự thật từng chương đã chứa ngữ nghĩa cấp chương; tổng hợp chỉ xử lý các sự thật cô đọng đó.

### 10.2 Hình dạng Map/Reduce

```text
ImportedChapterFacts × N
        ↓ chia khoảng liên tục theo context window hiện tại
RangeDigest × M
        ↓ khi cần thì gộp tiếp
BookSynthesis
```

Sách ngắn nếu một lần chứa hết sự thật các chương thì sinh thẳng `BookSynthesis`; sách dài mới sinh `RangeDigest`. Việc phân tầng hay không do ngân sách token quyết tất định, không do ngưỡng số chương.

`RangeDigest` chứa diễn tiến cốt truyện của khoảng liên tục đó, thay đổi nhân vật, sự thật thế giới, phục bút đã mở/đã thu và ranh giới cấu trúc ứng viên. Kích thước đầu ra của nó bị ràng buộc bởi một khoảng; tổng hợp cuối không xuất lại N đối tượng chi tiết chương nữa, chỉ xuất sự thật toàn cục và phạm vi tập-cung.

### 10.3 Kết quả tổng hợp cuối cùng

```go
type BookSynthesis struct {
	Title         *string                `json:"title"`    // null khi chính văn không xác nhận được, suy từ tên file
	Synopsis      string                 `json:"synopsis"` // giới thiệu không spoil hướng độc giả
	Premise       string                 `json:"premise"`
	Characters    []domain.Character     `json:"characters"`
	WorldRules    []domain.WorldRule     `json:"world_rules"`
	Structure     []ImportedVolumeRange  `json:"structure"`
	Compass       domain.StoryCompass    `json:"compass"`
	PlanningTier  domain.PlanningTier    `json:"planning_tier"`
	StoryStatus   string                 `json:"story_status"` // open / closed / uncertain
	StatusReason  string                 `json:"status_reason"`
}
```

Cấu trúc chỉ trả phạm vi, không xuất lại mọi chương:

```go
type ImportedVolumeRange struct {
	Title string             `json:"title"`
	Theme string             `json:"theme"`
	Arcs  []ImportedArcRange `json:"arcs"`
}

type ImportedArcRange struct {
	Title        string `json:"title"`
	Goal         string `json:"goal"`
	StartChapter int    `json:"start_chapter"`
	EndChapter   int    `json:"end_chapter"`
}
```

Model tự quyết số tập và số cung, có thể tham khảo tiêu đề group trong file nguồn, nhưng không bị giới hạn "một tập", "1-3 cung". Go dùng title/core_event/hook/scenes của `ImportedChapterFacts` để lắp `OutlineEntry` chính thức.

### 10.4 Trạng thái truyện

Nhập liệu chỉ dựng lại sự thật chính văn, không vì để Engine viết tiếp mà bịa tuyến dài chưa khép:

- `open`: chính văn có mục tiêu hoặc căng thẳng chưa khép thật, sinh Compass bình thường;
- `closed`: phát hành theo tác phẩm đã kết thúc, tập cuối đánh dấu Final; cần viết tiếp thì người dùng reopen tường minh và đưa hướng mới;
- `uncertain`: trước phát hành yêu cầu người dùng chọn xử lý theo chưa xong hay đã kết thúc; nếu Intent đã lưu lựa chọn qua `--story=open|closed` thì dùng thẳng, nếu không thì vào chờ tương tác. Lựa chọn được lưu thành sản phẩm `story-resolution.json` lấy synthesis hiện tại làm đầu vào.

Mã không lén đoán ý định người dùng qua việc `open_threads` có rỗng không.

## 11. Lắp ghép và kiểm chứng Foundation

Model xuất ngữ nghĩa tổng hợp, Go chịu trách nhiệm lắp đối tượng lĩnh vực chính thức. Trước phát hành phải thoả:

1. Premise có tiêu đề tên sách hợp lệ; khi chính văn không xác nhận được tên sách thì dùng basename file nguồn, và ghi nguồn là filename, không để model tuyên bố nó là "tên sách thật";
2. mọi phạm vi tập và cung liên tục theo thứ tự;
3. phạm vi đầu bắt đầu từ chương 1, phạm vi cuối kết thúc ở chương N;
4. mỗi chương thuộc đúng một cung;
5. sau `FlattenOutline` số chương là N, tiêu đề khớp sự thật từng chương;
6. tên nhân vật, quy tắc thế giới và Compass thoả ràng buộc kiểu lĩnh vực sẵn có;
7. PlanningTier là giá trị tập đóng hợp lệ, nhưng lý do chọn đến từ model chứ không từ ngưỡng số chương;
8. trạng thái closed/open khớp hình dạng phát hành của Final và Compass;
9. `InputDigest` của sản phẩm Synthesis dựng lại được từ tập phân tích có thứ tự hiện tại.

Khi vi phạm ràng buộc cấu trúc thì phản hồi lỗi cụ thể cho model sinh lại, tiếp tục tới khi thành công hoặc context bị hủy; không ghi xuống đĩa sản phẩm dở dang.

## 12. Phát hành chính thức

### 12.1 Điều kiện tiên quyết phát hành

Lần nhập mới chỉ được vào khi:

- không có chương đã hoàn thành;
- không có chương đang trên đường hoặc PendingCommit;
- không có workspace nhập khác không cùng nguồn;
- Foundation chính thức rỗng, hoặc khớp hoàn toàn digest đã phát hành của workspace hiện tại.

Ngữ nghĩa gộp tiểu thuyết đã có với văn bản ngoài mới không rõ ràng, bản một từ chối tường minh, không đoán mò ghi đè hay nối thêm.

### 12.2 Phát hành Foundation

Phát hành theo thứ tự phụ thuộc chính thức:

```text
planning tier
→ premise
→ characters
→ world rules
→ layered outline + flat outline
→ compass
→ đối chiếu progress
```

Mỗi bước:

1. tính digest nội dung cần phát hành;
2. sản phẩm chính thức chưa có thì ghi nguyên tử và nối checkpoint;
3. đã có và digest giống thì bỏ qua idempotent;
4. đã có nhưng khác thì trả lỗi xung đột, không ghi đè.

Sau khi sập giữa chừng chỉ cần đối chiếu lại từ mục đầu, không cần giao dịch xuyên file hay máy trạng thái Foundation Pending.

### 12.3 Phát hành chương

Theo thứ tự chương dùng lại luồng sẵn có:

```text
lưu draft
→ Progress.StartChapter
→ commit_chapter (sự thật từng chương)
```

`commit_chapter` đã có saga PendingCommit, checkpoint và kiểm idempotent chương hoàn thành. Nhập liệu không sao chép bộ logic commit thứ hai.

Cửa sổ sập:

| Cửa sổ | Hành vi khôi phục |
|---|---|
| trước draft | lưu lại cùng chính văn |
| sau draft, trước StartChapter | đối chiếu digest rồi tiếp |
| sau StartChapter, trước PendingCommit | thực thi lại commit cùng chương |
| trong PendingCommit | do saga commit sẵn có khôi phục |
| sau khi chương complete | digest/checkpoint khớp thì bỏ qua |
| nội dung chính thức xung đột digest nguồn | dừng tường minh, báo chương xung đột |

### 12.4 Ranh giới hoàn tất nhập

Sau khi mọi chương commit ổn định thì đặt một lần `AdvanceHoldAtBoundary`, lý do rõ là "nhập tiểu thuyết bên ngoài hoàn tất, chờ nghiệm thu để viết tiếp". Nó chỉ bảo vệ lần nhập xuyên hệ thống này, không đổi chế độ `auto/review` dài hạn của người dùng.

`--yes` chỉ uỷ quyền tự động chấp nhận phân tách, không được ngầm bỏ qua Hold này. Chỉ khi người dùng truyền thêm `--continue` độc lập thì Runner mới không tạo Hold riêng cho nhập; sau đó vẫn tuân advance mode bình thường: `auto` được viết tiếp, `review` vẫn chờ `/next`.

TUI mặc định không còn tự tiếp sức không nhắc. Người dùng kiểm Foundation và trạng thái chương xong thì dùng cửa vào tiếp tục sẵn có để khôi phục sáng tác.

**Điểm rơi khi đóng panel**: sau khi lần nhập phát sinh từ trang chào kết thúc thành công, Esc đóng panel sẽ chạy bù một lần `Resume()` (cổng khôi phục của bootstrap chỉ chạy một lần lúc khởi động), người dùng rơi thẳng vào workbench, bị Hold hoàn tất nhập chặn ở ranh giới chương kế tiếp chờ nghiệm thu — chứ không ở lại trang chào nơi lỡ nhấn Enter là "mở sách mới".

**Đường phòng vệ tạo mới**: `PrepareUserRules` / `StartPrepared` từ chối tạo mới khi thư mục sách đã có chương hoàn thành (`CompletedChapters` khác rỗng) — StartPrepared mở đầu là reset checkpoints và progress, lỡ chạm sẽ âm thầm xóa sạch cả cuốn sách (gồm toàn bộ chương vừa nhập). Phần quy hoạch còn sót chưa có chương hoàn thành thì cho qua, giữ đường tự lành cho việc thử lại Ctrl+S trong cùng phiên đồng sáng tác và quyết bù khi khôi phục.

### 12.5 Cổng phát hành xuyên khởi động lại

Khi workspace tồn tại và `NextAction != done`, `Host.New/Resume` phải nhận diện sách là lần nhập chưa hoàn tất:

- cho phép xem, chẩn đoán và chạy `/import` khôi phục;
- cấm Engine khởi động thường, Continue hoặc giao Writer;
- hiển thị rõ hành động khôi phục hiện tại, không coi Foundation/chương đã phát hành một phần là một cuốn sách ngữ nghĩa đầy đủ viết tiếp được.

Cổng suy trực tiếp từ workspace và Store chính thức, không thêm `published bool`. Nhờ đó sập ở bất kỳ cửa sổ phát hành nào cũng không bị luồng sáng tác thường tiêu thụ trạng thái nửa phát hành khi Runner chưa khôi phục.

## 13. Lõi gọi model

`imp` giữ nội bộ một helper typed-call nhỏ và chuyên dụng, không dựng khung luồng công việc LLM tổng quát.

### 13.1 Chọn model

- mặc định dùng model của vai trò architect;
- mức model của hàm ngữ nghĩa là núm vặn mở: segment/analyze/synthesize có thể khai mức riêng, mặc định rơi vào architect, tầng cấu hình có thể trỏ segment mang tính cơ học hơn sang mức rẻ hơn. Đây là cấu hình gọi, không đổi contract ngữ nghĩa nào, cũng không viết "một vai duy nhất" thành tiền đề kiến trúc — mục đích là để lợi ích chi phí từ "model mức rẻ trở nên mạnh hơn" cũng vào được;
  - điểm neo hiện thực: cấu hình roles hỗ trợ ba khóa `import_segment` / `import_analyze` / `import_synthesize`; chưa cấu hình thì rơi vào architect. Hai ngân sách và tùy chọn thinking/có cấu trúc của mỗi hàm dẫn xuất độc lập theo năng lực thật của mức tương ứng (cửa sổ nhỏ của mức nhỏ chỉ ràng buộc hàm của nó), lượng dùng tính sổ theo vai trò của mức thật;
- nối failover đã cấu hình của vai trò đã chọn;
- dùng reasoning effort của vai trò đã chọn, và qua dò năng lực quyết định có gửi tham số thinking không;
- ghi metadata session và usage theo provider/model thật;
- vào sentinel ngân sách sẵn có (lên sóng 2026-07-16): trước khởi động qua `Refuse()` cùng kỷ luật với Start/Resume/Continue; ngân sách dừng cứng lúc chạy thì qua `abortWithEvent` hủy context của chính việc nhập (Host đăng ký cancel cho công việc độc chiếm, không còn chỉ tạm dừng Engine vốn không chạy).

### 13.2 Năng lực đầu ra có cấu trúc

Bốn loại sản phẩm nhập dùng chung `llmcontract.Execute`: khi model hoặc cấu hình người dùng khai hỗ trợ rõ thì gửi JSON Schema gốc; khi năng lực chưa biết hoặc khai không hỗ trợ thì tự sinh Prompt Contract từ cùng một Schema. Chế độ gốc kiểm phản hồi đầy đủ, chế độ tương thích mới trích đối tượng JSON cân bằng; cả hai đường đều chạy cùng một phép kiểm Schema trước, rồi giải mã DTO và kiểm nghiệp vụ. Sau lỗi yêu cầu không được xóa schema im lặng rồi thử lại, phán đoán năng lực sai hoặc provider từ chối phải lộ ra.

### 13.3 Tách lỗi yêu cầu, ngữ nghĩa và dung lượng

- lỗi tầng yêu cầu: chỉ thử lại lỗi timeout, giới hạn tần suất, mạng mà adapter đánh dấu rõ retryable, theo ngữ nghĩa backoff sẵn có và tiếp tục tới khi thành công hoặc context bị hủy;
- lỗi tầng đầu ra: phản hồi lỗi cụ thể của JSON parse hoặc Validate cho cùng model, tự sửa liên tục tới khi thành công hoặc context bị người dùng/hệ ngân sách hủy; vi phạm contract Schema gốc, từ chối trả lời và cắt cụt không hỏi lại mù;
- thử lại không được im lặng: mỗi lần backoff tầng yêu cầu ("đang thử lại lần N · thử lại sau Xs") và mỗi lần hỏi lại tầng đầu ra đều hiển thị bằng sự kiện tiến độ lên panel nhập — không hiển thị thì người dùng tưởng kẹt. Sự kiện backoff chỉ mang thời điểm hết hạn (`RetryAt`), số giây còn lại do tầng kết xuất tính theo tick để tạo đếm ngược thời gian thực (dùng chung cơ chế với panel sự kiện của bàn sáng tác); trong lúc panel chạy có spinner thường trú trên đỉnh + thời gian đã dùng, đuôi log có thêm con trỏ sao kiểu streaming tương tự.
- hiển thị lỗi không được chung chung: message của gateway thường chỉ có một câu "Provider returned error"; phần hiển thị và văn bản thất bại đều đính kèm sự thật có cấu trúc của adapter (phân loại lỗi/HTTP status/provider/model, `modelErrDetail` lấy từ chuỗi lỗi litellm qua errors.As), sự thật đứng trước, khi cắt thì ưu tiên giữ lại.
- giai đoạn dài không được im lặng: phân tách từng khối, tổng hợp từng khoảng gọi model bên trong hàm (một khối có thể vài phút), qua `callProfile.step` hiển thị tiến triển từng khối/từng khoảng ("đang phân tách khối N/M, đã nhận diện K ranh giới"). Key sự kiện chỉ cấp cho backoff yêu cầu (trạng thái tạm trong cùng lệnh gọi, nhảy tại chỗ); hỏi lại kiểm chứng là sự kiện ngữ nghĩa xuyên lệnh gọi, mỗi cái thành một dòng giữ lịch sử — dùng chung Key sẽ khiến khối sau ghi đè khối trước, mất hết đầu mối chẩn đoán.
- ghi toàn bộ log: mọi sự kiện tiến độ (gồm dòng thử lại bị panel ghi đè tại chỗ) ghi vào **log riêng của nhập** `<gốc sách>/logs/import.log` (không trộn với tui.log, một lần nhập một file để xem trọn bản ghi); backoff yêu cầu và hỏi lại ngữ nghĩa ghi thêm chuỗi lỗi đầy đủ vào cùng log.
- hiển thị ngữ nghĩa model chứ không chỉ đếm cơ học: phân tách từng khối hiển thị tiêu đề model nhận diện được ("model nhận diện được: Chương mười hai Đêm gió tuyết / … (tổng N chỗ)"), phân tích từng chương hiển thị sự kiện cốt lõi ("Chương 12 <Đêm gió tuyết>: ……"), tổng hợp xong hiển thị khái quát toàn sách (tóm tắt premise) — người dùng nên thấy model đã hiểu được gì.
- lỗi dung lượng: `StopReasonLength` không thử lại nguyên dạng, cũng không vào vòng tự sửa ngữ nghĩa; lô analysis khi văn bản một phần phân tích được thì lưu tiền tố hợp lệ liên tục theo §9.5, nếu không thì ghi `prefix_salvage=unavailable` và thu nhỏ gộp lại lô; các hàm ngữ nghĩa còn lại thất bại tường minh và giữ phản hồi gốc.

Xác thực, quyền, model không hỗ trợ và xung đột trạng thái thất bại ngay. Không mô phỏng thành công, không lùi dự phòng đối tượng rỗng hay bỏ qua chương thất bại.

### 13.4 Ngân sách đầu vào và đầu ra

Mỗi hàm ngữ nghĩa có schema, ngân sách đầu vào, dự trữ suy luận và ngân sách đầu ra khả kiến độc lập:

- đầu ra phân đoạn chỉ chứa ranh giới của owned range hiện tại;
- lô analysis đồng thời bị ràng buộc bởi context window và giới hạn completion, đầu ra là sự thật từng chương trong một khoảng liên tục hữu hạn;
- RangeDigest chỉ chứa một khoảng liên tục;
- BookSynthesis chỉ chứa sự thật toàn cục và phạm vi tập-cung, không lặp đối tượng chương.

Mỗi yêu cầu trước khi gửi đều ghi ước lượng đầu vào, dự trữ suy luận, max tokens xin và đầu ra khả kiến ước tính. Ước lượng chỉ quyết định chia khối/gom lô, không xóa chính văn hay trường sự thật. Nên không tồn tại cấu trúc "tổng số chương càng nhiều thì một phản hồi nào đó ắt dài hơn", cũng không thể chỉ vì đầu vào chứa nổi mà bỏ qua rủi ro cắt cụt đầu ra.

## 14. Sự kiện, log và chẩn đoán

### 14.1 Giai đoạn sự kiện

```go
const (
	StageIngesting            Stage = "ingesting"
	StageSegmenting           Stage = "segmenting"
	StageAwaitingConfirmation Stage = "awaiting_confirmation"
	StageAnalyzing            Stage = "analyzing"
	StageSynthesizing         Stage = "synthesizing"
	StageAwaitingStoryStatus  Stage = "awaiting_story_status"
	StageValidating           Stage = "validating"
	StagePublishing           Stage = "publishing"
	StageDone                 Stage = "done"
	StageError                Stage = "error"
)
```

Mỗi sự kiện gồm action, chương/khoảng hiện tại, tổng số, thời gian và lỗi tùy chọn. Sự kiện lô analysis thêm phạm vi lô, ước lượng ngân sách, StopReason và phạm vi tiền tố đã commit. Event là bản chiếu, không tham gia khôi phục.

### 14.2 Lỗi phải tới đồng thời ba chỗ

1. panel nhập của TUI: tự xuống dòng, giữ chuỗi lỗi đầy đủ;
2. `tui.log`: ghi có cấu trúc stage, chapter/range, model, attempt và error;
3. `meta/import/failures/`: lưu metadata thất bại cuối cùng và phản hồi model chưa cắt tỉa.

Chính văn tiểu thuyết gốc không ghi vào log thường, cũng không vào phần xuất chẩn đoán khử nhạy cảm mặc định. Phản hồi thất bại nằm trong thư mục sách của chính người dùng, thông báo lỗi ghi rõ đường dẫn.

### 14.3 Session và Usage

Mỗi lệnh gọi ngữ nghĩa ghi:

- tên task ổn định, như `import/segment/0003`, `import/analyze/0054-0061`;
- phản hồi gốc của assistant;
- provider/model và usage;
- structured mode, thinking level và kết quả kiểm đầu ra.

Lượng dùng vào thống nhất vai trò architect, ngân sách thấy được chi phí nhập.

## 15. Vòng đời và đồng thời

- nhập loại trừ lẫn nhau với Engine, đồng sáng tác giai đoạn và thao tác ghi của simulation;
- trong lúc nhập, cùng một cuốn sách chỉ cho một Runner;
- người dùng hủy sẽ hủy lệnh gọi model đang trên đường, sự thật workspace đã ghi nguyên tử được giữ;
- hủy trước xác nhận không sửa Store chính thức;
- hủy sau khi phát hành bắt đầu thì không rollback đoán mò, lần sau chỉ khôi phục phát hành chính xác;
- bản một chạy tuần tự giữa các lô analysis, trong lô do một lệnh gọi model trả sự thật theo thứ tự chương; phát hành chính thức vẫn tuần tự theo chương;
- `Host.New/Resume` chạy cổng §12.5 khi nhập chưa xong, ngữ nghĩa loại trừ vẫn đúng qua khởi động lại tiến trình;
- xuất có được đồng thời không giữ ngữ nghĩa chỉ đọc sẵn có, nhưng nó chỉ thấy chương đã phát hành chính thức.

## 16. Bất biến cốt lõi

1. Mỗi sản phẩm workspace được định danh bằng `SchemaVersion + InputDigest + Payload`; chỉ dùng lại khi dựng lại được cùng `InputDigest` từ đầu vào ngữ nghĩa thật hiện tại.
2. Manifest ứng với duy nhất một ảnh chụp nguồn chuẩn hóa; mỗi đoạn văn bản nguồn khác rỗng có đúng một chủ.
3. Model chỉ được tham chiếu SourceUnit, điểm neo nguyên văn và số chương do Host cung cấp; Go chỉ nhận tọa độ ánh xạ duy nhất được về byte nguồn.
4. Lô analysis chỉ được commit phản hồi đầy đủ, hoặc tiền tố hợp lệ liên tục dài nhất từ chương đầu dưới `StopReasonLength`; bất kỳ chương thiếu nào cũng chặn phân tích và tổng hợp phía sau.
5. Phạm vi tập-cung phải liên tục, không chồng lấn và bao phủ đầy đủ `1..N`; Foundation chính thức chỉ được phát hành từ Synthesis đã qua kiểm chứng đầy đủ.
6. Chương chính thức chỉ được phát hành theo thứ tự qua `commit_chapter`; sản phẩm chính thức đã có chỉ được dùng lại idempotent khi digest nội dung giống, khác thì thất bại xung đột.
7. Bất kỳ thất bại model nào cũng không được diễn giải thành "không có nội dung" hay "đi tiếp chương sau", không được sửa nửa JSON hay bỏ qua chương thất bại.
8. `done` phải được chứng minh đồng thời bằng sản phẩm workspace, sản phẩm chính thức, Progress, PendingCommit và checkpoint; trước `done`, Engine thường không được khởi động.

## 17. Cấu trúc package và giao diện hẹp

Giữ `internal/host/imp`, chia theo trách nhiệm:

```text
imp/
├── types.go       Options/Event công khai và DTO ngữ nghĩa
├── source.go      đọc, giải mã, chuẩn hóa, SourceUnit/anchor
├── workspace.go   sản phẩm nguyên tử meta/import và InputDigest
├── call.go        gọi LLM typed dành riêng cho import
├── segment.go     chiếu cấu trúc, hàm ngữ nghĩa ranh giới, kiểm bao phủ
├── analyze.go     lô liên tục hai ngân sách, sự thật từng chương và tiền tố bị cắt
├── synthesize.go  RangeDigest và BookSynthesis
├── publish.go     đối chiếu Foundation và phát hành commit_chapter
└── runner.go      LoadState → NextAction → thực thi
```

Không thêm `ImportEngine`, `Task`, `WorkflowInstance`, Repository tổng quát hay bảng đăng ký plugin.

Phụ thuộc Host bơm vào giữ hẹp:

```go
type Deps struct {
	Store         *store.Store
	CommitChapter ChapterCommitter
	Model         agentcore.ChatModel
	Runtime       ModelRuntime
	Prompts       Prompts
	Emit          func(Event)
}
```

`ModelRuntime` chỉ mang sự thật gọi như context window, giới hạn completion, thinking, callback session/usage, và chừa ô chọn mức model cho mỗi hàm ngữ nghĩa (mặc định architect); không để `imp` phụ thuộc ngược vào cả Host, cũng không hàn chết vai trò duy nhất thành tiền đề kiến trúc.

## 18. Giao diện người dùng

### 18.1 Nhập mới

```text
/import <path> [--yes] [--story=open|closed] [--continue] [--guide=<hướng dẫn phân tách>]
```

Hành vi mặc định: tạo ảnh chụp nguồn, phân tách ngữ nghĩa và mở bản xem trước xác nhận, sau khi phát hành xong thì đặt một lần Hold riêng cho nhập. Xóa `from=N`.

Ba tùy chọn đầu là uỷ quyền tường minh độc lập với nhau, và ghi vào `intent.json`:

- `--yes`: sau khi kiểm bao phủ đạt thì tự động chấp nhận phân tách; không quyết trạng thái truyện uncertain, không bỏ qua Hold hoàn tất;
- `--story=open|closed`: chỉ cung cấp trước lựa chọn của người dùng khi synthesis trả uncertain; khi model đã phán rõ open/closed thì không ghi đè sự thật model;
- `--continue`: không tạo Hold riêng cho nhập; không vòng qua advance mode bình thường, dưới `review` vẫn cần `/next`.

`--guide` khác ba cái trên: nó không phải uỷ quyền khởi động mà là đầu vào ngữ nghĩa của phân tách, ghi thành `guidance.txt` trong workspace (được chứa khoảng trắng, phải đặt cuối lệnh). Xem §18.3.

Nên `/import book.txt --yes` vẫn sẽ dừng sau khi nhập xong; chỉ khi truyền thêm `--continue` mới uỷ quyền cho luồng sáng tác tiếp sức khi cổng bình thường cho phép.

### 18.2 Khôi phục

Khi cùng một cuốn sách có workspace hoạt động thì chạy `/import` không tham số, suy bước tiếp thẳng từ sự thật và intent đã lưu; đường dẫn file nguồn và tham số khởi động đều không bắt buộc để khôi phục. `/import <path>` với đường dẫn mới không được ghi đè workspace hoạt động.

Nhập chưa hoàn tất phải chủ động thấy được, không đợi người dùng bị cổng từ chối mới lộ. Hiện thực thành ba lớp nhắc tiệm tiến:

1. lúc khởi động TUI kiểm một lần (`imp.ResumeSummary`, sinh mô tả theo giai đoạn từ `NextAction`), màn hình chào hiển thị nổi bật "phát hiện lần nhập chưa hoàn tất (đã phân tích N/M chương), nhập /import để khôi phục từ điểm dừng";
2. khi người dùng phớt lờ nhắc mà thử sáng tác, cổng xuyên khởi động lại (§12.5) từ chối khởi động engine và phát sự kiện cảnh báo;
3. trong lúc chạy khôi phục, panel nhập hiển thị giai đoạn và tiến độ hiện tại theo thời gian thực.

### 18.3 Phân tách lại

Sau khi người dùng đối chiếu xem trước, dùng `/import --guide=<giải thích ngôn ngữ tự nhiên>` để nhận diện lại, ví dụ `--guide=Đoạn chuyển cảnh X cũng là chương độc lập`. Hướng dẫn ghi vào `guidance.txt` trong workspace và vào `InputDigest` của segmentation: hướng dẫn đổi khiến phân tách cũ, confirmation cũ, phân tích và synthesis không dựng lại được cùng `InputDigest`, tự nhiên làm lại hết; không cung cấp trình soạn regex.

### 18.4 Hủy

Hủy trước xác nhận chỉ giữ workspace; trước phát hành có thể bỏ toàn bộ workspace tường minh. Sau khi phát hành bắt đầu không cung cấp thao tác bỏ "giả vờ chưa có gì xảy ra", chỉ cho khôi phục hoàn tất hoặc để người dùng tự xử lý sách chính thức.

## 19. Thứ tự triển khai

### Giai đoạn một: workspace và suy trạng thái thuần

- Manifest, Intent, ảnh chụp nguồn, `Artifact/InputDigest`, đọc ghi nguyên tử;
- `LoadState/NextAction`;
- kiểm tiên quyết sách rỗng và khôi phục cùng nguồn;
- xóa phụ thuộc thiết kế `ResumeFrom`.

Giai đoạn này không gọi model, trước tiên chứng minh sự thật khôi phục không mơ hồ.

### Giai đoạn hai: phân tách ngữ nghĩa và xác nhận

- SourceUnit, phân mảnh ảo dòng quá dài, điểm neo nguyên văn và chia khối theo ngân sách ngữ cảnh;
- gọi typed BoundaryDecision;
- kiểm bao phủ toàn văn;
- xem trước TUI, nhận diện lại bằng ngôn ngữ tự nhiên, `--yes` và sản phẩm confirmation.

Trước tiên dùng tiêu đề phi chuẩn, tiêu đề tập, lời nói đầu và chú thích cuối để kiểm chứng "không sót một chữ".

### Giai đoạn ba: sự thật từng chương theo lô liên tục

- `ImportedChapterFacts`;
- quy hoạch lô hai ngân sách context/completion;
- phân tích tuần tự giữa lô và ledger liên tục cô đọng;
- khôi phục sản phẩm `InputDigest` từng chương;
- bị cắt tức là "thất bại + thu nhỏ gộp lại lô", và ghi văn bản một phần có dùng được không;
- nối session, usage, failover, thinking, lỗi dung lượng và thử lại phản hồi cấu trúc.

### Giai đoạn ba·bổ sung: vớt tiền tố bị cắt (tối ưu hiệu quả, có thể để sau)

- phân tích tiền tố hợp lệ liên tục dưới `StopReasonLength` (§9.5);
- chỉ bật khi văn bản một phần phân tích được, không đổi tính đúng của khôi phục; công tắc riêng, nghiệm thu riêng.

### Giai đoạn bốn: tổng hợp theo tầng và Foundation

- RangeDigest nhận biết ngữ cảnh;
- BookSynthesis;
- cấu trúc tập-cung dạng phạm vi;
- StoryStatus;
- lắp ghép và kiểm chứng Foundation đầy đủ.

### Giai đoạn năm: phát hành và bàn giao

- đối chiếu digest từng sản phẩm Foundation;
- dùng lại `commit_chapter` để phát hành;
- hủy/khôi phục sau sập;
- cổng Engine xuyên khởi động lại;
- AdvanceHold mặc định khi nhập xong và `--continue` tường minh;
- artifact TUI/log/failure đầy đủ.

### Giai đoạn sáu: xóa hiện thực cũ

- xóa phán định định dạng chương trong `splitter.go`;
- xóa envelope tagged;
- xóa lệnh gọi cả sách `ReverseFoundation`;
- xóa ngưỡng số chương `pickScale`;
- xóa `ResumeFrom/from=N`;
- xóa ràng buộc prompt "cố định một tập, 1-3 cung, ép open threads";
- sau khi hiện thực xong mới cập nhật mô tả luồng cũ trong README và architecture.

## 20. Kiểm tra và nghiệm thu

### 20.1 Test hàm thuần và thuộc tính

- mọi segmentation hợp lệ đều thoả phạm vi toàn văn không chồng lấn, không hụt;
- SourceUnit không hợp lệ, điểm neo nguyên văn không duy nhất, ranh giới đảo thứ tự và trùng lặp đều bắt buộc bị từ chối;
- thứ tự ranh giới phán theo thứ tự số `(Line, Part)`; dựng tập unit "kết luận từ điển và kết luận số ngược nhau", khẳng định qua theo thứ tự số;
- dòng thường và phân mảnh ảo đều ánh xạ không mất về cùng byte nguồn chuẩn hóa;
- mọi ranges tập-cung hợp lệ bao phủ đúng `1..N`;
- cùng đầu vào ngữ nghĩa sinh ổn định cùng `InputDigest`, bất kỳ đầu vào thật nào đổi đều khiến sản phẩm tương ứng không khớp;
- gom lô hai ngân sách không vượt ràng buộc context/completion cho trước;
- NextAction bất biến với cùng một ảnh chụp sự thật.

Làm fuzz/property test cho ánh xạ tọa độ, lắp phạm vi, ngân sách lô và `InputDigest`, không khẳng định model sẽ xuất một tiêu đề cố định.

### 20.2 Test contract model

- tên chương phi chuẩn và cấu trúc tập-chương trộn;
- mở đầu/dẫn nhập/ngoại truyện được model phán ngữ nghĩa là chương;
- front/back matter hiển thị rõ chứ không bị bỏ;
- cả sách một dòng, một dòng nhiều chương và dòng vượt ngân sách đều phân tách chính xác qua SourceUnit + anchor;
- chương tĩnh lặng được phép characters rỗng;
- JSON không hợp lệ, thiếu trường, phạm vi vượt biên vào thử lại kèm phản hồi;
- lô analysis trả đối tượng từng chương liên tục, không được nhảy số hoặc trùng;
- `StopReasonLength` chỉ lưu tiền tố hợp lệ liên tục dài nhất, đối tượng nửa vời và đối tượng không liên tục phía sau không lưu;
- khi chế độ có cấu trúc không sinh văn bản một phần phân tích được, khẳng định đi "thất bại + thu nhỏ gộp lại lô" và log đánh `prefix_salvage=unavailable`;
- JSON hỏng dưới `StopReasonStop` thường không đi đường tiền tố bị cắt;
- một chương vẫn bị cắt thì thất bại tường minh, không sinh sự thật rỗng;
- lỗi phân tích/nghiệp vụ của Prompt Contract được phản hồi tự sửa liên tục, tới khi thành công hoặc context bị hủy; vi phạm contract Schema gốc, từ chối trả lời và cắt cụt giữ phản hồi gốc và kết thúc ngay;
- model không hỗ trợ thinking/JSON Schema không nhận tham số không hợp lệ.

Test model khẳng định contract và bất biến, không khẳng định phán đoán văn học chính xác.

### 20.3 Ma trận sập

Ít nhất bao phủ:

- sau ảnh chụp nguồn;
- sau segmentation, trước xác nhận;
- trước và sau khi đối tượng thứ N của lô analysis ghi xuống đĩa;
- trước và sau khi chương cuối tiền tố bị cắt do độ dài ghi xuống đĩa;
- giữa RangeDigest;
- sau Synthesis, trước Foundation;
- trước và sau từng sản phẩm Foundation;
- các cửa sổ draft/StartChapter/PendingCommit/progress/checkpoint;
- sau khi chương cuối commit, trước và sau AdvanceHold;
- sau khi Foundation/chương phát hành một phần, khởi động lại và thử Host.Resume thường.

Mỗi cửa sổ sau khởi động lại chỉ được tiếp hành động hiện tại, không được tiêu thụ lặp lệnh gọi model đã thành công, cũng không được vượt qua sản phẩm thất bại. Khi `NextAction != done` thì Engine thường phải bị cổng chặn cho tới khi khôi phục nhập xong.

### 20.4 Hình dạng hồi quy #83

Dựng đầu vào 54 chương và dài hơn, kiểm chứng:

1. không có lệnh gọi đơn lần nào yêu cầu xuất đại cương chi tiết 54 chương;
2. lô analysis đồng thời gom theo ngân sách context đầu vào và completion đầu ra khả kiến, không nhét quá nhiều chương chỉ vì đầu vào chứa nổi;
3. giai đoạn ba·bổ sung: mô phỏng `StopReasonLength` "13 chương đầu đầy đủ, chương 14 bị cắt", chỉ commit 13 chương đầu, hành động sau bắt đầu từ chương 14; khi chưa hiện thực vớt tiền tố thì cả lô thất bại rồi gom lại từ chương đầu lô;
4. mô phỏng phản hồi bị cắt không có đối tượng đầy đủ, lỗi hiển thị đầy đủ, ghi log, lưu phản hồi gốc và không ghi sản phẩm phân tích;
5. mô phỏng JSON hỏng thường, đi thử lại phản hồi cấu trúc chứ không vớt tiền tố;
6. sau khi sửa chỉ chạy lại hành động thiếu đầu tiên, không làm lại chương đã xong;
7. tiêu đề phi chuẩn vào xem trước qua phân tách ngữ nghĩa, không sửa bằng cách thêm regex.

### 20.5 Tiêu chí nghiệm thu cuối

1. Chế độ tương tác mặc định để người dùng thấy và xác nhận mọi ranh giới chương trước khi ghi chính thức; `--yes` tự động chấp nhận tường minh và để lại sản phẩm audit tương đương.
2. Mọi văn bản nguồn khác rỗng đều tìm được chủ duy nhất từ segmentation.
3. 200-500 chương không hình thành lệnh gọi model đọc toàn bộ chính văn và xuất toàn bộ đối tượng chương; mỗi lô phân tích đồng thời bị ràng buộc bởi ngân sách đầu vào và đầu ra, đầu ra toàn cục chỉ biểu đạt sự thật toàn cục và phạm vi tập-cung.
4. Sập ở bất kỳ giai đoạn nào đều khôi phục chính xác mà không cần `from=N`.
5. Trạng thái chính thức giữ nguyên cho tới khi kiểm chứng ngữ nghĩa đầy đủ.
6. Sau khi phát hành gián đoạn, saga commit sẵn có khôi phục được, và không commit lặp chương.
7. Nhập chưa hoàn tất sau khởi động lại không được khởi động Engine thường; chỉ xem, chẩn đoán hoặc khôi phục nhập.
8. `--yes` không bỏ qua Hold hoàn tất; chỉ `--continue` độc lập mới bỏ qua, và không vòng qua cổng review.
9. Năng lực model và provider, lượng dùng, StopReason, ước lượng ngân sách và lỗi đều quan sát được.
10. Đổi model mạnh hơn là cải thiện chất lượng phân tách, phân tích và tổng hợp, đồng thời tự nhiên mở rộng lô an toàn, giảm số lần gọi, không sửa luật văn học trong Go.

## 21. Khả năng mở rộng hướng tương lai

Khả năng mở rộng của phương án này đến từ biên giới ổn định, không phải trừu tượng dựng trước:

- model hiểu tốt hơn: ba loại hàm ngữ nghĩa Boundary/Chapter/Synthesis chính xác lên thẳng;
- cửa sổ ngữ cảnh hoặc đầu ra mở rộng: bộ hai ngân sách tự mở rộng lô analysis an toàn, và giảm số khối cùng số tầng Reduce;
- đầu ra có cấu trúc mạnh hơn: typed-call tự chọn ràng buộc provider mạnh hơn;
- model mức rẻ mạnh hơn: segment mang tính cơ học hơn có thể chuyển sang mức rẻ hơn, lợi ích chi phí theo đó vào, không đổi contract ngữ nghĩa;
- định dạng đầu vào mới: chỉ cần chuyển EPUB v.v. thành cùng văn bản chuẩn hóa và tọa độ SourceUnit;
- ngữ nghĩa toàn sách mới: thêm trường có người tiêu thụ rõ ràng vào `ImportedChapterFacts` hoặc `BookSynthesis`, không đổi giao thức khôi phục và phát hành;
- đồng sáng tác người dùng mạnh hơn: thêm sửa bằng ngôn ngữ tự nhiên ở ranh giới xác nhận, không viết tri thức định dạng vào mã.

Phần không đổi là bao phủ toàn văn, danh tính `InputDigest`, kiểm phạm vi và phát hành idempotent. Đây là ghi sổ mà model có mạnh đến đâu cũng không đáng giao cho model; phần ngữ nghĩa khả biến đều ở lại trong hàm model, nên lợi ích nâng cấp model xuyên được tới kết quả sản phẩm.

## 22. Quyết định cuối cùng

Chọn **đường ống nhập ngữ nghĩa theo giai đoạn**, từ chối hai hướng:

1. tiếp tục mở rộng regex chương và ngưỡng số chương/số cung;
2. dùng một Agent vòng lặp dài tự do tiếp quản toàn bộ việc nhập.

Biên giới cuối cùng là:

> **Model quyết định văn bản nghĩa là gì; mã bảo đảm mỗi chữ đi đâu, mỗi kết quả ứng với đầu vào nào, đầu vào/đầu ra mỗi lệnh gọi đều chứa nổi, sau thất bại tiếp từ đâu, và khi nào có tư cách thành sự thật chính thức.**

Điều này vừa giữ năng lực tự chủ và lợi ích tương lai của model, vừa giữ kiến trúc gọn gàng Engine + hàm ngữ nghĩa có kiểu + tầng sự thật hệ thống file hiện tại của ainovel-cli.