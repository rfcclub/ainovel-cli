package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/voocel/agentcore"
)

// SessionStore appends LLM conversation history to JSONL files.
// Bulk content (novel prose, full context) is replaced by a [session_compact: ...] placeholder marker.
type SessionStore struct {
	io      *IO
	mu      sync.Mutex
	seq     map[string]int    // Agent run sequence (used when no chapter number can be extracted)
	taskKey map[string]string // "agentName|task" -> suffix; the same run reuses one file
}

func NewSessionStore(io *IO) *SessionStore {
	return &SessionStore{io: io, seq: make(map[string]int), taskKey: make(map[string]string)}
}

// ModelLookup resolves the "effective at the time" provider/model by agent name when the logger writes.
// A func type rather than an interface makes it easy for the caller to inject a normalisation rule as a closure (such as
// architect_short → architect). An empty string means unknown: the caller still writes as usual but without _meta, and
// replay falls back to the ModelSet fallback.
type ModelLookup func(agentName string) (provider, model string)

// SubAgentLogger returns a subagent's OnMessage callback.
func (s *SessionStore) SubAgentLogger(lookup ModelLookup) func(agentName, task string, msg agentcore.AgentMessage) {
	return func(agentName, task string, msg agentcore.AgentMessage) {
		rel, err := s.subAgentPath(agentName, task)
		if err != nil {
			slog.Warn("session log failed", "agent", agentName, "err", err)
			return
		}
		var meta *sessionLogMeta
		if lookup != nil {
			meta = lookupMeta(lookup, agentName)
		}
		if err := s.logEntry(rel, msg, meta); err != nil {
			slog.Warn("session log failed", "agent", agentName, "err", err)
		}
	}
}

func lookupMeta(lookup ModelLookup, agentName string) *sessionLogMeta {
	provider, model := lookup(agentName)
	if provider == "" && model == "" {
		return nil
	}
	return &sessionLogMeta{Provider: provider, Model: model}
}

// LogCoCreate appends one cocreation conversation entry to meta/sessions/cocreate.jsonl.
// The cocreation stage is not yet bound to a specific novel, so it lands under OutputDir's default root (output/novel),
// alongside the agents/* of formal creation, for easier investigation.
func (s *SessionStore) LogCoCreate(entry any) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal cocreate session: %w", err)
	}
	data = append(data, '\n')
	return s.io.AppendLine("meta/sessions/cocreate.jsonl", data)
}

// Log appends one message to the given path, compressing bulk content automatically.
// It carries no _meta and is used only by role-less paths such as cocreate.
func (s *SessionStore) Log(rel string, msg agentcore.AgentMessage) error {
	return s.logEntry(rel, msg, nil)
}

// sessionLogEntry embeds agentcore.Message plus an optional _meta.
// agentcore.Message is a plain struct (no MarshalJSON), so embedding flattens it to the top level under json marshal;
// _meta is controlled by omitempty — injected only for assistant messages with Usage != nil, while user/tool messages
// carry none, making _meta=nil a no-op when parsing old jsonl files.
type sessionLogEntry struct {
	agentcore.Message
	Meta *sessionLogMeta `json:"_meta,omitempty"`
}

type sessionLogMeta struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// logEntry serialises a message and attaches _meta as needed. The already-computed meta from lookupMeta is passed in;
// the function writes meta only for messages that "produced LLM usage" (assistant + Usage != nil), leaving every other
// message in the pure agentcore.Message serialised form.
func (s *SessionStore) logEntry(rel string, msg agentcore.AgentMessage, meta *sessionLogMeta) error {
	m, ok := msg.(agentcore.Message)
	if !ok {
		return nil // Skip non-LLM messages (custom types).
	}
	compacted := compactMessage(m)
	entry := sessionLogEntry{Message: compacted}
	if compacted.Role == agentcore.RoleAssistant && compacted.Usage != nil {
		entry.Meta = usageMeta(compacted.Usage)
		if entry.Meta == nil {
			entry.Meta = meta
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal session message: %w", err)
	}
	data = append(data, '\n')
	return s.io.AppendLine(rel, data)
}

func usageMeta(usage *agentcore.Usage) *sessionLogMeta {
	if usage == nil || (usage.Provider == "" && usage.Model == "") {
		return nil
	}
	return &sessionLogMeta{
		Provider: usage.Provider,
		Model:    usage.Model,
	}
}

// subAgentPath generates a file path from agentName+task.
func (s *SessionStore) subAgentPath(agentName, task string) (string, error) {
	suffix := extractChapter(task)
	if suffix != "" {
		return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, suffix), nil
	}
	key := agentName + "|" + task
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.taskKey[key]; ok {
		return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, cached), nil
	}
	if _, ok := s.seq[agentName]; !ok {
		seq, err := s.maxAgentSequence(agentName)
		if err != nil {
			return "", err
		}
		s.seq[agentName] = seq
	}
	s.seq[agentName]++
	suffix = fmt.Sprintf("%03d", s.seq[agentName])
	s.taskKey[key] = suffix
	return fmt.Sprintf("meta/sessions/agents/%s-%s.jsonl", agentName, suffix), nil
}

