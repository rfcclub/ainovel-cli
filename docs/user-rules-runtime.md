# Thiết kế thống nhất quy tắc người dùng

## Một câu

Mọi quy tắc viết dài hạn đều được chuẩn hóa vào cùng một ảnh chụp quy tắc của cuốn sách này; lúc chạy chỉ tiêm ảnh chụp đó qua `novel_context`, không nhồi lại văn bản quy tắc nguyên gốc vào prompt nữa.

```text
prompt khởi động / file rules của người dùng / yêu cầu dài hạn lúc chạy
        ↓
LLM chuẩn hóa ngữ nghĩa (theo từng nguồn)
        ↓
Go hợp nhất tất định (theo ưu tiên)  ←  quy tắc mặc định hệ thống (nhúng trong mã, vào hợp nhất trực tiếp, không qua LLM)
        ↓
output/novel/meta/user_rules.json
        ↓
novel_context tiêm vào
        ↓
Architect / Writer / Editor / kiểm tra commit dùng chung
```

## Trạng thái triển khai (2026-07-19, đã lên sóng + đã sửa thiếu sót qua review)

Thiết kế này đã được triển khai, 24 package `go build` / `go vet` / `go test` đều xanh. Sau một vòng code review đã sửa 4 thiếu sót (đều đã sửa): ① quy tắc từ prompt khởi động chỉ được nối vào phương thức chết `Host.Start`, còn cửa vào thật đi `StartPrepared` nên bỏ sót việc dựng ảnh chụp — đã truyền thẳng prompt nguyên gốc từ hai cửa vào quick/cocreate, gọi thống nhất `Host.PrepareUserRules`; ② lỗi ghi ảnh chụp xuống đĩa bị nuốt — `PrepareUserRules` đổi thành ghi thất bại thì trả error và hủy mở sách (đường resume giữ best-effort, tránh đưa chế độ thất bại mới vào sách cũ); ③ lỗi đọc file rules bị bỏ im lặng — `raw.go` ghi log cho lỗi không phải "không tồn tại" (quyền v.v.); ④ README vẫn dạy YAML/front matter cũ và trỏ tới file đã xóa — đã viết lại.

Bản lên sóng cơ bản khớp tài liệu này; lựa chọn hiện thực sau khi nâng cấp đầu ra có cấu trúc như sau:

1. **Chuẩn hóa chỉ có một `Contract.Schema`, không duy trì hai bộ prompt.**
   Khi model khai báo hỗ trợ thì gửi JSON Schema gốc; khi không hỗ trợ hoặc năng lực chưa biết, tầng contract thống nhất tiêm cùng Schema đó vào prompt.
   Cả hai chế độ đều kiểm lại Schema ở phía Go, sau đó thực thi kiểm tra miền giá trị và ràng buộc nghiệp vụ xuyên trường.
2. **Khi một giá trị trường không hợp lệ thì hạ cấp thành "trường đó thiếu", không hạ cấp cả nguồn.**
   Nếu một trường là chỗ giữ chỗ rỗng hoặc sai kiểu, sanitize bỏ trường đó (coi như chưa khai báo) và giữ các trường hợp lệ còn lại của nguồn đó;
   chỉ khi "cả lần chuẩn hóa thất bại" (mạng/model/JSON không hợp lệ/phân tích thất bại) mới hạ cấp toàn bộ nguồn thành raw preferences và
   đặt `status=degraded`. Như vậy một trường hỏng không kéo theo các quy tắc hợp lệ khác cùng nguồn. Lỗi đầu ra model sửa được sẽ kèm
   lý do chính xác để tự sửa tiếp, vòng đời do `context` điều khiển; lỗi kết thúc rõ ràng vào log và hạ cấp theo nguồn.

Điểm neo mã: `internal/rules` (thuần dữ liệu + hợp nhất tất định: snapshot.go / raw.go / types.go), `internal/userrules`
(LLM chuẩn hóa + điều phối + ghi xuống đĩa: normalize.go / service.go), `internal/store/user_rules.go` (lưu ảnh chụp),
`internal/userrules/service.go` (ghi quy tắc lúc chạy), `assets/prompts/arbiter-intervention.md` (phân ba loại).
Đường cơ sở cơ học mặc định của hệ thống đã được chuyển từ `assets/rules/default.md` vào bộ nhúng trong mã `rules.SystemDefaultsFor(lang)`, đường phân tích YAML và
phụ thuộc yaml.v3 đã bị xóa. **Chưa kiểm chứng**: toàn tuyến mở sách bằng LLM thật / hành động rules của Arbiter lúc chạy (nguyên mẫu offline của normalizer đã kiểm 10/10).

