package host

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

// handleToolUpdate handles the Worker's progress relay (ProgressPayload): TOOL rows, streaming prose,
// thinking, retry and context. The Engine feeds it through observer.workerProgress.
func (o *observer) handleToolUpdate(ev agentcore.Event) {
	if ev.Progress == nil {
		return
	}
	switch ev.Progress.Kind {
	case agentcore.ProgressToolDelta:
		if ev.Progress.Delta != "" {
			o.handleSubagentDelta(ev.Progress)
		}
	case agentcore.ProgressToolStart:
		// A tool call inside a Worker (writer → draft_chapter, say).
		// Note: the TOOL row may already have been emitted early by handleSubagentDelta during stream
		// recognition. Here: if already emitted, only the summary is updated (the args are complete by now
		// and can render "tool(chapter N)"); otherwise it is emitted normally.
		if ev.Progress.Agent == "" || ev.Progress.Tool == "" {
			break
		}
		toolName := displayToolName(ev.Progress.Tool, ev.Progress.Args)
		if _, ok := o.toolStarts[ev.Progress.Agent]; ok {
			o.updateToolCallSummary(ev.Progress.Agent, ev.Progress.Tool, toolName)
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.state = "working"
				a.tool = ev.Progress.Tool
				a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
			})
			break
		}
		// Not emitted early → the normal path
		// (a model with non-streaming tool args never triggers ensureSubagentToolStarted, so the fallback
		// header must be added on this path, or a tool without an extractor such as read_chapter gets no ✻
		// header on the stream panel and hugs the preceding thinking segment.)
		id := nextEventID()
		o.toolStarts[ev.Progress.Agent] = &activeCall{id: id, start: time.Now(), summary: toolName, depth: 1}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "TOOL",
			Agent:    ev.Progress.Agent,
			Summary:  toolName,
			Level:    "info",
			Depth:    1,
		})
		o.updateAgent(ev.Progress.Agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Progress.Tool
			a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
		})
		o.emitFallbackStreamHeader(ev.Progress.Tool)
	case agentcore.ProgressToolEnd:
		delete(o.streamExtractors, ev.Progress.Agent)
		if ev.Progress.Agent == "" {
			return
		}
		call, ok := o.toolStarts[ev.Progress.Agent]
		if !ok {
			return
		}
		delete(o.toolStarts, ev.Progress.Agent)
		// Same-ID update event: the TUI locates the original TOOL row by ID and fills in FinishedAt / Duration.
		// Summary / Depth come along too, so a runtime queue replay restores the complete row.
		finishEv := Event{
			ID:         call.id,
			Time:       call.start,
			FinishedAt: time.Now(),
			Category:   "TOOL",
			Agent:      ev.Progress.Agent,
			Summary:    call.summary,
			Level:      "info",
			Depth:      call.depth,
			Duration:   time.Since(call.start),
		}
		o.emitEv(finishEv)
		o.persistEvent(finishEv)
	case agentcore.ProgressThinking:
		o.handleThinkingProgress(ev)
	case agentcore.ProgressRetry:
		// Only the actual wait time reported explicitly by the upstream is shown, avoiding a local estimate
		// that disagrees with the real backoff rhythm. The Summary embeds no static delay — the UI counts
		// down per second from RetryAt; Detail/logs keep the delay snapshot from emission time.
		delay := retryProgressDelay(ev.Progress)
		retryEv := Event{
			ID:       o.retryEventID(ev.Progress.Agent, ev.Progress.Attempt),
			Time:     time.Now(),
			Category: "SYSTEM",
			Agent:    ev.Progress.Agent,
			Summary:  retryPrefix(ev.Progress.Attempt, ev.Progress.MaxRetries, 0) + truncate(ev.Progress.Message, 80),
			Detail:   retryPrefix(ev.Progress.Attempt, ev.Progress.MaxRetries, delay) + ev.Progress.Message,
			Kind:     errorKind(nil, ev.Progress.Message),
			Level:    "warn",
			Depth:    1,
		}
		if delay > 0 {
			retryEv.RetryAt = retryEv.Time.Add(delay)
		}
		o.emitEv(retryEv)
		o.persistEvent(retryEv)
	case agentcore.ProgressToolError:
		delete(o.streamExtractors, ev.Progress.Agent)
		msg := ev.Progress.Message
		if msg == "" {
			msg = "unknown error"
		}
		// When a TOOL row is in progress it is marked failed in place with the full error in Detail.
		// One failure produces only one ERROR-level event, so a TOOL failure state plus an attached ERROR
		// detail cannot be misread in tui.log as two separate faults.
		if call, ok := o.toolStarts[ev.Progress.Agent]; ok {
			delete(o.toolStarts, ev.Progress.Agent)
			detail := fmt.Sprintf("%s lỗi: %s", ev.Progress.Tool, msg)
			finishEv := Event{
				ID:         call.id,
				Time:       call.start,
				FinishedAt: time.Now(),
				Failed:     true,
				Category:   "TOOL",
				Agent:      ev.Progress.Agent,
				Summary:    fmt.Sprintf("%s lỗi: %s", call.summary, truncate(msg, 100)),
				Detail:     detail,
				Kind:       errorKind(nil, msg),
				Level:      "error",
				Depth:      call.depth,
				Duration:   time.Since(call.start),
			}
			o.emitEv(finishEv)
			o.persistEvent(finishEv)
			return
		}
		// The rare progress stream missing its start cannot be updated in place, so a standalone ERROR event is kept to expose the fault.
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    ev.Progress.Agent,
			Summary:  fmt.Sprintf("%s lỗi: %s", ev.Progress.Tool, truncate(msg, 100)),
			Detail:   fmt.Sprintf("%s lỗi: %s", ev.Progress.Tool, msg),
			Kind:     errorKind(nil, msg),
			Level:    "error",
			Depth:    1,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
	case agentcore.ProgressContext:
		o.handleContextProgress(ev)
	}
}

