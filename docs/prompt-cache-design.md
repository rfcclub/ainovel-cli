# Thiết kế cache prompt: ba tầng litellm / agentcore / ainovel phối hợp

> Đây là tài liệu giảng giải: giới thiệu cách chúng tôi thiết kế cache prompt LLM đầu-cuối
> (prompt caching) trên ba repo cộng tác, gồm nguyên lý thiết kế, ca chẩn đoán thật và
> vị trí mã nguồn để đối chiếu.
>
> - **litellm** —— gateway LLM: dịch giao thức và khai báo năng lực
> - **agentcore** —— khung Agent: đặt cache và định danh cache
> - **ainovel-cli** —— tầng ứng dụng: tích hợp bằng một dòng cấu hình (codebot tương tự)

---

## 1. Vì sao đáng làm: mô hình chi phí và một ca thật

Yêu cầu của hệ thống Agent có một đặc điểm cấu trúc: **mỗi lượt yêu cầu đều mang theo toàn bộ lịch sử**. Với một vòng lặp công cụ 30 lượt, phần thân yêu cầu ở lượt 30 chứa toàn bộ tin nhắn của 29 lượt trước. Không cache thì cùng một đoạn byte tiền tố bị tính phí lặp lại.

Bảng giá cache của hai nhà cung cấp lớn (lấy Anthropic làm ví dụ):

| Mục | So với giá input thường |
|---|---|
| Ghi cache (TTL 5 phút) | 1.25x |
| Ghi cache (TTL 1 giờ) | 2x |
| **Đọc cache** | **0.1x (tiết kiệm 90%)** |

Ca thật: một lần sinh tiểu thuyết dài 33 chương tốn $58, phân tích `meta/usage.json` sau đó phát hiện
**tỷ lệ trúng cache tổng thể chỉ 8.5%** (coordinator chỉ 2.7%, architect là 0). Sau khi đối chiếu từng
yêu cầu chuỗi usage (input vs cache_read), xác định được ba nguyên nhân gốc:

1. **Byte của tools dao động**: Description/Schema của công cụ subagent được dựng lại mỗi lượt bằng cách lặp trực tiếp trên Go map,
   thứ tự ngẫu nhiên → thân yêu cầu khác lượt trước ngay từ byte 0 → toàn bộ cache tiền tố mất hiệu lực;
2. **Không có ái lực định tuyến**: họ OpenAI không truyền `prompt_cache_key`, yêu cầu có byte hoàn toàn giống nhau vẫn có thể bị
   cân bằng tải về một instance không có cache (bằng chứng đanh thép: trong 33 phiên, yêu cầu đầu có byte giống nhau chỉ trúng 12);
3. **Họ Claude không có điểm ngắt**: Anthropic là cache tường minh, không đặt điểm ngắt `cache_control` tức là hoàn toàn không có cache.

Ba nguyên nhân gốc này tương ứng với ba khối thiết kế ở dưới: **kỷ luật ổn định tiền tố**, **định danh cache**, **dàn dựng điểm ngắt**.

---

## 2. Kiến thức nền: mô hình tinh thần của hai giao thức cache

### 2.1 OpenAI: cache tiền tố tự động (ngầm định)

- Máy chủ tự động cache tiền tố **≥1024 tokens**, không cần client khai báo;
- Lượng trúng tăng theo độ chi tiết căn chỉnh 128-token;
- Yêu cầu có thể mang `prompt_cache_key` (trường chính thức) để **gắn ái lực định tuyến** — các yêu cầu cùng key
  cố gắng rơi vào cùng một phân mảnh cache;
- Trong usage, `cached_tokens` báo lượng trúng; **lượng ghi cache không bao giờ được báo cáo** (`cache_write` luôn 0
  là hiện tượng bình thường, không phải bug).

### 2.2 Anthropic: điểm ngắt tường minh (cache_control)

- Client đặt điểm ngắt `cache_control` trên khối nội dung, **điểm ngắt bao trùm mọi thứ trước nó**
  (thứ tự cố định là tools → system → messages);