## Vì sao

Writer mỗi chương không nhận được prompt đầy đủ ban đầu của người dùng một cách ổn định. Nó chủ yếu dựa vào nhiệm vụ của chương này và `novel_context(chapter=N)`.

Nên quy tắc dài hạn không thể dựa vào trí nhớ lịch sử hội thoại, cũng không nên lén đoán từ ngôn ngữ tự nhiên bằng regex. Cách đúng là: chuẩn hóa quy tắc dài hạn thành trạng thái một cách tường minh, rồi để `novel_context` phân phối thống nhất.

"Chuẩn hóa" ở đây bắt buộc phải tận dụng năng lực hiểu ngôn ngữ tự nhiên của mô hình lớn, chứ không phải liệt kê cách diễn đạt trong Go. Chương trình chỉ định nghĩa một số ít trường kiểm tra cơ học được, phụ trách schema, hợp nhất tất định, kiểm chứng, ghi xuống đĩa và kiểm tra khi commit; những cách nói như "mỗi chương khoảng một nghìn rưỡi", "đừng quá hai nghìn một chương", "đừng viết mấy câu kiểu bánh răng vận mệnh nữa" do LLM hiểu ngữ nghĩa.

## Trạng thái thống nhất

Lúc chạy, cuốn sách này chỉ duy trì một nguồn sự thật quy tắc người dùng:

```text
output/novel/meta/user_rules.json
```

Hình dạng giữ đơn giản:

```json
{
  "version": 2,
  "status": "ready",
  "structured": {
    "genre": "tu tiên",
    "forbidden_chars": [],
    "forbidden_phrases": ["một cách nào đó"],
    "fatigue_words": {}
  },
  "preferences": "Nhân vật chính lạnh lùng kìm chế; ít giải thích, dùng hành động và đối thoại nhiều hơn.",
  "sources": [
    "startup_prompt",
    ".ainovel/rules/style.md"
  ],
  "uncertain": [
    "ít dùng so sánh: không có ngưỡng rõ ràng, xử lý như sở thích văn phong"
  ]
}
```

Ranh giới trường:

- `version`: phiên bản schema ảnh chụp, thuận tiện di trú tương lai.
- `status`: `ready` / `degraded`, đánh dấu việc chuẩn hóa có thành công đầy đủ không; chỉ dùng để hiển thị và chẩn đoán, không vào phán đoán sáng tác.
- `structured`: quy tắc mã kiểm tra cơ học được hoặc tiêu thụ ổn định được.
- `preferences`: sở thích ngôn ngữ tự nhiên không kiểm cơ học được nhưng có hiệu lực dài hạn với sáng tác.
- `sources`: audit nguồn, không vào phán đoán sáng tác.
- `uncertain`: chẩn đoán chuẩn hóa, chỉ dùng hiển thị và chẩn đoán, không vào phán đoán sáng tác.

Chỉ `structured` và `preferences` được tiêm cho model; `version` / `status` / `sources` / `uncertain` là metadata vận hành và chẩn đoán, không vào `working_memory.user_rules`. Lỗi kỹ thuật không vào ảnh chụp, chỉ vào log (xem §Thất bại và hạ cấp).

## Nguồn đầu vào

Quy tắc dài hạn có bốn nguồn đầu vào:

1. **Prompt khởi động**: yêu cầu dài hạn người dùng viết khi mở sách.
2. **File rules của người dùng**: sở thích dài hạn toàn cục hoặc cấp dự án, đọc dưới dạng ngôn ngữ tự nhiên thường.
3. **Quy tắc mặc định của hệ thống**: đường cơ sở cơ học nhúng trong mã.
4. **Yêu cầu dài hạn lúc chạy**: người dùng nói giữa chừng "từ sau cứ thế", Arbiter trích ra hành động `rules`, Host gọi `AddRuntimeRule`.