func retryProgressDelay(p *agentcore.ProgressPayload) time.Duration {
	if p == nil {
		return 0
	}
	if len(p.Meta) > 0 {
		var meta struct {
			DelayMS int64 `json:"retry_delay_ms"`
		}
		if json.Unmarshal(p.Meta, &meta) == nil && meta.DelayMS > 0 {
			return time.Duration(meta.DelayMS) * time.Millisecond
		}
	}
	return 0
}

func dispatchSummary(agent, task string) string {
	if agent == "" {
		agent = "subagent"
	}
	if task == "" {
		return agent
	}
	firstLine := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
	if firstLine == "" {
		return agent
	}
	return agent + "（" + truncate(firstLine, 30) + "）"
}

func dispatchDetail(task, reason string) string {
	var parts []string
	if strings.TrimSpace(reason) != "" {
		parts = append(parts, "Lý do giao việc: "+reason)
	}
	if strings.TrimSpace(task) != "" {
		parts = append(parts, "Nhiệm vụ đầy đủ:\n"+task)
	}
	return strings.Join(parts, "\n")
}

func (o *observer) updateToolCallSummary(agent, tool, summary string) {
	if agent == "" || summary == "" {
		return
	}
	call, ok := o.toolStarts[agent]
	if !ok || call.summary == summary {
		return
	}
	call.summary = summary
	o.emitEv(Event{
		ID:       call.id,
		Time:     call.start,
		Category: "TOOL",
		Agent:    agent,
		Summary:  summary,
		Level:    "info",
		Depth:    call.depth,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
		a.summary = fmt.Sprintf("%s → %s", agent, summary)
	})
}

func (o *observer) updateToolCallSummaryFromDelta(agent, tool, delta string) {
	key := streamArgKey(agent, tool)
	prefix := o.streamArgPrefixes[key] + delta
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}
	o.streamArgPrefixes[key] = prefix

	summary := streamedToolLabel(tool, prefix)
	if summary == "" {
		return
	}
	if o.streamArgLabels[key] == summary {
		return
	}
	o.streamArgLabels[key] = summary
	o.updateToolCallSummary(agent, tool, summary)
}

func streamArgKey(agent, tool string) string {
	return agent + "\x00" + tool
}

func streamedToolLabel(tool, delta string) string {
	if tool != "save_foundation" || delta == "" {
		return ""
	}
	typ := firstJSONStringField(delta, "type")
	if typ == "" {
		return ""
	}
	return fmt.Sprintf("%s[%s]", tool, typ)
}

func firstJSONStringField(raw, field string) string {
	needle := `"` + field + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(needle):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	var value strings.Builder
	escape := false
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		if escape {
			value.WriteByte(c)
			escape = false
			continue
		}
		switch c {
		case '\\':
			escape = true
		case '"':
			return value.String()
		default:
			value.WriteByte(c)
		}
	}
	return ""
}

func (o *observer) emitCallFinish(call *activeCall, category, agentName string, callErr error) {
	if call == nil {
		return
	}
	failed := callErr != nil
	level := "success"
	if failed {
		level = "error"
	}
	summary := call.summary
	detail := ""
	kind := ""
	if failed {
		detail = callErr.Error()
		kind = errorKind(callErr, detail)
		summary = fmt.Sprintf("%s lỗi: %s", call.summary, truncate(detail, 100))
	}
	finishEv := Event{
		ID:         call.id,
		Time:       call.start,
		FinishedAt: time.Now(),
		Failed:     failed,
		Category:   category,
		Agent:      agentName,
		Summary:    summary,
		Detail:     detail,
		Kind:       kind,
		Level:      level,
		Depth:      call.depth,
		Duration:   time.Since(call.start),
	}
	o.emitEv(finishEv)
	o.persistEvent(finishEv)
}

func displayToolName(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return tool
	}
	switch tool {
	case "save_foundation":
		var p struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(args, &p) == nil && p.Type != "" {
			return fmt.Sprintf("%s[%s]", tool, p.Type)
		}
	case "commit_chapter", "plan_chapter", "draft_chapter", "check_consistency":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(chương %d)", tool, p.Chapter)
		}
	case "save_review":
		var p struct {
			Chapter int    `json:"chapter"`
			Scope   string `json:"scope"`
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(args, &p) == nil {
			label := ""
			switch p.Scope {
			case "arc":
				label = "cung này"
			case "global":
				label = "toàn cục"
			default:
				if p.Chapter > 0 {
					label = fmt.Sprintf("chương %d", p.Chapter)
				}
			}
			if label == "" {
				return tool
			}
			if p.Verdict != "" {
				return fmt.Sprintf("%s(%s·%s)", tool, label, p.Verdict)
			}
			return fmt.Sprintf("%s(%s)", tool, label)
		}
	case "novel_context":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(chương %d)", tool, p.Chapter)
		}
	case "read_chapter":
		var p struct {
			Chapter   int    `json:"chapter"`
			Source    string `json:"source"`
			Character string `json:"character"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			suffix := ""
			if p.Character != "" {
				suffix = "·đối thoại " + p.Character
			} else if p.Source == "draft" {
				suffix = "·bản nháp"
			}
			return fmt.Sprintf("%s(chương %d%s)", tool, p.Chapter, suffix)
		}
	}
	return tool
}
