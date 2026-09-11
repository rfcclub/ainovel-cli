# Sổ tay quan sát

Khi viết một cuốn tiểu thuyết dài, làm sao biết các cơ chế có thật sự đang hoạt động?

Tài liệu này không chép lại các luật của diag, mà hướng tới **vận hành thực tế**: bạn đã viết tới chương N, nên mở file nào, xem trường nào, và phán đoán khỏe mạnh hay bất thường.

---

## 1. Quy trình chẩn đoán chung

```
1. /diag                       # chẩn đoán tự động, xem mục Findings
2. cd output/{novel}/meta/     # cat trực tiếp các sản phẩm then chốt
3. tail decisions.jsonl        # xem các phán định Arbiter gần đây
4. ls -lt sessions/agents/     # xác định phiên Worker gần nhất rồi mới tail
```

Những sự thật `/diag` không bao phủ (kể cả các mục "chẩn đoán cần bổ sung" liệt kê trong tài liệu này) phải tra tay theo bước 2-4.

### Báo issue: xuất chẩn đoán đã khử nhạy cảm

Mỗi lần `/diag` còn ghi thêm `output/{novel}/meta/diag-export.md` — một bản chẩn đoán **đã khử nhạy cảm** (chính văn tiểu thuyết / prompt / suy nghĩ đã bị loại bỏ, chỉ giữ bộ khung hành vi: tên công cụ, chuỗi lỗi, số lần lặp, phase/flow, bước bị kẹt, phân loại lỗi log). Gặp vấn đề dạng vòng lặp vô hạn hay gián đoạn, chỉ cần dán file này vào issue GitHub; người bảo trì dựa vào đó để định vị, không cần dữ liệu `output/` của người dùng.

---

## 2. Bảng tra nhanh các sản phẩm then chốt

Xếp theo "đường chẩn đoán phổ biến nhất khi có sự cố":

| Sản phẩm | Đường dẫn | Xem gì | Khỏe mạnh | Bất thường |
|---|---|---|---|---|
| Tiến độ | `meta/progress.json` | `phase` / `flow` / `completed_chapters` | phase tiến đơn điệu, flow nằm trong tập hợp hợp lệ | phase lùi / flow kẹt ở một trạng thái |
| La bàn | `meta/compass.json` | khoảng cách giữa `last_updated` và chương mới nhất | gap < 15 chương | gap > 15 chương (CompassDrift kích hoạt) |
| Danh bạ nhân vật phụ | `meta/cast_ledger.json` | số mục / tỷ lệ điền brief_role / tính nhất quán tên | xem §4 | xem §4 |
| Sổ phục bút | `meta/foreshadow.json` | số chương đình trệ dài nhất của `status="planted"` | < số chương/3 | > số chương/3 (StaleForeshadow kích hoạt) |
| Đại cương | `meta/layered_outline.json` | số chương chưa viết còn lại của tập hiện tại | đã mở rộng trước 1-2 chương | viết tới chương hiện tại nhưng chương sau không có outline (OutlineExhausted) |
| Hồ sơ nhân vật | `meta/characters.json` | có tìm thấy nhân vật core/important trong tóm tắt N chương gần đây không | tìm thấy hết | vắng mặt (GhostCharacter kích hoạt) |
| Checkpoint | `meta/checkpoints.jsonl` | `step` của dòng cuối có khớp progress không | khớp | không khớp (khôi phục sau sập chưa tự lành) |
| Audit phán định | `meta/decisions.jsonl` | facts/decision của vài phán định gần nhất | phân loại chính xác, hành động hợp lý | cùng loại can thiệp phán định thất bại lặp lại |

---

## 3. Quan sát la bàn (compass)

**Thời điểm sửa**: 2026-05-08 (commit `fix: update_compass 工具自动填 last_updated` — công cụ tự điền last_updated)

### Xem gì

```bash
cat output/{novel}/meta/compass.json
```

Ngữ nghĩa trường:
- `ending_direction`: hướng kết cục (phải khớp mục "Hướng kết cục" trong `premise.md`)
- `open_threads`: tuyến dài đang mở (architect thêm/xóa ở mỗi ranh giới tập)
- `estimated_scale`: quy mô dự kiến (ví dụ "4-6 tập", cập nhật ở mỗi ranh giới tập)
- `last_updated`: **do công cụ tự điền** bằng số chương hoàn thành lớn nhất tại thời điểm cập nhật (không còn phụ thuộc LLM tự điền)

