package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
)

// CoCreateSession carries cocreation mode's non-UI state.
type CoCreateSession struct {
	history        []host.CoCreateMessage
	draftPrompt    string
	ready          bool
	streamReply    string
	streamThinking string
	suggestions    []string
}

func NewCoCreateSession(initial string) *CoCreateSession {
	return &CoCreateSession{
		history: []host.CoCreateMessage{
			{Role: "user", Content: strings.TrimSpace(initial)},
		},
	}
}

func (s *CoCreateSession) History() []host.CoCreateMessage {
	if s == nil {
		return nil
	}
	return append([]host.CoCreateMessage(nil), s.history...)
}

func (s *CoCreateSession) ApplyReply(reply host.CoCreateReply) {
	if s == nil {
		return
	}
	s.streamReply = ""
	s.streamThinking = ""
	// In history the assistant stores the complete three-part Raw (including [DRAFT]) so the next round's model can see the
	// draft it wrote last round and accumulate updates on it; storing Message alone would keep [DRAFT] entirely out of
	// context, leaving the model to re-derive from the conversation every round and easily losing early detail. On the
	// degraded path Raw == Message, which is equivalent.
	text := strings.TrimSpace(reply.Raw)
	if text == "" {
		text = strings.TrimSpace(reply.Message)
	}
	if text != "" {
		s.history = append(s.history, host.CoCreateMessage{Role: "assistant", Content: text})
	}
	// The draft is overwritten only when Prompt is non-empty: the parse degradation path returns Prompt="", and the previous
	// round's draft must be kept then, or the "current creation brief" the user has accumulated would be wiped by a truncated
	// reply.
	if prompt := strings.TrimSpace(reply.Prompt); prompt != "" {
		s.draftPrompt = prompt
	}
	s.ready = reply.Ready
	// suggestions is overwritten directly (including with an empty value): each round's guidance only matters for the present.
	s.suggestions = append(s.suggestions[:0], reply.Suggestions...)
}

func (s *CoCreateSession) AppendUser(text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// The user has decided what to say next, so suggestions are voided at once — otherwise old suggestions would hang on the
	// input box and mislead while the AI has not yet replied.
	s.suggestions = nil
	s.history = append(s.history, host.CoCreateMessage{Role: "user", Content: text})
}

// ApplyDelta receives streaming accumulation; kind="thinking" writes the reasoning stream and "reply" the reply preview.
// The two accumulate separately so the UI can colour the blocks distinctly, letting the user see the LLM working during the
// thinking stage.
func (s *CoCreateSession) ApplyDelta(kind, text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	switch kind {
	case host.CoCreateProgressThinking:
		s.streamThinking = text
	case host.CoCreateProgressReply:
		s.streamReply = text
	}
}

func (s *CoCreateSession) StreamReply() string {
	if s == nil {
		return ""
	}
	return s.streamReply
}

func (s *CoCreateSession) StreamThinking() string {
	if s == nil {
		return ""
	}
	return s.streamThinking
}

func (s *CoCreateSession) DraftPrompt() string {
	if s == nil {
		return ""
	}
	return s.draftPrompt
}

func (s *CoCreateSession) Suggestions() []string {
	if s == nil {
		return nil
	}
	return s.suggestions
}

func (s *CoCreateSession) Ready() bool {
	if s == nil {
		return false
	}
	return s.ready
}

func (s *CoCreateSession) CanStart() bool {
	return strings.TrimSpace(s.DraftPrompt()) != ""
}

func (s *CoCreateSession) InitialInput() string {
	if s == nil || len(s.history) == 0 {
		return ""
	}
	return strings.TrimSpace(s.history[0].Content)
}

func (s *CoCreateSession) BuildPrompt() (string, error) {
	if s == nil || !s.CanStart() {
		return "", fmt.Errorf("cocreate draft prompt is required")
	}
	return s.DraftPrompt(), nil
}
