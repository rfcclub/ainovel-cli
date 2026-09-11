package host

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Cold-start co-creation: clarify requirements from scratch, produce the creation
// brief for the whole book.
const coCreateSystemPrompt = `Bạn là trợ lý đồng sáng tác tiểu thuyết. Nhiệm vụ của bạn không phải bắt tay viết tiểu thuyết ngay, mà là qua vài lượt đối thoại ngắn giúp người dùng làm rõ nhu cầu sáng tác, đồng thời liên tục duy trì một bản hướng dẫn sáng tác bằng Tiếng Việt có thể giao thẳng cho engine sáng tác.

Mỗi lượt trả lời bắt buộc xuất ra đúng định dạng XML sau, gồm bốn thẻ, xuất hiện lần lượt, mỗi thẻ đều phải có thẻ mở và thẻ đóng đúng cú pháp:

<reply>
Trả lời tự nhiên bằng Tiếng Việt cho người dùng thấy: trước tiên phản hồi điều người dùng vừa nhập, sau đó đặt tối đa 1 đến 2 câu hỏi then chốt nhất ở thời điểm hiện tại. Nếu thông tin đã đủ để bắt đầu sáng tác, hãy báo người dùng có thể nhấn Ctrl+S để bắt đầu.
</reply>

<draft>
Bản hướng dẫn sáng tác đầy đủ hiện tại, dùng Markdown: bắt đầu thẳng từ tiêu đề cấp hai, ví dụ "## Chủ đề", "## Yếu tố then chốt", "## Thông tin cần làm rõ"; dùng gạch đầu dòng liệt kê các điểm. Mỗi lượt đều phải **cập nhật tích lũy** trên kết luận đã có, hấp thụ ý định mới nhất của người dùng; kể cả lượt này không có gì mới cũng phải viết lại nguyên vẹn bản đầy đủ — không được lược bớt, không được viết các chỗ giữ chỗ kiểu "（giữ nguyên lượt trước）".
</draft>
` + coCreateProtocolTail

// Stage co-creation: the novel already has some chapters; plan where the "next stage"
// goes. The caller appends the current story-state summary after this prompt
// (a "## Trạng thái truyện hiện tại" section) so the model plans on top of what is
// already written.
const stageCoCreateSystemPrompt = `Bạn là trợ lý "đồng sáng tác giai đoạn" cho tiểu thuyết. Cuốn tiểu thuyết này đã viết được một phần (tiến độ xem phần "Trạng thái truyện hiện tại" bên dưới). Người dùng tạm dừng lại, muốn cùng bạn quy hoạch hướng đi của "giai đoạn tiếp theo" rồi tiếp tục sáng tác.

Nhiệm vụ của bạn không phải viết tiếp chính văn, mà là qua vài lượt đối thoại ngắn giúp người dùng nghĩ rõ đoạn sắp tới (mấy chương tới / cung tiếp theo / tập tiếp theo) sẽ đi về đâu, đồng thời liên tục duy trì một "brief hướng đi tiếp theo" để engine sáng tác dựa vào đó mà triển khai.

Nguyên tắc sắt: mọi đề xuất phải nhất quán với cốt truyện, nhân vật và phục bút đã xảy ra trong "Trạng thái truyện hiện tại", tuyệt đối không lật ngược hay bỏ qua nội dung đã viết; chỉ quy hoạch "đoạn sau đi thế nào", không thiết kế lại toàn bộ cuốn sách.

Mỗi lượt trả lời bắt buộc xuất ra đúng định dạng XML sau, gồm bốn thẻ, xuất hiện lần lượt, mỗi thẻ đều phải có thẻ mở và thẻ đóng đúng cú pháp:

<reply>
Trả lời tự nhiên bằng Tiếng Việt cho người dùng thấy: trước tiên phản hồi điều người dùng vừa nhập, sau đó đặt tối đa 1 đến 2 câu hỏi then chốt nhất ở thời điểm hiện tại. Nếu hướng đi tiếp theo đã đủ rõ, hãy báo người dùng có thể nhấn Ctrl+S để giao hướng đi cho engine sáng tác và tiếp tục viết.
</reply>

<draft>
"Brief hướng đi tiếp theo" đầy đủ hiện tại, dùng Markdown: bắt đầu thẳng từ tiêu đề cấp hai, ví dụ "## Hướng đi tiếp theo", "## Bước ngoặt then chốt", "## Phục bút cần thu hồi", "## Nhịp điệu và dung lượng"; dùng gạch đầu dòng liệt kê các điểm. Mỗi lượt đều phải **cập nhật tích lũy** trên kết luận đã có, hấp thụ ý định mới nhất của người dùng; kể cả lượt này không có gì mới cũng phải viết lại nguyên vẹn brief đầy đủ — không được lược bớt, không được viết các chỗ giữ chỗ kiểu "（giữ nguyên lượt trước）".
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail is the output-protocol tail shared by both co-creation modes
// (<ready> / <suggestions> + output rules). The two modes differ only in opening
// context and the semantics of <draft>; the protocol is identical.
const coCreateProtocolTail = `
<ready>false</ready>

