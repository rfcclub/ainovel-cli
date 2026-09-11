package host

import (
	"strings"
	"unicode/utf8"
)

// toolDisplays configures each tool's display policy on the stream panel. Tools absent from this table
// take no part in streaming rendering (the observer drops their DeltaToolCall outright).
//
// Generic mode (empty nakedKey): the tokenizer renders the LLM's args JSON as indented "key: value"
// text, with nested objects/arrays indented by level and string/number/bool streamed out. It is fully
// decoupled from the schema — one extra field from the LLM simply becomes one extra panel line, with no
// code change needed.
//
// Naked-stream mode (non-empty nakedKey): only the target top-level field's string value streams
// through verbatim and every other field is skipped. It serves draft_chapter, keeping a whole chapter's
// markdown from being decorated as "content: # …".
// Headers always start with "✻ ": that prefix sends TUI renderStreamContent down the renderAgentBlock
// highlight path (gold ✻ + cyan-on-blue underlined label + dim rule), consistent with the fallback
// header (streamHeaderFallback); plain text would fall to the prose path in the terminal's default
// colour and the title would stop standing out.
var toolDisplays = map[string]toolDisplay{
	"draft_chapter": {nakedKey: "content"},

	"plan_chapter":        {header: "✻ Quy hoạch"},
	"edit_chapter":        {header: "✻ Gọt giũa"},
	"commit_chapter":      {header: "✻ Commit chương"},
	"save_review":         {header: "✻ Thẩm duyệt"},
	"save_arc_summary":    {header: "✻ Tóm tắt cung"},
	"save_volume_summary": {header: "✻ Tóm tắt tập"},
	"save_foundation":     {header: "✻ Thiết lập"},
	"revise_outline":      {header: "✻ Tu chỉnh đại cương"},
	"read_chapter":        {header: "✻ Đọc chương"},
	"check_consistency":   {header: "✻ Kiểm tra nhất quán"},
	"novel_context":       {header: "✻ Truy vấn ngữ cảnh"},
}

type toolDisplay struct {
	header   string
	nakedKey string
}

// jsonFieldExtractor is the streaming JSON tokenizer. A byte-driven state machine turns the LLM's tool
// args stream into readable text. One instance serves a single tool call, and Done() becomes true once
// the top-level container closes.
type jsonFieldExtractor struct {
	cfg toolDisplay

	state pState
	stack []byte // Container stack: 'O' obj / 'A' arr

	keyBuf strings.Builder

	escape bool
	uHex   []byte

	started bool // Whether any character has been emitted (governs the newline between the header and the first key)

	done bool
}

type pState int

const (
	psRoot         pState = iota
	psBeforeKey           // inside obj: awaiting the next key or }
	psInKey               // inside obj: parsing a key
	psAfterKey            // inside obj: awaiting :
	psBeforeValue         // awaiting the value start character
	psStringStream        // string value: stream cooked characters
	psStringSkip          // string value: skip (a non-target field in naked-stream mode)
	psNumberStream        // number: stream out
	psNumberSkip          // number: skip
	psPrimStream          // true/false/null: stream out
	psPrimSkip            // true/false/null: skip
	psDone                // top-level container closed
)

func newToolExtractor(tool string) *jsonFieldExtractor {
	cfg, ok := toolDisplays[tool]
	if !ok {
		return nil
	}
	return &jsonFieldExtractor{cfg: cfg}
}

func (e *jsonFieldExtractor) Done() bool { return e.done }