### Phán đoán sức khỏe

| Tín hiệu | Phán đoán |
|---|---|
| `last_updated` nằm trong khoảng `[latest-15, latest]` | Khỏe mạnh |
| `last_updated` chậm hơn latest quá 15 chương | architect không cập nhật ở ranh giới cung/tập — kiểm tra prompt architect-long.md |
| `last_updated == 0` | **dữ liệu bẩn từ trước bản sửa này**, lần gọi update_compass sau sẽ tự lành |
| `ending_direction` không khớp mục "Hướng kết cục" trong premise.md | architect lén đổi ý định người dùng — ghi lại, quyết định có đóng băng trường này không (vấn đề thiết kế, xem todo.md) |

### Cách kiểm chứng bản sửa có hiệu lực

So sánh trước/sau khi chạy truyện dài:
- **Trước khi sửa**: sau 30+ chương, `compass.last_updated` phần lớn là `0` hoặc một số chương rất sớm
- **Sau khi sửa**: mỗi lần architect gọi `update_compass`, `last_updated` đều bị tầng công cụ ghi đè bằng latest hiện tại

---

## 4. Quan sát danh bạ nhân vật phụ (cast_ledger)

**Tính năng lên sóng**: 2026-05-08 (commit `feat: 新增配角名册自动追踪次要角色` — thêm danh bạ tự theo dõi nhân vật phụ)

### Xem gì

```bash
cat output/{novel}/meta/cast_ledger.json | jq 'length'                     # tổng số mục
cat output/{novel}/meta/cast_ledger.json | jq '[.[] | select(.brief_role == "" or .brief_role == null)] | length'  # số mục thiếu brief_role
cat output/{novel}/meta/cast_ledger.json | jq '[.[] | select(.appearance_count >= 3)] | length'   # số mục xuất hiện thường xuyên (>=3 lần)
cat output/{novel}/meta/cast_ledger.json | jq 'sort_by(-.appearance_count) | .[:10]'  # 10 mục xuất hiện nhiều nhất
```

### Phán đoán sức khỏe

| Chiều | Khỏe mạnh | Bất thường | Xử lý |
|---|---|---|---|
| **Số mục so với số chương đã hoàn thành** | số mục ledger ≈ số chương đã hoàn thành × 0.3-0.6 | > số chương × 0.8 (nhân vật thoáng qua bị vào sổ sai) | kiểm tra mục `cast_intros` trong writer.md đã đủ rõ chưa |
| **Tỷ lệ điền brief_role** | thiếu < 30% | thiếu > 50% | Writer bỏ điền nhiều — hướng dẫn trong prompt chưa đủ |
| **Độ giống nhau của tên** | không có dấu hiệu một người nhiều tên | đồng thời xuất hiện "Lý X" / "Lão Lý" / "X chưởng quầy" | Tên trôi dạt do LLM — thêm ràng buộc "dùng tên nhất quán" vào prompt, hoặc thêm công cụ gộp theo steer của người dùng |
| **Nhân vật xuất hiện thường xuyên** | số mục có `appearance_count >= 5` ít | nhiều mục xuất hiện dày xuyên cung | nên cân nhắc nâng lên hồ sơ cốt lõi (kênh nâng cấp giai đoạn 3) |
| **Việc gợi nhớ có được tiêu thụ** | khi Writer viết tới nhân vật cũ, trường characters của commit_chapter có chứa tên đã có trong ledger | Writer phát minh lại cùng một tên (xuất hiện "Lão Chu A" và "Lão Chu B") | gợi nhớ recent_cast không được tiêu thụ — kiểm tra mục "tính liên tục nhân vật phụ" trong writer.md |

### Kiểm chứng luồng dữ liệu (đầu-cuối)

Sau khi chạy 5 chương:
1. `cat meta/cast_ledger.json` phải khác rỗng (trừ khi chương nào cũng chỉ dùng nhân vật cốt lõi)
2. Nếu Writer giới thiệu "Lão Chu" ở chương 1:
   - trong `cast_ledger` phải có mục `Lão Chu` với `appearance_count=1`
3. Nếu chương 5 viết lại Lão Chu:
   - `Lão Chu.appearance_count=2`, `last_seen_chapter=5`
4. Trong `meta/sessions/agents/writer-*.jsonl`, giá trị trả về của novel_context ở chương 5 phải thấy Lão Chu trong `episodic_memory.recent_cast`
5. Nếu bước trên thấy mà Writer không tiêu thụ (Lão Chu viết ra không khớp chương 1) — đây là vấn đề prompt