Các nguồn này không vào thẳng prompt Writer, cũng không bị đọc lặp lại lúc chạy. Chúng chỉ tham gia chuẩn hóa khi sinh hoặc cập nhật ảnh chụp, kết quả hợp nhất vào `meta/user_rules.json`.

## File rules

File rules là prompt dài hạn thường, không phải prompt lúc chạy, cũng không phải file cấu hình. Nó chỉ là đầu vào cho chuẩn hóa, không hỗ trợ YAML:

```md
# Sở thích viết

Mỗi chương 1200-1600 chữ.
Nhân vật chính lạnh lùng kìm chế, đừng thánh mẫu.
Ít giải thích, dùng hành động và đối thoại để đẩy tiến.
Đừng xuất hiện "ở một mức độ nào đó".
```

Sau khi hệ thống đọc, chuẩn hóa thành:

```json
{
  "structured": {
    "forbidden_phrases": ["một cách nào đó"]
  },
  "preferences": "Mỗi chương 1200-1600 chữ; nhân vật chính lạnh lùng kìm chế, đừng thánh mẫu; ít giải thích, dùng hành động và đối thoại để đẩy tiến."
}
```

Nếu trong file có YAML front matter, cũng xử lý như văn bản thường, không coi là khai báo có cấu trúc. Kết quả có cấu trúc chỉ đến từ luồng chuẩn hóa thống nhất.

Sau khi khởi động, nếu người dùng sửa file rules, cuốn sách hiện tại sẽ không tự đổi; cần sinh lại ảnh chụp. Như vậy sách cũ không trôi dạt hành vi vì file rules toàn cục thay đổi.

## Chuẩn hóa ngữ nghĩa

Chuẩn hóa là một lệnh gọi LLM độc lập, ràng buộc bởi schema — mỗi nguồn chuẩn hóa riêng một lần, không trộn vào sinh sáng tác, cũng không phân tích cứng bằng regex hay bảng từ khóa.

Đầu vào:

- Nguyên văn của một nguồn (prompt khởi động / một file rules / một yêu cầu lúc chạy)
- Giải thích các trường `structured` hệ thống hiện hỗ trợ

Quy tắc mặc định hệ thống không thuộc nhóm này — chúng là quy tắc có cấu trúc đã biên dịch nhúng trong mã, vào thẳng §Quy tắc hợp nhất, không qua normalizer.

Đầu ra:

- `structured` ứng viên của nguồn đó
- `preferences` ứng viên của nguồn đó
- `sources`
- `uncertain`

Trách nhiệm phía Go:

- Cung cấp schema.
- Kiểm tra kiểu và miền giá trị của trường.
- Theo ưu tiên ở §Quy tắc hợp nhất, hợp nhất tất định các nguồn (LLM không phán định ưu tiên nguồn).
- Lưu ảnh chụp.
- Tiêm ảnh chụp trong `novel_context`.
- Dùng cùng ảnh chụp đó để kiểm tra cơ học trong `commit_chapter`.

Trách nhiệm phía LLM:

- Hiểu quy tắc ngôn ngữ tự nhiên của một nguồn.
- Nâng các quy tắc rõ ràng, kiểm cơ học được vào `structured`.
- Giữ sở thích thẩm mỹ, văn phong, nhân vật lại ở `preferences`.
- Với nội dung chưa chắc chắn thì giữ thận trọng, không tự bịa ngưỡng.

### Nâng thận trọng

`structured` là quy tắc cứng hoặc tham số ổn định, không phải "vùng model đoán". Nâng quy tắc phải thận trọng:

- Chỉ khi người dùng diễn đạt rõ ràng, không mơ hồ mới ghi vào `structured`.
- `forbidden_chars` / `forbidden_phrases` là trường mức error, càng phải thận trọng; chỉ lệnh cấm rõ ràng kiểu "đừng xuất hiện X", "cấm dùng X", "chớ viết X" mới nâng.
- `fatigue_words` chỉ nâng khi người dùng đưa ra từ cụ thể và ngưỡng; các yêu cầu không ngưỡng như "ít dùng so sánh", "đừng quá sách vở", "bớt cửa miệng" vào `preferences`.
- Mọi mong muốn về số chữ/độ dài ("mỗi chương 3000 chữ", "ngắn một chút") đều vào `preferences`: ngắn dài chương là phán đoán ngữ nghĩa của nhịp tự sự, không kiểm cơ học — biến thành đường cứng sẽ dụ model châm nước để vượt đường.
- Yêu cầu không cơ học hóa được, không có ngưỡng rõ ràng, phụ thuộc ngữ cảnh đều vào `preferences`.