func (e *jsonFieldExtractor) Feed(chunk string) string {
	if e.done || chunk == "" {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(chunk); i++ {
		e.step(chunk[i], &out)
		if e.done {
			break
		}
	}
	return out.String()
}

// ── Container stack / indentation ──

func (e *jsonFieldExtractor) push(kind byte) {
	e.stack = append(e.stack, kind)
}

func (e *jsonFieldExtractor) pop() {
	if len(e.stack) == 0 {
		return
	}
	e.stack = e.stack[:len(e.stack)-1]
}

func (e *jsonFieldExtractor) parent() byte {
	if len(e.stack) == 0 {
		return 0
	}
	return e.stack[len(e.stack)-1]
}

// writeIndent writes the current indentation. Depth = nesting levels = len(stack)-1 (nothing inside the root container is indented).
func (e *jsonFieldExtractor) writeIndent(out *strings.Builder) {
	depth := len(e.stack) - 1
	for range depth {
		out.WriteString("  ")
	}
}

// ── State machine ──

func (e *jsonFieldExtractor) step(c byte, out *strings.Builder) {
	switch e.state {
	case psRoot:
		switch c {
		case '{':
			e.push('O')
			e.state = psBeforeKey
		case '[':
			// Cannot actually happen (tool args are always obj); tolerated: treat as a root arr
			e.push('A')
			e.state = psBeforeValue
		}
	case psBeforeKey:
		switch c {
		case '"':
			e.keyBuf.Reset()
			e.escape = false
			e.state = psInKey
		case '}':
			e.closeContainer(out)
		case ' ', '\t', '\n', '\r', ',':
		}
	case psInKey:
		if e.escape {
			e.keyBuf.WriteByte(c)
			e.escape = false
			return
		}
		if c == '\\' {
			e.escape = true
			return
		}
		if c == '"' {
			e.emitKeyLine(out, e.keyBuf.String())
			e.state = psAfterKey
			return
		}
		e.keyBuf.WriteByte(c)
	case psAfterKey:
		if c == ':' {
			e.state = psBeforeValue
		}
	case psBeforeValue:
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' {
			return
		}
		switch c {
		case '"':
			e.beginString(out)
		case '{':
			e.beginNested('O', out)
		case '[':
			e.beginNested('A', out)
		case ']', '}':
			e.closeContainer(out)
		case 't', 'f', 'n':
			e.beginPrim(c, out)
		default:
			if c == '-' || (c >= '0' && c <= '9') {
				e.beginNumber(c, out)
			}
		}
	case psStringStream:
		e.handleStringByte(c, out, false)
	case psStringSkip:
		e.handleStringByte(c, out, true)
	case psNumberStream:
		if isNumberByte(c) {
			out.WriteByte(c)
			return
		}
		e.afterValueChar(c, out)
	case psNumberSkip:
		if isNumberByte(c) {
			return
		}
		e.afterValueChar(c, out)
	case psPrimStream:
		if c >= 'a' && c <= 'z' {
			out.WriteByte(c)
			return
		}
		e.afterValueChar(c, out)
	case psPrimSkip:
		if c >= 'a' && c <= 'z' {
			return
		}
		e.afterValueChar(c, out)
	case psDone:
	}
}

// ── Line rendering ──

// emitKeyLine is called once a key inside an obj is parsed, writing the "<lf><indent>key:" prefix.
// In naked-stream mode the key prefix is omitted (the key is recorded in keyBuf for beginString to
// check).
func (e *jsonFieldExtractor) emitKeyLine(out *strings.Builder, key string) {
	if e.cfg.nakedKey != "" {
		return
	}
	if !e.started {
		if e.cfg.header != "" {
			out.WriteString(e.cfg.header)
			out.WriteByte('\n')
		}
		e.started = true
	} else {
		out.WriteByte('\n')
	}
	e.writeIndent(out)
	out.WriteString(key)
	out.WriteByte(':')
}

// emitArrayItem is called at the start of each element inside an arr, writing "<lf><indent>-". Primitive
// elements emit their value after a space; struct elements are handled naturally by the following
// nesting.
func (e *jsonFieldExtractor) emitArrayItem(out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		return
	}
	if !e.started {
		if e.cfg.header != "" {
			out.WriteString(e.cfg.header)
			out.WriteByte('\n')
		}
		e.started = true
	} else {
		out.WriteByte('\n')
	}
	e.writeIndent(out)
	out.WriteByte('-')
}

// ── Value start ──

func (e *jsonFieldExtractor) beginString(out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		// Naked stream: only the target key's string value in the top-level obj is emitted
		if e.cfg.nakedKey == e.keyBuf.String() && len(e.stack) == 1 && e.stack[0] == 'O' {
			e.state = psStringStream
		} else {
			e.state = psStringSkip
		}
		e.escape = false
		e.uHex = nil
		return
	}
	// Generic: an obj field is followed by "key: " ("key:" was already emitted, so just add the space); an arr element is followed by "- "
	if e.parent() == 'A' {
		e.emitArrayItem(out)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
	}
	e.state = psStringStream
	e.escape = false
	e.uHex = nil
}

func (e *jsonFieldExtractor) beginNumber(first byte, out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		e.state = psNumberSkip
		return
	}
	if e.parent() == 'A' {
		e.emitArrayItem(out)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
	}
	out.WriteByte(first)
	e.state = psNumberStream
}

func (e *jsonFieldExtractor) beginPrim(first byte, out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		e.state = psPrimSkip
		return
	}
	if e.parent() == 'A' {
		e.emitArrayItem(out)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
	}
	out.WriteByte(first)
	e.state = psPrimStream
}