func (s *SessionStore) maxAgentSequence(agentName string) (int, error) {
	entries, err := os.ReadDir(s.io.path("meta/sessions/agents"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read agent sessions: %w", err)
	}

	prefix := agentName + "-"
	maxSeq := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		seq, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl"))
		if err == nil && seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq, nil
}

var chapterRe = regexp.MustCompile(`第\s*(\d+)\s*章`)

func extractChapter(task string) string {
	m := chapterRe.FindStringSubmatch(task)
	if len(m) < 2 {
		return ""
	}
	n, _ := strconv.Atoi(m[1])
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("ch%02d", n)
}

// compactMessage clones a message and replaces bulk content.
func compactMessage(m agentcore.Message) agentcore.Message {
	if len(m.Content) == 0 {
		return m
	}
	blocks := make([]agentcore.ContentBlock, len(m.Content))
	copy(blocks, m.Content)

	toolName := toolNameFromMeta(m.Metadata)

	for i := range blocks {
		switch blocks[i].Type {
		case agentcore.ContentText:
			blocks[i].Text = compactText(m.Role, toolName, blocks[i].Text)
		case agentcore.ContentToolCall:
			if blocks[i].ToolCall != nil {
				blocks[i].ToolCall = compactToolCall(blocks[i].ToolCall)
			}
		}
	}
	m.Content = blocks
	return m
}

func toolNameFromMeta(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta["tool_name"].(string); ok {
		return v
	}
	return ""
}

// compactText compresses a tool result's text content.
func compactText(role agentcore.Role, toolName, text string) string {
	if role != agentcore.RoleTool || len(text) < 4096 {
		return text
	}
	switch toolName {
	case "novel_context":
		summary := extractJSONField(text, "_loading_summary")
		return fmt.Sprintf("[session_compact: novel_context %dB | %s]", len(text), summary)
	case "read_chapter":
		chars := utf8.RuneCountInString(text)
		return fmt.Sprintf("[session_compact: read_chapter %d chữ | xem chapters/]", chars)
	default:
		if len(text) > 8192 {
			chars := utf8.RuneCountInString(text)
			return fmt.Sprintf("[session_compact: %s %d chữ]", toolName, chars)
		}
		return text
	}
}

// compactToolCall compresses the bulk content fields inside a tool call's args.
func compactToolCall(tc *agentcore.ToolCall) *agentcore.ToolCall {
	switch tc.Name {
	case "draft_chapter":
		return compactArgsContent(tc, "chính văn chương N", "drafts/")
	case "save_foundation":
		return compactFoundationArgs(tc)
	default:
		return tc
	}
}

func compactArgsContent(tc *agentcore.ToolCall, label, ref string) *agentcore.ToolCall {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return tc
	}
	contentRaw, ok := args["content"]
	if !ok || len(contentRaw) < 4096 {
		return tc
	}
	var content string
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		// content is not a string (it may be a JSON object), so use the byte count
		placeholder := fmt.Sprintf("[session_compact: %s %dB | xem %s]", label, len(contentRaw), ref)
		args["content"], _ = json.Marshal(placeholder)
	} else {
		chars := utf8.RuneCountInString(content)
		ch := extractJSONFieldInt(tc.Args, "chapter")
		if ch > 0 {
			label = fmt.Sprintf("chính văn chương %d", ch)
			ref = fmt.Sprintf("drafts/%02d.draft.md", ch)
		}
		placeholder := fmt.Sprintf("[session_compact: %s %d chữ | xem %s]", label, chars, ref)
		args["content"], _ = json.Marshal(placeholder)
	}
	clone := *tc
	clone.Args, _ = json.Marshal(args)
	return &clone
}

func compactFoundationArgs(tc *agentcore.ToolCall) *agentcore.ToolCall {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return tc
	}
	contentRaw, ok := args["content"]
	if !ok || len(contentRaw) < 4096 {
		return tc
	}
	typeName := "foundation"
	var t string
	if json.Unmarshal(args["type"], &t) == nil && t != "" {
		typeName = t
	}
	placeholder := fmt.Sprintf("[session_compact: %s %dB | xem store]", typeName, len(contentRaw))
	args["content"], _ = json.Marshal(placeholder)
	clone := *tc
	clone.Args, _ = json.Marshal(args)
	return &clone
}

// extractJSONField extracts the string value of a named field from a JSON string.
func extractJSONField(jsonStr, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var val string
	if err := json.Unmarshal(raw, &val); err != nil {
		return string(raw)
	}
	return val
}

func extractJSONFieldInt(data json.RawMessage, field string) int {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return 0
	}
	raw, ok := m[field]
	if !ok {
		return 0
	}
	var val int
	if err := json.Unmarshal(raw, &val); err != nil {
		return 0
	}
	return val
}

// CompactTag is the placeholder marker prefix, making search and restoration easy.
const CompactTag = "[session_compact:"

// IsCompacted checks whether text has already been compacted.
func IsCompacted(text string) bool {
	return strings.HasPrefix(text, CompactTag)
}