Nguyên tắc:

```text
Thà bỏ sót khỏi structured, hạ cấp thành sở thích mềm;
không được nâng sai vào structured, tạo báo lỗi cứng mỗi chương.
```

Cái giá bỏ sót là sở thích văn phong yếu đi chút; cái giá nâng sai là sinh sự thật quy tắc sai mỗi chương.

## Thất bại và hạ cấp

Chuẩn hóa là đường tăng cường, không phải tiền điều kiện của sáng tác chính. Model hiểu thất bại tuyệt đối không được chặn việc viết sách.

- **Hạ cấp theo nguồn**: một nguồn chuẩn hóa thất bại (mạng / model / JSON không hợp lệ / kiểm schema thất bại) thì nguồn đó hạ thành raw preferences, không sinh `structured`; các nguồn thành công khác vẫn đóng góp `structured` như thường.
- **Tự sửa theo context**: lỗi yêu cầu có thể thử lại, lỗi định dạng/Schema ở chế độ prompt và lỗi kiểm nghiệp vụ tự sửa liên tục cho tới khi thành công hoặc `context` kết thúc; không đặt số lần cố định. Vi phạm contract gốc, từ chối trả lời, cắt cụt, kết thúc lỗi và lỗi yêu cầu không thử lại được đều lộ ra ngay và hạ cấp theo nguồn.
- **Lỗi kỹ thuật vào log**: lỗi kỹ thuật JSON / schema / mạng ghi vào log, không vào `working_memory.user_rules`, không là đầu vào sáng tác.
- **Đánh dấu ảnh chụp**: khi bất kỳ nguồn nào hạ cấp thì ảnh chụp `status=degraded`.
- **Ghi được xuống đĩa thì tiếp tục**: chỉ cần `meta/user_rules.json` ghi được, sáng tác chính phải tiếp tục.
- **Chỉ thất bại ghi đĩa mới hủy**: chỉ hủy khi ảnh chụp không ghi được xuống đĩa, vì các lần chạy sau không có nguồn sự thật ổn định.

Hợp đồng `AddRuntimeRule` (lúc chạy): khi normalizer thất bại thì lưu ảnh chụp degraded,
không tiêm lỗi chuẩn hóa JSON/schema/mạng vào luồng sáng tác; chỉ thất bại ghi đĩa mới trả error.

## Quy tắc mặc định của hệ thống

`System defaults` là đường cơ sở cơ học nhúng trong mã, không phải file rules của người dùng, cũng không dùng YAML.

Nó không qua chuẩn hóa LLM — đã ở dạng có cấu trúc, vào thẳng hợp nhất Go ở §Quy tắc hợp nhất với tư cách nguồn ưu tiên thấp nhất. Nhờ đó quy tắc mặc định không có vấn đề thất bại, trôi dạt hay chi phí của LLM.

Quy tắc cơ học mặc định trước đây tạm trú ở `assets/rules/default.md` (chi tiết hiện thực cũ, chỉ để tương thích YAML cho người dùng cố chấp); khi lên sóng thiết kế này đã chuyển vào `rules.SystemDefaultsFor(lang)` nhúng trong mã, đường phân tích YAML đã bị xóa (xem §Trạng thái triển khai).

Khi di trú, giữ lại chú thích cần thiết giải thích nguồn gốc ngưỡng, ví dụ một số ngưỡng từ nhàm đến từ thực nghiệm sản phẩm chạy dài. Không phải để tương thích YAML cũ, mà để người bảo trì tương lai biết ngưỡng mặc định vì sao tồn tại, khi nào nên điều chỉnh.

Lưu ý: đường cơ sở tách theo ngôn ngữ — truyện tiếng Việt dùng bảng `systemDefaultsVI`, truyện tiếng Trung dùng `systemDefaultsZH`.