func (e *jsonFieldExtractor) beginNested(kind byte, out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		// Naked-stream mode does not open nesting; stack depth tracks through to the matching } / ]
		e.push(kind)
		if kind == 'O' {
			e.state = psBeforeKey
		} else {
			e.state = psBeforeValue
		}
		return
	}
	// Generic mode: when an arr element is a nested structure, emit a standalone "<indent>-" line first
	// (obj keys have no space after ":", so the nested child keys wrap naturally onto the next line)
	if e.parent() == 'A' {
		e.emitArrayItem(out)
	}
	e.push(kind)
	if kind == 'O' {
		e.state = psBeforeKey
	} else {
		e.state = psBeforeValue
	}
}

// closeContainer handles } or ].
func (e *jsonFieldExtractor) closeContainer(out *strings.Builder) {
	e.pop()
	if len(e.stack) == 0 {
		// Fallback for empty args (novel_context passes none, say): emitKeyLine never got to output a
		// header, so add one here to avoid ending up with neither title nor content.
		if !e.started && e.cfg.nakedKey == "" && e.cfg.header != "" {
			out.WriteString(e.cfg.header)
			out.WriteByte('\n')
			e.started = true
		}
		// The trailing newline gives a clear boundary between the panel and the next segment
		if e.started {
			out.WriteByte('\n')
		}
		e.state = psDone
		e.done = true
		return
	}
	if e.parent() == 'O' {
		e.state = psBeforeKey
	} else {
		e.state = psBeforeValue
	}
}

// ── String streaming ──

func (e *jsonFieldExtractor) handleStringByte(c byte, out *strings.Builder, skipping bool) {
	if e.uHex != nil {
		e.uHex = append(e.uHex, c)
		if len(e.uHex) == 4 {
			if r, ok := parseHex4(e.uHex); ok && !skipping {
				var buf [4]byte
				n := utf8.EncodeRune(buf[:], r)
				out.Write(buf[:n])
			}
			e.uHex = nil
		}
		return
	}
	if e.escape {
		e.escape = false
		if !skipping {
			writeEscapedByte(out, c)
		}
		if c == 'u' {
			e.uHex = make([]byte, 0, 4)
		}
		return
	}
	if c == '\\' {
		e.escape = true
		return
	}
	if c == '"' {
		e.afterValueDone()
		return
	}
	if !skipping {
		out.WriteByte(c)
	}
}

func writeEscapedByte(out *strings.Builder, c byte) {
	switch c {
	case 'n':
		out.WriteByte('\n')
	case 't':
		out.WriteByte('\t')
	case 'r':
		out.WriteByte('\r')
	case '"':
		out.WriteByte('"')
	case '\\':
		out.WriteByte('\\')
	case '/':
		out.WriteByte('/')
	case 'b', 'f':
		// Backspace / form feed: ignored
	case 'u':
		// The caller sets up the uHex buffer; nothing is emitted here
	default:
		out.WriteByte('\\')
		out.WriteByte(c)
	}
}

// ── Wrap-up ──

// afterValueDone transitions to the next state once a string closes (the closing `"` is read).
func (e *jsonFieldExtractor) afterValueDone() {
	e.escape = false
	e.uHex = nil
	if len(e.stack) == 0 {
		e.state = psDone
		e.done = true
		return
	}
	if e.parent() == 'O' {
		e.state = psBeforeKey
	} else {
		e.state = psBeforeValue
	}
}

// afterValueChar decides the next state from a number / primitive "terminator" already read.
// That character may be , / } / ] / whitespace, and this function forwards it accordingly.
func (e *jsonFieldExtractor) afterValueChar(c byte, out *strings.Builder) {
	switch c {
	case '}', ']':
		e.closeContainer(out)
	case ',', ' ', '\t', '\n', '\r':
		if len(e.stack) == 0 {
			e.state = psDone
			e.done = true
			return
		}
		if e.parent() == 'O' {
			e.state = psBeforeKey
		} else {
			e.state = psBeforeValue
		}
	}
}

// ── Tools ──

func isNumberByte(c byte) bool {
	switch c {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
		'-', '+', '.', 'e', 'E':
		return true
	}
	return false
}

func parseHex4(b []byte) (rune, bool) {
	var r rune
	for _, d := range b {
		var v rune
		switch {
		case d >= '0' && d <= '9':
			v = rune(d - '0')
		case d >= 'a' && d <= 'f':
			v = rune(d-'a') + 10
		case d >= 'A' && d <= 'F':
			v = rune(d-'A') + 10
		default:
			return 0, false
		}
		r = r*16 + v
	}
	return r, true
}