- **Tối đa 4 điểm ngắt** mỗi yêu cầu;
- Giá ghi 1.25x (5 phút) / 2x (1 giờ), giá đọc 0.1x;
- `cache_control` **không được đặt trên khối thinking** (sẽ bị 400 từ chối).

### 2.3 Tiền đề chung

Dù ngầm định hay tường minh, cache chỉ nhận diện **sự bằng nhau ở mức byte của tiền tố**. Nên nền móng của mọi thiết kế là cùng một câu:

> **Sắp xếp toàn bộ yêu cầu theo tần suất thay đổi từ thấp đến cao: cái tĩnh đặt trước nhất, cái động đặt cuối cùng,
> và lịch sử đã gửi thì không được đổi dù chỉ một byte.**

---

## 3. Kiến trúc tổng thể: phân công ba tầng

```
┌────────────────────────────────────────────────────────┐
│ Tầng ứng dụng (ainovel-cli / codebot)                    │
│   Quyết định "định danh cache" lấy giá trị gì: một sách một gốc, một vai một tên  │
│   Chi phí tích hợp = hai dòng cấu hình mỗi agent         │
├────────────────────────────────────────────────────────┤
│ agentcore (khung Agent)                                  │
│   Quyết định "đặt điểm ngắt ở đâu, key dẫn xuất khi nào":  │
│   sàn system + đầu mút cuộn của tin nhắn cuối; spawn nối thêm #seq;  │
│   cổng theo năng lực provider, không hỗ trợ thì bỏ im lặng  │
├────────────────────────────────────────────────────────┤
│ litellm (gateway LLM)                                    │
│   Thuần dịch giao thức: cache_control ↔ trường của từng hãng,  │
│   prompt_cache_key truyền thẳng, Capabilities khai báo năng lực  │
│   Không làm bất kỳ quyết định "có cache hay không"        │
└────────────────────────────────────────────────────────┘
```

Nguyên tắc phân chia: **litellm chỉ trả lời "endpoint này hỗ trợ gì", agentcore chỉ trả lời "đặt điểm cache ở đâu",
tầng ứng dụng chỉ trả lời "định danh là gì"**. Mỗi tầng kiểm chứng được riêng, đổi một ứng dụng khác (codebot dùng lại cùng
agentcore/litellm) không phải viết lại logic cache.

---

## 4. Nền móng: ba kỷ luật ổn định byte tiền tố

Tiền đề của lợi ích cache là tiền tố byte ổn định. Ba kỷ luật, mỗi cái tương ứng một sự cố thật.

### Kỷ luật một: tuần tự hóa tools phải tất định byte

Sự cố: công cụ `subagent` nhúng danh sách agent đã đăng ký vào Description/Schema của chính nó, mà danh sách lấy từ
vòng lặp Go map — thứ tự ngẫu nhiên mỗi lần gọi, byte của tools đổi mỗi lượt, tỷ lệ trúng của coordinator vì thế chỉ 2.7%.
(Nhóm Claude Code cũng từng bị đúng vấn đề này: toàn bộ fleet của họ từng phải trả thêm 10.2% chi phí ghi cache vì nó.)

Sửa (agentcore `subagent/subagent.go`):

```go
// sortedAgentNames returns registered agent names in deterministic order.
// Description and Schema are rebuilt on every LLM call; iterating the map
// directly would shuffle their bytes across requests and defeat provider
// prefix caching (tools serialize into the cached prompt prefix).
func (t *Tool) sortedAgentNames() []string {
	return slices.Sorted(maps.Keys(t.agents))
}
```

> Dạng tổng quát của bài học: **bất kỳ tập hợp nào đi vào thân yêu cầu, trước khi tuần tự hóa đều phải sắp xếp**. Việc Go
> ngẫu nhiên hóa vòng lặp map giấu bug này rất sâu — chức năng hoàn toàn bình thường, chỉ có hoá đơn là bất thường.

### Kỷ luật hai: lịch sử phải append-only (nén phải "commit")