## Quy tắc hợp nhất

Thứ tự hợp nhất theo "càng cụ thể càng ưu tiên":

```text
System defaults
→ Kết quả biên dịch Global rules
→ Kết quả biên dịch Project rules
→ Kết quả biên dịch Startup prompt
→ Runtime user update
```

Nguồn ưu tiên cao ghi đè nguồn thấp.

Hợp nhất do Go thực thi tất định: LLM chỉ chuẩn hóa ngôn ngữ tự nhiên của một nguồn thành `structured`/`preferences` ứng viên, Go theo thứ tự trên làm ghi đè trường và nối văn bản, ưu tiên không giao cho LLM phán định.

- `structured`: ghi đè theo trường, trường cùng tên của nguồn sau ghi đè nguồn trước.
- `preferences`: không ghi đè nhau, nối theo thứ tự ưu tiên thành văn bản dễ đọc (nguồn ưu tiên cao ở sau), để LLM thấy được thứ tự nguồn.

Hạn chế đã biết: `preferences` được sắp theo ưu tiên, nhưng Go không giải quyết xung đột. Trong chạy dài, nếu người dùng lần lượt đưa ra các sở thích mềm mâu thuẫn (ví dụ trước "lạnh lùng kìm chế" sau "nói nhiều"), cả hai sẽ nằm trong văn bản, để LLM cân nhắc theo thứ tự và ngữ cảnh; cái nào cần ghi đè cứng tất định thì nên diễn đạt thành trường `structured` cơ học hóa được.

## Cửa vào ghi xuống đĩa

Chuẩn hóa, hợp nhất, ghi xuống đĩa là cùng một bộ logic, nhưng có hai bên gọi, phải phân biệt rõ, nếu không sẽ trộn việc chuẩn bị khởi động vào ngữ cảnh sáng tác chính:

- **Mở sách / làm mới (phía khởi động, tất định)**: Host / luồng khởi động gọi thẳng bộ logic này để sinh ảnh chụp ban đầu, không vào vòng lặp sáng tác chính. Đây là nhiệm vụ chuẩn bị khởi động tất định.
- **Cập nhật lúc chạy (hành động phán định can thiệp)**: hành động `rules` Arbiter phân loại ra được Host gọi thẳng `userrules.Service.AddRuntimeRule`, dùng lại cùng bộ logic kiểm / hợp nhất / ghi đĩa, đưa quy tắc mới không có điểm tiến độ vào ảnh chụp với tư cách `Runtime user update`.

(Trong hiện thực, nên thu bộ logic này thành một dịch vụ nội bộ để hai bên gọi dùng chung; tên cụ thể để hiện thực quyết.)

Dù bên gọi nào, cuối cùng đều ghi vào cùng một `meta/user_rules.json`. Logic ghi đĩa chỉ làm ba việc:

1. Kiểm tra trường có cấu trúc.
2. Theo ưu tiên ở §Quy tắc hợp nhất, hợp nhất vào ảnh chụp hiện tại của cuốn sách.
3. Trả về sự thật quy tắc đầy đủ sau khi lưu.

Không làm:

- Không giao subagent.
- Không sửa đại cương.
- Không nuốt im lặng trường không hợp lệ (ghi lại và hạ cấp, xem §Thất bại và hạ cấp).
- Không tiêm văn bản nguyên gốc vào như prompt cuối cùng.

Ví dụ cập nhật lúc chạy: người dùng nói "từ sau cứ thế" (không có điểm tiến độ) → Arbiter phán định thành hành động `rules` → Host qua `AddRuntimeRule` chuẩn hóa mục đó → hợp nhất vào ảnh chụp với tư cách `Runtime user update` ở ưu tiên cao nhất → luồng sự kiện hiển thị lại.

## Hiển thị lại

Mỗi lần sinh hoặc cập nhật ảnh chụp `user_rules` đều phải hiển thị kết quả chuẩn hóa cho người dùng:

