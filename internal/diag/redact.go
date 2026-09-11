package diag

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SkelEvent is a session message's behaviour skeleton after redaction: structural signals are kept (role / tool / error /
// repetition fingerprint) while every piece of free text (prose, prompt, thinking) is redacted. This is a stricter
// projection than store.compactMessage — the latter compresses by size (>4KB), whereas this ignores size and lets no text
// out at all.
type SkelEvent struct {
	Agent    string     // Source session: writer-ch07 / architect-arc02 ...
	Role     string     // assistant / tool / user
	Tools    []SkelTool // Tool calls inside this message
	ErrClass string     // role=tool with is_error: first line of the error (framework error string, no prose)
	TextSha  string     // Short hash of the redacted prose; same sha = the same passage regenerated (loop signal)
	Redacted int        // Number of text/thinking blocks redacted in this entry (for the redaction self-check)
}

// SkelTool is the redacted projection of one tool call.
type SkelTool struct {
	Name     string            // Tool name (structural signal, no prose)
	Args     map[string]string // key -> raw scalar / short quoted string / "<redacted len sha>"
	Invalid  bool              // ArgsInvalid: the arguments the model sent cannot be parsed (#34 signal)
	ParseErr string            // ArgsParseError: reason parsing failed
}

// redactMessage projects one agentcore.Message into a behaviour skeleton.
func redactMessage(agent string, m agentcore.Message) SkelEvent {
	ev := SkelEvent{Agent: agent, Role: string(m.Role)}
	isErr, _ := m.Metadata["is_error"].(bool)

	var text strings.Builder
	for _, b := range m.Content {
		switch b.Type {
		case agentcore.ContentText:
			// A tool error result keeps its first line: this is our own error string (InputValidationError, say), carries no
			// prose and is the key to locating the loop. All other text goes into the redaction pool.
			if m.Role == agentcore.RoleTool && isErr && ev.ErrClass == "" {
				ev.ErrClass = firstLine(b.Text, 160)
				continue
			}
			if strings.TrimSpace(b.Text) != "" {
				text.WriteString(b.Text)
				ev.Redacted++
			}
		case agentcore.ContentThinking:
			if strings.TrimSpace(b.Thinking) != "" {
				text.WriteString(b.Thinking)
				ev.Redacted++
			}
		case agentcore.ContentToolCall:
			if b.ToolCall != nil {
				ev.Tools = append(ev.Tools, redactToolCall(b.ToolCall))
			}
		}
	}
	if t := text.String(); t != "" {
		ev.TextSha = shortHash(t)
	}
	return ev
}

// redactToolCall projects one tool call: tool name + arguments (values redacted) + parse-error marker.
func redactToolCall(tc *agentcore.ToolCall) SkelTool {
	return SkelTool{
		Name:     tc.Name,
		Args:     redactArgs(tc.Args),
		Invalid:  tc.ArgsInvalid,
		ParseErr: tc.ArgsParseError,
	}
}

// redactArgs projects a tool arguments object into key → redacted value. A non-object argument returns nil
// (ArgsInvalid/ParseErr are recorded separately in SkelTool).
func redactArgs(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = projectValue(v)
	}
	return out
}

// projectValue projects a single argument value by JSON type:
//   - scalar (number / bool / null): the value itself is the structural signal, kept as-is (chapter: 7)
//   - a short identifier-like string: kept with quotes, exposing the type (chapter: "7" ← #34's stringified-number signal)
//   - a string with non-ASCII / spaces / long text, an object or an array: redacted to <redacted …> (zero prose leakage)
//   - already a [session_compact: …] placeholder: safe and informative, kept verbatim
func projectValue(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return ""
	}
	switch s[0] {
	case '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return redactPlaceholder(s)
		}
		if strings.HasPrefix(str, store.CompactTag) {
			return str
		}
		// Only short values that "look like an identifier/number/enum" are kept (chapter:"7", type:"premise",
		// agent:"writer"); any string with non-ASCII characters, spaces or other symbols counts as prose and is redacted.
		if utf8.RuneCountInString(str) <= 32 && isStructuralToken(str) {
			return strconv.Quote(str)
		}
		return redactPlaceholder(str)
	case '{':
		return fmt.Sprintf("<redacted object len=%d>", len(raw))
	case '[':
		return fmt.Sprintf("<redacted array len=%d>", len(raw))
	default:
		return s
	}
}

// isStructuralToken decides whether a string "looks like an identifier" — pure-ASCII letters / digits / `_-.:/`, with no
// spaces and no non-ASCII. It separates structural signals (kept) from prose fragments (redacted).
func isStructuralToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}

func redactPlaceholder(s string) string {
	return fmt.Sprintf("<redacted len=%d sha=%s>", utf8.RuneCountInString(s), shortHash(s))
}

// shortHash takes a short hash of text; used only to judge whether the same text recurs, not for cryptographic purposes.
func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// firstLine takes the first line and truncates by rune, for error-string summaries.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return s
}