Sự cố: chiến lược nén ngữ cảnh của writer là "chiếu trước" (mỗi lần gọi tạm viết lại khung nhìn lịch sử, nhưng không ghi
ngược về đường cơ sở). Hễ vượt ngưỡng, **mỗi lượt đều viết lại toàn bộ tiền tố** → mỗi lượt miss sạch.

Sửa: chiếu rồi commit (`CommitOnProject: true`), để việc viết lại chỉ xảy ra một lần, sau đó khôi phục
append-only cho tới lần vượt ngưỡng sau.

> Dạng tổng quát: nén ngữ cảnh là **một lần đứt gãy trong kế hoạch** (đặt lại tiền tố, trả giá đầy một lần),
> điều đó không sao; không chấp nhận được là **đứt mỗi lượt**. Nén hoặc không làm, hoặc làm xong thì cố định lại.

### Kỷ luật ba: nội dung động đi vào phần đuôi

Thứ đổi mỗi lượt (phong bì trạng thái thế giới, nhắc nhở mỗi lượt, kết quả công cụ mới nhất) chỉ được **nối vào đuôi tin nhắn**,
tuyệt đối không quay lại sửa đoạn giữa. Phong bì `novel_context` của ainovel chính là thiết kế nối đuôi — nó đổi mỗi chương,
nhưng việc nó đổi không ảnh hưởng cache của hàng trăm nghìn token phía trước.

---

## 5. Định danh cache: một sách một gốc, một vai một tên, một phiên một key

`prompt_cache_key` của họ OpenAI giải quyết **vấn đề định tuyến**: yêu cầu có byte giống nhau nếu bị cân bằng tải sang
instance khác thì vẫn miss. Mục tiêu thiết kế key là "mọi yêu cầu cùng một dòng máu cache luôn mang cùng một key".

Ba cấp định danh của chúng tôi (ainovel `internal/agents/build.go`):

```go
// promptCacheBase derives a stable short hash from the book directory as the prompt-cache
// identity prefix: the same book shares routing buckets across process restarts, and no local
// path leaks to the provider. The role suffix is appended by the caller, and each subagent
// spawn appends "#seq" (one key per session).
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}
```

Tích hợp ở tầng ứng dụng chỉ là hai dòng mỗi agent:

```go
writer := subagent.Config{
	// ...
	CacheLastMessage: "ephemeral",                // công tắc điểm ngắt Claude (xem §6)
	PromptCacheKey:   cacheBase + "-writer",      // định danh định tuyến OpenAI (cấp vai)
}
// coordinator (Agent tầng trên) tương tự:
agentcore.WithCacheLastMessage("ephemeral"),
agentcore.WithPromptCacheKey(cacheBase+"-coordinator"),
```

Cấp thứ ba (cấp phiên) do agentcore tự dẫn xuất — mỗi lần spawn một phiên mới tức là một dòng máu cache mới
(agentcore `subagent/subagent.go`):

```go
runSeq := t.runSeq.Add(1)

// One conversation, one cache key: suffix the per-run sequence so each
// spawn forms its own cache lineage instead of piling every run of this
// agent into a single routing bucket.
promptCacheKey := cfg.PromptCacheKey
if promptCacheKey != "" {
	promptCacheKey = fmt.Sprintf("%s#%d", promptCacheKey, runSeq)
}
```

Hình thái cuối cùng: `nvl-a1b2c3-writer#17` = cuốn sách này, vai writer, phiên spawn thứ 17.

> Vì sao không dùng một key toàn cục? Tiền tố của các phiên khác nhau khác nhau, trộn vào một bộ định tuyến sẽ pha loãng lượng trúng.
> Vì sao không mang dấu thời gian/số ngẫu nhiên? Key phải **ổn định xuyên yêu cầu**, trong một phiên lượt nào cũng phải giống nhau.

Thiết kế tương ứng của codebot: ngữ nghĩa key = SessionID (đổi phiên = đổi dòng máu), teammate nối thêm hậu tố tên,
khi host dùng lại cùng một instance Agent mà đổi phiên thì gọi `Agent.SetPromptCacheKey` để trỏ lại định danh.

---

## 6. Dàn dựng điểm ngắt Claude: sàn + đầu mút cuộn