```text
Đã sinh ảnh chụp quy tắc của sách:
- Quy tắc cơ học: mỗi chương 1200-1600 chữ; cấm cụm từ "ở một mức độ nào đó"
- Sở thích văn phong: nhân vật chính lạnh lùng kìm chế; ít giải thích, dùng hành động và đối thoại để đẩy tiến
- Chưa nâng thành quy tắc cơ học: ít dùng so sánh (không có ngưỡng rõ ràng, xử lý như sở thích văn phong)
```

- Khởi động / làm mới: dùng lại năng lực log quy tắc khởi động sẵn có để in ảnh chụp, không thêm cơ chế mới; ở kịch bản đồng sáng tác có thể gộp hiển thị vào bước xác nhận.
- Lúc chạy: `AddRuntimeRule` thành công thì hiển thị qua luồng sự kiện ("quy tắc viết đã cập nhật và lưu trữ").
- Hạ cấp: khi `status=degraded`, phần hiển thị nói rõ nguồn nào chưa phân tích được, hiện đang chạy theo raw preferences, có thể sinh lại ảnh chụp.

Hiển thị lại không phải cổng phê duyệt lần hai; tác dụng của nó là để người dùng biết hệ thống hiểu thành gì, phát hiện sai thì sinh lại ảnh chụp.

## Cách agent tiêu thụ

Mọi agent chỉ xem:

```json
working_memory.user_rules
```

Phân công trách nhiệm:

- Architect: theo mong muốn số chữ trong `preferences` để điều chỉnh mật độ tình tiết và số chương chia mỗi chương.
- Writer: viết theo quy tắc cứng trong `structured`, điều chỉnh văn phong theo `preferences`.
- Editor: thẩm duyệt theo cùng bộ quy tắc.
- `commit_chapter`: dùng `structured` để kiểm tra cơ học và trả violations.

Writer không hiểu lại prompt khởi động nguyên gốc, cũng không đọc file rules nguyên gốc.

## Phân loại can thiệp: ba hướng đi

Can thiệp lúc chạy chia ba loại theo "muốn đổi cái gì":

- **Viết thế nào** (bút pháp / văn phong / chất lượng: số chữ, dùng từ, từ cấm, mẫu câu, tỷ lệ đối thoại, định dạng tiêu đề...) → hành động `rules` của Arbiter, chuẩn hóa hợp nhất vào `meta/user_rules.json`. Ví dụ: "mỗi chương 1500 chữ", "tiêu đề chỉ dùng tiếng Việt", "nhân vật chính nói chung lạnh lùng kìm chế", "tăng tỷ lệ đối thoại".
- **Viết cái gì** (tình tiết / cấu trúc / hướng nhân vật / dung lượng) → architect, rơi vào compass / outline / hồ sơ nhân vật. Ví dụ: "tập này viết nhiều tuyến chiến đấu", "từ chương 30 giọng nhân vật chính chuyển lạnh", "tăng lên 40 chương".
- **Sửa cái đã viết** (viết lại / tu chỉnh chương chỉ định) → editor, vào hàng đợi PendingRewrites.

Tiêu chí: **"viết thế nào" → rules; "viết cái gì" → architect; "sửa cái đã viết" → editor**.

## Các bước triển khai

1. Thêm store `meta/user_rules.json`.
2. Thêm một lượt chuẩn hóa LLM độc lập (theo từng nguồn), dùng schema ràng buộc đầu ra `structured/preferences/sources/uncertain` ứng viên.
3. Thêm hợp nhất tất định phía Go: theo ưu tiên làm ghi đè trường và nối văn bản cho từng nguồn, sinh ảnh chụp.
4. Thu chuẩn hóa / hợp nhất / ghi đĩa thành một bộ logic để hai bên gọi dùng chung: phía khởi động gọi thẳng để sinh ảnh chụp ban đầu; lúc chạy thì hành động `rules` do can thiệp phán định dùng lại qua `AddRuntimeRule`. Khi thất bại xử lý theo §Thất bại và hạ cấp: nguồn hạ thành raw preferences, ảnh chụp `status=degraded`, sáng tác chính tiếp tục.
5. Chuyển quy tắc cơ học mặc định hiện có trong `assets/rules/default.md` vào cấu trúc nhúng trong mã hoặc asset JSON, giữ chú thích nguồn gốc ngưỡng; xóa đường phân tích YAML cho rules người dùng, không làm lớp tương thích.
6. Sau khi đọc file rules không tiêm thẳng chính văn như prompt nữa, mà chuẩn hóa rồi hợp nhất vào ảnh chụp `user_rules`.
7. `novel_context` chỉ tiêm `working_memory.user_rules` từ `meta/user_rules.json`.
8. `commit_chapter` dùng cùng `user_rules.structured` để kiểm.
10. Phân loại can thiệp (nay do Arbiter đảm nhiệm, arbiter-intervention.md) phân ba hướng rõ ràng theo "muốn đổi cái gì": yêu cầu dài hạn về văn phong / chất lượng viết đi hành động `rules` vào ảnh chụp; tình tiết / cấu trúc / nhân vật / dung lượng đi architect; làm lại chương đã viết đi editor (xem §Phân loại can thiệp: ba hướng đi).