<suggestions>
1-3 câu "người dùng có thể muốn nói tiếp", mỗi dòng một câu bắt đầu bằng "- ". Đây là gợi ý khi người dùng bí ý,
nhấn phím số để điền vào ô nhập, người dùng có thể sửa lại rồi gửi.

Yêu cầu:
- Đứng ở giọng người dùng, như lời người dùng nói với bạn, không viết thành câu hỏi ngược của trợ lý.
- Mỗi câu không quá 25 chữ, đa dạng kiểu câu, tránh rập khuôn.
- Nêu khuynh hướng / lựa chọn / ý định bổ sung, đừng viết trọn thiết lập thay người dùng trong một câu.
</suggestions>

Quy cách xuất ra:
- Bắt buộc dùng bốn thẻ XML: <reply> / <draft> / <ready> / <suggestions>, mỗi thẻ đều phải mở và đóng đầy đủ.
- Tên thẻ chỉ được viết chữ thường tiếng Anh, không được đổi thành <REPLY> / <REWRITE> hay bất kỳ biến thể nào.
- Không thêm bất kỳ giải thích, suy nghĩ hay hàng rào code nào bên ngoài thẻ.
- Trong <draft> được phép có Markdown nhiều dòng, viết xuống dòng trực tiếp, không cần escape gì.
- <ready> chỉ viết true hoặc false. Khi thông tin đã đủ thì điền true.
- Khi <ready>true</ready> thì <suggestions> có thể để trống (giữ thẻ rỗng <suggestions></suggestions> là được).`

// CoCreateProgressKind marks the content type of a streaming callback.
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

// Four-part XML tag output. The XML style is more robust than bracketed markers —
// Claude/GPT training data is full of <thinking>...</thinking> shapes, so models
// almost never rewrite <reply> into <REWRITE> or another variant; closing tags also
// make mid-stream truncation more precise (no need to find the next marker to end).
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string)) (reply CoCreateReply, err error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}

	model := models.ForRole("thinking")

	msgs := []agentcore.Message{agentcore.SystemMsg(sysPrompt)}
	for _, item := range history {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "assistant":
			msgs = append(msgs, assistantMsg(content))
		default:
			msgs = append(msgs, agentcore.UserMsg(content))
		}
	}

	var raw, thinking strings.Builder

	// Debugging occasional issues such as "cocreate empty response" requires seeing what
	// the model actually returned. Every round is logged in full to
	// <output>/meta/sessions/cocreate.jsonl, alongside the regular writing session logs.
	start := time.Now()
	defer func() {
		if sessions == nil {
			return
		}
		if logErr := sessions.LogCoCreate(coCreateLogEntry{
			Time:         time.Now(),
			DurationMS:   time.Since(start).Milliseconds(),
			InputHistory: history,
			RawResponse:  raw.String(),
			RawLen:       len([]rune(raw.String())),
			Thinking:     thinking.String(),
			ParsedReply:  reply.Message,
			ParsedDraft:  reply.Prompt,
			ParsedReady:  reply.Ready,
			ParsedSugs:   reply.Suggestions,
			Error:        errString(err),
		}); logErr != nil {
			slog.Warn("ghi log phiên đồng sáng tác thất bại", "module", "cocreate", "err", logErr)
		}
	}()

	streamCh, err := model.GenerateStream(ctx, msgs, nil, agentcore.WithMaxTokens(2048))
	if err != nil {
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", err)
	}

	var streamed bool
	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventThinkingDelta:
			thinking.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressThinking, thinking.String())
			}
		case agentcore.StreamEventTextDelta:
			streamed = true
			raw.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressReply, extractReplyPreview(raw.String()))
			}
		case agentcore.StreamEventDone:
			if !streamed {
				raw.WriteString(ev.Message.TextContent())
			}
		case agentcore.StreamEventError:
			if ev.Err != nil {
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", ev.Err)
			}
			return CoCreateReply{}, fmt.Errorf("cocreate generate failed")
		}
	}

	// Channel fallback: reasoning models (R1/GLM-Z1/QwQ and friends) occasionally write
	// the whole answer into reasoning_content and never switch back to the final-answer
	// channel, leaving raw empty while thinking holds the complete four-part payload.
	// Observed in meta/sessions/cocreate.jsonl — parsing thinking as raw directly works,
	// because the protocol layer already degrades gracefully (with no [REPLY] marker the
	// whole text becomes the reply), so the recovered UI experience is indistinguishable.
	rawText := raw.String()
	if strings.TrimSpace(rawText) == "" {
		if t := strings.TrimSpace(thinking.String()); t != "" {
			rawText = t
		}
	}
	reply, err = parseCoCreateResponse(rawText)
	return reply, err
}

// coCreateLogEntry is one line written to meta/sessions/cocreate.jsonl.
// Field names follow jsonl query habits (snake_case) so jq filters stay short.
type coCreateLogEntry struct {
	Time         time.Time         `json:"time"`
	DurationMS   int64             `json:"duration_ms"`
	InputHistory []CoCreateMessage `json:"input_history"`
	RawResponse  string            `json:"raw_response"`
	RawLen       int               `json:"raw_len"`
	Thinking     string            `json:"thinking,omitempty"`
	ParsedReply  string            `json:"parsed_reply"`
	ParsedDraft  string            `json:"parsed_draft"`
	ParsedReady  bool              `json:"parsed_ready"`
	ParsedSugs   []string          `json:"parsed_sugs,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