### Hiện chưa có chẩn đoán tự động (nhưng snapshot đã nạp)

`diag.Snapshot.CastLedger` đã được đọc trong `Load()` và có thể được luật tiêu thụ trực tiếp — nhưng hiện chưa viết luật nào. Việc kiểm chứng vẫn phải tra tay bằng các lệnh `jq` ở trên.

Nếu sau này muốn bổ sung luật chẩn đoán (ứng viên):
- `CastBriefRoleMissing`: cảnh báo khi tỷ lệ thiếu > 50%
- `CastBloat`: cảnh báo khi số mục > số chương × 0.8
- `CastPromotionCandidate`: appearance_count ≥ 5 và xuyên cung → đề xuất nâng cấp

Ngưỡng thì đừng chốt vội — cứ để dữ liệu truyện dài ra rồi xem phân bố thật mới định. Bản thân mã luật chỉ cần 30-50 dòng.

---

## 5. Writer có đang làm việc như kỳ vọng không

Điều quan tâm nhất khi chạy truyện dài là **Writer có thật sự hành xử theo prompt không**. Cách quan sát trực tiếp nhất là session log:

```bash
ls output/{novel}/meta/sessions/agents/    # mỗi subagent một file jsonl
tail -50 output/{novel}/meta/sessions/agents/writer-*.jsonl
```

Xem vài hành vi cụ thể:

| Hành vi kỳ vọng | Thể hiện trong jsonl |
|---|---|
| Writer có xem recent_cast | trường `episodic_memory.recent_cast` trong giá trị trả về của công cụ novel_context khác rỗng |
| Writer có điền cast_intros trong commit_chapter | mảng `cast_intros` trong tham số tool_call khác rỗng (chỉ ở chương giới thiệu nhân vật mới) |
| Writer có dùng gợi ý chương liên quan | số lần gọi `read_chapter` > 1 (mặc định 1 lần, nhiều hơn nghĩa là có đọc lại) |
| Writer không vi phạm thứ tự công cụ | chuỗi tool_call đúng nghiêm ngặt `novel_context → read_chapter → plan_chapter → draft_chapter → check_consistency → commit_chapter` |

Nếu trong jsonl thấy Writer gọi novel_context rỗng nhiều lần, hoặc gọi công cụ khác sau commit_chapter — là prompt chưa giữ được.

---

## 6. Đường đỏ của chạy dài

Khi chạy truyện dài 100+ chương, hễ mục nào dưới đây kích hoạt thì nên dừng lại chẩn đoán:

- [ ] CompassDrift kích hoạt và tồn tại qua 2 cung mà chưa hết
- [ ] Số mục cast_ledger > số chương đã hoàn thành × 0.8
- [ ] Tỷ lệ điền brief_role trong cast_ledger < 30%
- [ ] Cùng một nhân vật xuất hiện dấu hiệu nhiều tên ("Lão Lý" / "Lý chưởng quầy" cùng tồn tại)
- [ ] Writer viết chương mới mà không đọc nhân vật cũ đã có trong recent_cast (phát minh lại)
- [ ] Trong Worker session xuất hiện ≥ 5 lần gọi novel_context rỗng liên tiếp
- [ ] Sau khi một chương bất kỳ commit, `meta/checkpoints.jsonl` không có step `commit_chapter` tương ứng

Bốn mục đầu là sức khỏe của cơ chế mới trong đợt này; ba mục sau là tính ổn định của cơ chế sẵn có.

---

## 7. Quy ước bảo trì tài liệu

**Khi thêm một sản phẩm ở tầng sự thật (tạo mới một `meta/*.json` / `meta/*.jsonl`), đồng thời:**

1. Thêm một dòng tra nhanh vào §2 của tài liệu này
2. Nếu sản phẩm cần quan sát chuyên biệt (không phải phán đoán đơn giản "có/không"), thêm một mục chuyên đề §X
3. Nếu muốn chẩn đoán tự động, nạp nó trong `internal/diag/snapshot.go::Load` và thêm luật trong `internal/diag/rules_*.go`

**Không nên:**
- Đừng chép toàn bộ luật trong `internal/diag/` vào tài liệu này (đó là tham chiếu luật, không phải sổ tay quan sát)
- Đừng viết luật chẩn đoán cho mọi cơ chế — ngưỡng đoán mò sẽ sai, hãy quan sát trước rồi bổ sung sau