## Tiêu chí nghiệm thu

- Người dùng viết "mỗi chương 1200-1600 chữ" ở prompt khởi động, `novel_context` của chương 1 mà Writer thấy phải có nguyên văn mong muốn đó trong `preferences`.
- File rules chỉ viết ngôn ngữ tự nhiên cũng chuẩn hóa được vào cùng `user_rules` khi sinh ảnh chụp.
- File rules không cần cũng không hỗ trợ YAML; tất cả chuẩn hóa theo quy tắc ngôn ngữ tự nhiên.
- Lúc chạy không đọc lại file rules nữa; chỉ đọc `meta/user_rules.json`.
- Quy tắc cơ học mặc định không còn đến từ file rules YAML, rules người dùng cũng không có lớp tương thích YAML.
- Chuẩn hóa không dùng regex/từ khóa cứng; việc hiểu ngôn ngữ tự nhiên do LLM làm.
- Quy tắc mơ hồ không bị nâng thành trường `structured` mức error.
- Quy tắc mặc định hệ thống không qua LLM, vào thẳng hợp nhất Go.
- Ưu tiên nguồn và ghi đè trường do Go thực thi tất định, cùng đầu vào sinh cùng ảnh chụp.
- Người dùng nói "từ sau cứ thế" lúc chạy, qua hành động rules của Arbiter hợp nhất vào ảnh chụp, `novel_context` các chương sau thấy được cập nhật.
- Chuẩn hóa thất bại không chặn viết sách: nguồn thất bại hạ thành raw preferences, ảnh chụp `status=degraded`, sáng tác chính tiếp tục; chỉ khi ảnh chụp không ghi được xuống đĩa mới hủy.
- Chuẩn hóa thất bại trả `status=degraded`, không ném lỗi kỹ thuật lên làm nhiễm luồng chính.
- Sau khi sinh hoặc cập nhật ảnh chụp sẽ hiển thị `structured` / `preferences` / các mục chưa nâng; khi hạ cấp thì hiển thị nguồn bị hạ cấp.
- Mở sách mới không kế thừa `user_rules` của sách trước.
- Trường có cấu trúc không hợp lệ không bị bỏ qua im lặng: ghi lại và hạ cấp nguồn đó, không chặn luồng chính.

## Dứt khoát không làm (phán định là không cần, không phải cắt giai đoạn)

Các năng lực dưới đây không có lợi ích trong nhu cầu hiện tại, không vào thiết kế, tránh thiết kế quá mức:

- Ngữ nghĩa xóa / hoàn tác ở mức trường như `clear_fields`.
- Tự làm mới khi phát hiện file rules thay đổi (sửa file rồi sinh lại ảnh chụp tường minh là đủ).
- Mốc thời gian cho `preferences` / giải quyết ghi đè (cần ghi đè cứng thì dùng `structured`).
- Lưu mảng `diagnostics` trong ảnh chụp (lỗi kỹ thuật vào log là đủ, ảnh chụp chỉ giữ `status`).
- Sinh giải thích trường schema tự động từ kiểu Go (giữ tay một bản giải thích ngắn là đủ).

Nguyên tắc thiết kế không đổi: LLM chịu trách nhiệm hiểu ngôn ngữ tự nhiên, Go chịu trách nhiệm hợp nhất tất định, kiểm chứng, ghi xuống đĩa và kiểm tra.