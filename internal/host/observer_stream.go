package host

import (
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// handleSubagentDelta splits a subagent's text and tool-call arguments:
// - DeltaText streams out directly as markdown
// - DeltaToolCall extracts and streams a field only for known long-content tools (draft_chapter.content,
//   say); the argument JSON of every other tool is dropped
func (o *observer) handleSubagentDelta(p *agentcore.ProgressPayload) {
	if p.DeltaKind != agentcore.DeltaToolCall {
		o.emitStreamDelta(p.Delta, false)
		return
	}
	if p.Tool == "" {
		return // Tool name not ready yet; try again on the next delta
	}

	// The tool name appears in the stream, so the TOOL in-progress event is emitted early and the
	// spinner covers the whole LLM generation (otherwise "in progress" for a tool like draft_chapter
	// would show only during the tens of milliseconds of the real Execute). When the genuine
	// ProgressToolStart arrives, toolStarts already has a record and only the summary is filled in.
	o.ensureSubagentToolStarted(p.Agent, p.Tool)
	o.updateToolCallSummaryFromDelta(p.Agent, p.Tool, p.Delta)

	cur, ok := o.streamExtractors[p.Agent]
	// Trailing deltas can still arrive after the same tool call's args have closed (the top-level }
	// was hit): some providers (deepseek-v4-flash, measured) split one call's args across several
	// chunks, and the last chunk carries whitespace or repeated characters after the `}`. Treating that
	// as "tool name matches + Done means rebuild" would make a fresh extractor emit another ✻ header
	// and parse the tail tokens as new args. These deltas are a redundant tail and can simply be
	// dropped.
	if ok && cur.tool == p.Tool && cur.ext.Done() {
		return
	}
	// The tool name changed, or nothing was built yet: create a new one.
	if !ok || cur.tool != p.Tool {
		ext := newToolExtractor(p.Tool)
		if ext == nil {
			delete(o.streamExtractors, p.Agent)
			return
		}
		cur = &agentExtractor{tool: p.Tool, ext: ext}
		o.streamExtractors[p.Agent] = cur
	}
	if emitted := cur.ext.Feed(p.Delta); emitted != "" {
		if !cur.emittedAny {
			cur.emittedAny = true
			// streamClear makes the extractor's ✻ header land at the start of a new round, so
			// renderStreamContent's HasPrefix("✻") check takes the renderAgentBlock highlight path;
			// ensureStreamParagraphBreak only inserts a blank line without opening a round, and the ✻ would
			// still be wrapped by the preceding thinking/prose and drawn by renderChapterBlock in the
			// default colour.
			o.streamClear()
			// streamClear defensively emptied streamExtractors, but cur must keep feeding this tool
			// call's remaining deltas, so it has to be re-registered immediately; otherwise the next delta
			// would create a fresh extractor that parses from the middle of the args (only entering
			// psBeforeKey at a nested object's `{`), treats timeline_events.time / foreshadow_updates.id as
			// top-level fields, and shows a repeated ✻ header in the TUI.
			o.streamExtractors[p.Agent] = cur
		}
		o.emitStreamDelta(emitted, false)
	}
}

func (o *observer) emitStreamDelta(delta string, thinking bool) {
	if delta == "" {
		return
	}
	if thinking != o.streamThinking {
		o.emitD(utils.ThinkingSep)
		o.streamThinking = thinking
	}
	o.emitD(delta)
	o.streamHasContent = true
	o.streamLastByte = delta[len(delta)-1]
}

// ensureSubagentToolStarted registers an in-progress TOOL call for the agent as soon as a tool_call
// first appears in the stream, so the event-stream spinner covers the time the LLM spends streaming
// tool_call arguments (usually 99% of the call's total duration). The args are still incomplete, so the
// summary is the bare tool name for now; the genuine ProgressToolStart later fills in the
// parameterised summary.
func (o *observer) ensureSubagentToolStarted(agent, tool string) {
	if agent == "" || tool == "" {
		return
	}
	if _, ok := o.toolStarts[agent]; ok {
		return // A call is already in progress; idempotent
	}
	o.resetStreamArgLabel(agent, tool)
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{
		id:      id,
		start:   time.Now(),
		summary: tool, // Bare tool name for now; ProgressToolStart may update it to tool(chapter N)
		depth:   1,
	}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  tool,
		Level:    "info",
		Depth:    1,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
	})
	o.emitFallbackStreamHeader(tool)
}

func (o *observer) resetStreamArgLabel(agent, tool string) {
	key := streamArgKey(agent, tool)
	delete(o.streamArgPrefixes, key)
	delete(o.streamArgLabels, key)
}

// emitFallbackStreamHeader adds a one-line ✻ title to the stream panel for tools without an extractor.
// Both paths must call it to stay consistent:
//  1. ensureSubagentToolStarted — subagent streaming tool args (DeltaToolCall)
//  2. handleToolUpdate ProgressToolStart — subagent non-streaming tool args
//
// Missing either one makes streamed and non-streamed models behave differently in tool titles.
func (o *observer) emitFallbackStreamHeader(tool string) {
	if _, has := toolDisplays[tool]; has {
		return // An extractor exists; it emits the header itself
	}
	o.streamClear()
	o.emitStreamDelta(streamHeaderFallback(tool)+"\n", false)
}

// streamHeaderFallback generates the streaming header text for tools without an extractor, so the user
// can see what is being called even for lightweight read-only tools.
//
// The "✻ " prefix is the agreed "agent dispatch block" marker — TUI's renderStreamContent renders it
// through the renderAgentBlock path (icon + highlighted label + divider) when it sees that prefix, and
// otherwise falls to the prose-block path in the terminal's default colour, where the header just looks
// like ordinary prose and does not stand out.
func streamHeaderFallback(tool string) string {
	return "✻ " + tool
}

// streamClear tells the TUI to open a new streamRound while resetting the paragraph-separation state.
// Logically the new round is an "empty stream"; otherwise the next first extractor emit would wrongly
// add a leading blank line.
//
// streamThinking must be reset along with it: emitStreamDelta uses streamThinking across calls to
// track whether the previous segment was thinking. Nothing has been emitted in the new round yet, so
// the next emit(thinking=false) must not insert a ThinkingSep. Otherwise a fallback header (✻ read
// chapter, say) would be headed by \x02 first, renderStreamContent's HasPrefix("✻") would miss, and the
// whole segment would fall to the prose path and be split into a thinking segment by ThinkingSep, with
// the title drawn in the thinking colour.
func (o *observer) streamClear() {
	o.emitC()
	o.streamHasContent = false
	o.streamLastByte = 0
	o.streamThinking = false
	// The previous round's subagent had already been deleted by ProgressToolEnd; this clears defensively.
	if len(o.streamExtractors) > 0 {
		o.streamExtractors = make(map[string]*agentExtractor)
	}
}