Anthropic không đặt điểm ngắt = zero cache. Phân bổ ngân sách của chúng tôi (giới hạn 4 điểm ngắt mỗi yêu cầu):

```
[tools][system ←điểm ngắt ①"sàn"][...tin nhắn lịch sử...][tin nhắn mới nhất ←điểm ngắt ②"đầu mút cuộn"]
```

### 6.1 Sàn (floor): ghim tiền tố tĩnh

System prompt là khối tĩnh lớn nhất. Cho nó một điểm ngắt riêng, bảo đảm **khi phiên mới/cache đuôi bị trục xuất,
ít nhất tiền tố system+tools vẫn đọc từ cache** (agentcore `loop.go`):

```go
} else if agentCtx.SystemPrompt != "" {
	m := SystemMsg(agentCtx.SystemPrompt)
	if config.CacheLastMessage != "" {
		// Cache floor: pin the static system prompt with its own
		// breakpoint so a fresh session — or a turn whose tail entry was
		// evicted — still reads the system+tools prefix from cache.
		m.Metadata = map[string]any{"cache_control": config.CacheLastMessage}
	}
	prefix = append(prefix, m)
}
```

### 6.2 Đầu mút cuộn (rolling tip): mỗi lượt đẩy độ phủ tiến lên

Đặt một điểm ngắt lên **tin nhắn cuối cùng không phải system**. Trong vòng lặp công cụ, mỗi lệnh gọi LLM đều ghi một
cache phủ tới tool_use+tool_result mới nhất, lượt sau đọc thẳng, không truyền lại:

```go
// markLastMessageForCache returns a copy of messages with cache_control attached
// to the metadata of the last non-system message. System messages are skipped so
// trailing per-turn reminders (which change every turn) don't end up carrying
// the breakpoint.
func markLastMessageForCache(messages []Message, cacheControl string) []Message {
	idx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != RoleSystem {
			idx = i
			break
		}
	}
	// ...
}
```

Lưu ý bỏ qua system reminder ở đuôi: nó đổi mỗi lượt, đặt điểm ngắt lên nó tức là mỗi lượt ghi một cache
mãi mãi không được dùng lại.

### 6.3 Ngữ nghĩa khối cuối: một tin nhắn chỉ đốt một điểm ngắt

`cache_control` ở mức tin nhắn có nghĩa là "ghi một điểm ngắt sau tin nhắn này". Khi dịch xuống mức khối chỉ được
rơi vào **khối cache được cuối cùng** — đánh dấu mọi khối sẽ đốt sạch ngân sách 4 điểm ngắt; còn Anthropic
từ chối khối thinking mang `cache_control`, nên phải quét từ đuôi, bỏ qua reasoning
(agentcore `llm/litellm.go`):

```go
if cache != nil {
	// Anthropic rejects cache_control on thinking blocks — land the
	// breakpoint on the last cacheable block instead.
	for i := len(blocks) - 1; i >= 0; i-- {
		if _, isReasoning := blocks[i].(litellm.ReasoningBlock); isReasoning {
			continue
		}
		blocks[i] = withBlockCache(blocks[i], cache)
		break
	}
}
```

### 6.4 Ống dẫn TTL

Giá trị cấu hình quy ước là chuỗi `"type[:ttl]"`, ví dụ `"ephemeral"` (mặc định 5 phút) hoặc `"ephemeral:1h"`:

```go
func cacheControlFromMetadata(metadata map[string]any) *litellm.CacheControl {
	value, _ := metadata["cache_control"].(string)
	if value == "" {
		return nil
	}
	if typ, ttl, ok := strings.Cut(value, ":"); ok {
		return &litellm.CacheControl{Type: typ, TTL: ttl}
	}
	return &litellm.CacheControl{Type: value}
}
```

Có nâng lên 1h hay không thì để dữ liệu nói: giá ghi từ 1.25x lên 2x, chỉ đáng khi thực đo thấy khoảng cách giữa các
lệnh gọi thường vượt 5 phút (chúng tôi đo được khoảng cách trung vị của coordinator là 172s, nên không nâng).