// parseCoCreateResponse parses the XML tag output. If the model ignores the protocol
// and just speaks natural language, the whole text is shown as the reply and draft is
// left empty so the session keeps the previous round's version.
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	reply, draft, ready, suggestions := splitCoCreateMarkers(raw)
	if reply == "" {
		// The model ignored the XML protocol: treat the whole text as the reply.
		return CoCreateReply{Message: raw, Prompt: "", Ready: false, Raw: raw}, nil
	}
	return CoCreateReply{
		Message:     reply,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         raw,
	}, nil
}

// splitCoCreateMarkers splits the text by the four XML tags.
// Tags may be missing (mid-stream or omitted by the model); the corresponding field
// is then empty / false / nil. When a closing tag is missing, extractTagContent takes
// everything to the end of the string and still parses best-effort.
func splitCoCreateMarkers(s string) (reply, draft string, ready bool, suggestions []string) {
	reply = extractTagContent(s, tagReply)
	draft = extractTagContent(s, tagDraft)
	readyStr := strings.ToLower(extractTagContent(s, tagReady))
	ready = readyStr == "true" || readyStr == "yes"
	suggestions = parseSuggestions(extractTagContent(s, tagSuggestions))
	return
}

// extractTagContent digs the text between <tag>...</tag> out of s.
// It covers three occasional failure shapes so a degraded parse does not silently
// drop fields:
//  1. Opening without closing (mid-stream) -> cut at the next known opening tag.
//  2. No opening but a closing tag (model typo, e.g. <suggestions> written as
//     <uggestions>) -> start after the last known complete closing tag, end before </tag>.
//  3. reply with no opening tag at all (the model just starts in natural language and
//     appends </reply>) -> from the start to </reply>.
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// Opening without closing -> cut at the next known opening tag.
		for _, other := range []string{"<reply>", "<draft>", "<ready>", "<suggestions>"} {
			if other == open {
				continue
			}
			if idx := strings.Index(rest, other); idx >= 0 {
				rest = rest[:idx]
			}
		}
		return strings.TrimSpace(rest)
	}

	// No opening but a closing tag -> start after the last known complete closing tag, end before </tag>.
	if cIdx := strings.Index(s, closeTag); cIdx >= 0 {
		prefix := s[:cIdx]
		start := 0
		for _, t := range []string{"</reply>", "</draft>", "</ready>", "</suggestions>"} {
			if t == closeTag {
				continue
			}
			if i := strings.LastIndex(prefix, t); i >= 0 {
				if end := i + len(t); end > start {
					start = end
				}
			}
		}
		return strings.TrimSpace(prefix[start:])
	}
	return ""
}

// parseSuggestions pulls each line of the <suggestions> block out, stripping list
// prefixes such as "- " / "* " / "1. ". At most 3 are kept; blank lines, lines that
// are too short (<2 chars), and lines that look like a whole XML tag (leftovers from
// the typo-opening-tag fallback, e.g. <uggestions>) are ignored.
func parseSuggestions(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Whole line looks like an XML tag -> skip (guards against typo opening tags).
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue
		}
		// Strip the list prefix.
		switch {
		case strings.HasPrefix(line, "- "):
			line = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case isOrderedSuggestion(line):
			line = stripOrderedPrefix(line)
		}
		if len([]rune(line)) < 2 {
			continue
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// isOrderedSuggestion reports whether the line starts like "1. " / "12. " (digits, dot, space).
func isOrderedSuggestion(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func stripOrderedPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// extractReplyPreview is the streaming preview: gives the UI displayable text while
// raw is still growing. It finds the content after <reply> and cuts at </reply> or
// the next opening tag <draft>. When the model half-complies (misses the <reply>
// opening tag), everything from the start to </reply> or <draft> counts as the reply.
func extractReplyPreview(raw string) string {
	trimmed := strings.TrimSpace(raw)
	open := "<" + tagReply + ">"
	closeTag := "</" + tagReply + ">"
	draftOpen := "<" + tagDraft + ">"

	rest := trimmed
	if rIdx := strings.Index(trimmed, open); rIdx >= 0 {
		rest = trimmed[rIdx+len(open):]
	}
	if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
		return strings.TrimSpace(rest[:cIdx])
	}
	if dIdx := strings.Index(rest, draftOpen); dIdx >= 0 {
		rest = rest[:dIdx]
	}
	return strings.TrimSpace(rest)
}