---

## 7. Gửi an toàn: cổng năng lực + phán định endpoint chính thức

### 7.1 Cổng năng lực: trường không được hỗ trợ thì không ra khỏi cửa

Các provider của litellm **kiểm tra nghiêm ngặt** `ProviderOptions` (khóa lạ báo lỗi thẳng), nên
agentcore cổng theo khai báo năng lực trước khi gửi (agentcore `llm/litellm.go`):

```go
// Prompt-cache routing identity. Capability-gated: litellm providers
// validate provider options strictly, so an unsupported key must be
// dropped here rather than rejected there.
if callCfg.PromptCacheKey != "" && caps.Cache.PromptKey == litellm.SupportYes {
	req.ProviderOptions["prompt_cache_key"] = callCfg.PromptCacheKey
}
```

### 7.2 Phán định endpoint chính thức: hệ sinh thái tương thích không có khế ước trường lạ

`prompt_cache_key` là trường chính thức của OpenAI, nhưng hành vi của endpoint "tương thích OpenAI" không có khế ước
thống nhất nào. Kiểm chứng qua mạng (2026-07):

- **Endpoint nghiêm ngặt từ chối thẳng**: Groq, Cerebras, Volcano Engine, Fireworks trả 400/422 cho trường này
  (Zed #36215, OpenClaw #48155 đều vì thế phải chuyển sang gửi có điều kiện);
- **Trung chuyển dạng tái biên dịch bỏ im lặng**: đường không truyền thẳng của one-api/new-api/sub2api phân tích thân yêu cầu vào
  struct rồi re-marshal, trường lạ biến mất không tiếng động (gửi cũng như không);
- **Endpoint nới lỏng bỏ qua**: Ollama, vLLM bản hiện tại, MiniMax.

Nên khai báo năng lực của provider openai trong litellm phán định **động** theo BaseURL
(litellm `provider/openai/capabilities.go`):

```go
// promptCacheParamsSupport reports whether this endpoint is trusted to accept
// OpenAI's prompt cache params (prompt_cache_key / prompt_cache_retention).
// Only the official endpoint guarantees the field contract.
func (p *Provider) promptCacheParamsSupport() litellm.Support {
	if p.cfg.PromptCacheParams || isOfficialBaseURL(p.cfg.BaseURL) {
		return litellm.SupportYes
	}
	return litellm.SupportUnknown
}

func isOfficialBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}
```

`api.openai.com` chính thức → `SupportYes` (gửi); BaseURL bên thứ ba → `SupportUnknown`
(cổng ở §7.1 tự động không gửi, **mặc định không bao giờ làm nổ endpoint nào**); người dùng đã xác nhận trung chuyển của mình
truyền thẳng nguyên dạng thì opt-in tường minh trong cấu hình provider:

```jsonc
"my-relay": {
  "type": "openai",
  "base_url": "https://relay.example.com/v1",
  "extra": { "prompt_cache_params": true }   // tôi xác nhận trung chuyển này truyền thẳng thân yêu cầu
}
```

> Vì sao công tắc đặt ở tầng năng lực của litellm chứ không phải tầng cấu hình ứng dụng? Vì lúc chạy, `/model` đổi provider
> sẽ đổi client, khai báo năng lực tự chuyển theo client; phán định lúc dựng ứng dụng không phủ được việc đổi lúc chạy.

---

## 8. Quan sát: phát hiện đứt chuỗi cache

Cache là "chức năng vô hình" — hỏng không báo lỗi, chỉ đắt hơn. Nên phải có quan sát (mượn
promptCacheBreakDetection của Claude Code, làm bản nhẹ).

Tiêu chí phán định (ainovel `internal/host/usage.go`):

```go
// trong cùng một phiên (role+task): tiền tố không ngắn đi, mà lượng trúng giảm >5% so với lần trước và mức giảm ≥2000 tokens
broke := prevPrefix > 0 && prefix >= prevPrefix &&
	float64(u.CacheRead) < float64(prevRead)*cacheBreakKeepRatio &&
	prevRead-u.CacheRead >= cacheBreakMinDropTokens
```

Bốn thiết kế then chốt, mỗi cái ứng với một loại báo nhầm:

| Thiết kế | Chống báo nhầm gì |
|---|---|
| **Hai ngưỡng** (tương đối 5% và tuyệt đối 2000) | Ngưỡng tương đối đơn độc bị nhiễu tiền tố nhỏ nhấn chìm; ngưỡng tuyệt đối đơn độc bỏ sót thoái hóa tiền tố lớn |
| **Đường cơ sở đi theo phiên (role+task)** | Chiều phát hiện phải khớp với độ chi tiết phiên (`#seq`) của `prompt_cache_key`; so sánh theo role xuyên phiên sẽ báo nhầm khi "phiên trước rất ngắn, yêu cầu đầu của phiên mới lại có tiền tố dài hơn" (lỗ hổng thật mà Codex review bắt được) |
| **Tiền tố ngắn đi = đặt lại hợp lệ** | Nén ngữ cảnh là đứt gãy trong kế hoạch, đặt lại đường cơ sở không cảnh báo |
| **replay không phát hiện** | Phát lại lịch sử lúc khởi động sẽ quét các đứt gãy cũ thành cảnh báo mới |

Khi cảnh báo thì gợi ý quy kết theo khoảng thời gian: khoảng >1h → nghi ngờ TTL 1h hết hạn; >5 phút → nghi ngờ TTL 5 phút hết hạn;
rất ngắn → nghi ngờ máy chủ trục xuất/định tuyến trôi dạt (**trung chuyển luân phiên tài khoản thượng nguồn là nguyên nhân phổ biến nhất**). Số đếm được lưu vào
`usage.json` và hiển thị ở dòng "đứt chuỗi" trong bảng cache của TUI.

---

## 9. Đường đỏ kiểu chốt cửa: nguyên tắc đơn điệu trong phiên

Một ràng buộc cấp hiến pháp cho các chức năng tương lai:

> **Mọi lượng đi vào tiền tố cache (system prompt, tools, tham số thinking, tham số lấy mẫu),
> sau khi tính lần đầu trong phiên đều phải đóng băng — thà cũ còn hơn phá cache.**

Ví dụ: chức năng kiểu "chỉnh mức thinking lúc chạy", nếu để mức mới có hiệu lực ngay với phiên đang diễn ra,
tức là mỗi lần chỉnh đều viết lại tiền tố, vô hiệu hóa toàn bộ cache. Cách đúng là giá trị mới chỉ có hiệu lực với
**phiên spawn mới**. Mọi nhu cầu "chỉnh X lúc chạy" khi thẩm định, câu hỏi đầu tiên đều là: X có nằm trong tiền tố cache không?

---

## 10. Ngộ nhận thường gặp và trần

1. **`cache_write` của OpenAI luôn 0 là bình thường** — API không báo lượng ghi, đừng tra như bug.
2. **Trần của trung chuyển**: nếu trung chuyển luân phiên nhiều tài khoản thượng nguồn, byte phía client có ổn định đến đâu vẫn miss (cache của
   tài khoản thượng nguồn A không nhìn thấy được từ tài khoản B). Điều này giải thích câu đố "yêu cầu có byte hoàn toàn giống nhau chỉ trúng 12/33".
   **Đây không phải vấn đề client giải được** — dữ liệu của nhóm Claude Code cũng cho thấy khoảng chín phần mười ca "client không đổi
   mà vẫn đứt" là do phía máy chủ.
3. **Tiêu chí kiểm chứng**: JSONL của phiên không chứa system prompt và thân yêu cầu đầy đủ, **chuỗi usage từng yêu cầu
   (input vs cache_read) mới là chuẩn vàng chẩn đoán**. Một dấu vân tay hữu dụng: nếu lượng trúng đúng bằng
   "số token của system prompt làm tròn xuống 128", nghĩa là chỉ đoạn system trúng, đoạn tin nhắn miss sạch.
4. **Hạch toán lợi ích**: giá đọc 0.1x, giá ghi 1.25x, nghĩa là một cache chỉ cần được đọc 1 lần là hoàn vốn.
   Trong phiên agent nhiều lượt, điểm ngắt gần như luôn có lợi ròng, nên `CacheLastMessage` không đặt công tắc, mặc định bật.

---

## 11. Tra nhanh hướng dẫn tích hợp

**ainovel-cli** (đã tích hợp sẵn): mỗi agent cấu hình `CacheLastMessage: "ephemeral"` +
`PromptCacheKey: promptCacheBase(bookDir) + "-<role>"`, phần còn lại tự động.

**codebot** (đã tích hợp sẵn): key = SessionID; khi `Reset`/`SwitchSession` thì
`agent.SetPromptCacheKey(newSessionID)`; teammate dùng `sessionID + "-" + name`.

**Ứng dụng mới nối vào agentcore**, danh sách tối thiểu:

```go
agentcore.NewAgent(
	agentcore.WithCacheLastMessage("ephemeral"),   // điểm ngắt Claude: sàn + đầu mút cuộn
	agentcore.WithPromptCacheKey(stableIdentity),  // định tuyến OpenAI: ổn định, duy nhất mỗi phiên
	// ...
)
```

Kèm ba câu tự kiểm (ứng với ba kỷ luật):

1. Tuần tự hóa tools của tôi có tất định byte không? (Mọi tập hợp đã sắp xếp chưa?)
2. Lịch sử của tôi có append-only không? (Nén có commit không?)
3. Nội dung đổi mỗi lượt của tôi có nằm ở đuôi không?

---

## 12. Danh sách kinh nghiệm cho người học

- Bản chất tối ưu cache là **kỷ luật byte**, không phải chỉnh tham số: trước tiên bảo đảm tiền tố ổn định, rồi mới nói tới key và điểm ngắt.
- Chẩn đoán luôn bắt đầu từ **chuỗi usage từng yêu cầu**, đừng đoán từ mã.
- Ngẫu nhiên hóa vòng lặp Go map + tuần tự hóa thân yêu cầu = sát thủ cache khó thấy nhất, test chức năng mãi mãi không phát hiện được.
- "Tương thích OpenAI" là từ marketing chứ không phải khế ước: trước khi gửi trường chính thức cho endpoint bên thứ ba, hãy tìm bằng chứng gốc
  (mã nguồn/issue/cách sửa đã lên sóng của client cùng loại), "thường thì sẽ bỏ qua" là suy đoán nguy hiểm.
- Quan sát phải ưu tiên chống báo nhầm: chiều phát hiện phải khớp độ chi tiết của dòng máu cache; thà bỏ sót còn hơn báo nhầm,
  nếu không cảnh báo sẽ nhanh chóng bị phớt lờ.
- Tiêu chí kiểm chứng của phân tầng: khi đổi sang ứng dụng khác (codebot) tích hợp, logic cache không phải viết lại một dòng nào.

---

### Phụ lục: chỉ mục mã nguồn

| Chủ đề | Vị trí |
|---|---|
| Sắp xếp tất định tools | agentcore `subagent/subagent.go` `sortedAgentNames` |
| Dẫn xuất key cấp phiên (#seq) | agentcore `subagent/subagent.go` `runAgent` |
| Sàn system + đầu mút cuộn | agentcore `loop.go` `callLLM` / `markLastMessageForCache` |
| Điểm ngắt khối cuối + bỏ thinking | agentcore `llm/litellm.go` `convertAgentBlocks` |
| Phân tích TTL ("ephemeral:1h") | agentcore `llm/litellm.go` `cacheControlFromMetadata` |
| Cổng năng lực | agentcore `llm/litellm.go` `applyCallConfig` |
| Phán định endpoint chính thức + opt-in | litellm `provider/openai/capabilities.go` / `provider.go Config` |
| Định danh cache (một sách một gốc) | ainovel `internal/agents/build.go` `promptCacheBase` |
| Phát hiện đứt chuỗi | ainovel `internal/host/usage.go` `noteCacheBreak` |
| Định vị kiến trúc | ainovel `docs/architecture.md` §6.6 |