package guard

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

// TestSubAgentGuard_HardStopReasonEscalatesImmediately verifies that when the model returns an
// unrecoverable provider-side refusal such as safety / content_filter, the subagent StopGuard must
// Escalate immediately instead of injecting a nudge message.
//
// Background: measured on hy3-preview:free writing chapter 2, eight consecutive
// stop_reason='safety' refusals. The old logic kept injecting "you must commit", the model kept
// refusing, and escalation only came after three blocks; the Engine then reran the writer three
// times in total. Each attempt was a fresh SubAgent, so the whole cache prefix went cold. After the
// fix the first safety refusal escalates at once and the Engine can pause on an unrecoverable
// error directly.
//
// Note that only safety / content_filter are tested: StopReasonError / StopReasonAborted take the
// terminate the run outright in agentcore loop.go and never call the StopGuard at all, so listing
// them would only introduce dead code.
func TestSubAgentGuard_HardStopReasonEscalatesImmediately(t *testing.T) {
	cases := []agentcore.StopReason{
		agentcore.StopReason("safety"),
		agentcore.StopReason("content_filter"),
	}
	for _, sr := range cases {
		t.Run(string(sr), func(t *testing.T) {
			s := newTestStore(t)
			guard := NewWriterStopGuard(s, nil)
			info := agentcore.StopInfo{
				TurnIndex: 1,
				Message:   agentcore.Message{StopReason: sr},
			}
			d := guard(context.Background(), info)
			if !d.Escalate {
				t.Fatalf("stop_reason=%q must escalate immediately, got %#v", sr, d)
			}
			if d.InjectMessage != "" {
				t.Fatalf("stop_reason=%q must not inject any message, got %q", sr, d.InjectMessage)
			}
		})
	}
}

// TestSubAgentGuard_NormalStopStillBlocks ensures the interception of an ordinary stop_reason is
// unaffected by the hard-error bypass — a model that stops on its own without committing still gets
// nudged.
func TestSubAgentGuard_NormalStopStillBlocks(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	info := agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	}
	d := guard(context.Background(), info)
	if d.Escalate {
		t.Fatal("normal stop must not escalate on first block")
	}
	if d.Allow {
		t.Fatal("normal stop must be blocked when no commit checkpoint exists")
	}
	if d.InjectMessage == "" {
		t.Fatal("normal stop must inject a follow-up message")
	}
}

// TestSubAgentGuard_ProgressBetweenBlocksResetsCounter verifies that consecutive resets when a new
// checkpoint appeared between two interceptions (the model drafts again after a nudge, say) —
// escalation punishes only idling with no output at all, following the "reset on progress" semantic
// (issue #75).
func TestSubAgentGuard_ProgressBetweenBlocksResetsCounter(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// Intercept → a new draft lands (progress) → intercept again: going back and forth past the threshold must still not escalate.
	for i := 0; i < subagentMaxConsecutiveBlocks+2; i++ {
		if d := guard(context.Background(), normalStop); d.Escalate {
			t.Fatalf("escalated at block %d despite progress between blocks", i)
		}
		if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.draft.md", fmt.Sprintf("d%d", i)); err != nil {
			t.Fatalf("append draft: %v", err)
		}
	}
	// Progress stops: escalation comes only after idling interceptions reach the threshold.
	for i := 0; i < subagentMaxConsecutiveBlocks; i++ {
		if d := guard(context.Background(), normalStop); d.Escalate {
			t.Fatalf("escalated too early at idle block %d", i)
		}
	}
	if d := guard(context.Background(), normalStop); !d.Escalate {
		t.Fatal("expected escalate after consecutive no-progress blocks")
	}
}

// TestWriterStopGuard_StageAwareBlockMessage verifies the nudge message is assembled from the steps
// already on disk: a static "you must call commit_chapter" misleads the model when a prerequisite step
// is missing or the commit errored (issue #75).
func TestWriterStopGuard_StageAwareBlockMessage(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// No output at all: it should guide the full flow rather than nudging straight to commit.
	d := guard(context.Background(), normalStop)
	if !strings.Contains(d.InjectMessage, "draft_chapter") || !strings.Contains(d.InjectMessage, "plan_chapter") {
		t.Fatalf("no-draft message should walk through the protocol, got %q", d.InjectMessage)
	}

	// The draft has landed: it should nudge toward wrapping up with check_consistency.
	if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.draft.md", "d1"); err != nil {
		t.Fatalf("append draft: %v", err)
	}
	d = guard(context.Background(), normalStop)
	if !strings.Contains(d.InjectMessage, "check_consistency") {
		t.Fatalf("draft-only message should point to check_consistency, got %q", d.InjectMessage)
	}

	// Draft plus consistency check done: only the commit is missing, and the message must
	// leave room for a commit that returns an error.
	if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "consistency_check", "meta/checks/01.json", "c1"); err != nil {
		t.Fatalf("append consistency_check: %v", err)
	}
	d = guard(context.Background(), normalStop)
	if !strings.Contains(d.InjectMessage, "commit_chapter") || !strings.Contains(d.InjectMessage, "lỗi") {
		t.Fatalf("ready-to-commit message should mention commit and error handling, got %q", d.InjectMessage)
	}
}

// TestSubAgentGuard_BlockHookReceivesAgentAndReason checks that the audit hook receives the correct
// The agent-name and reason sequence — the Host uses it to surface interceptions into the TUI.
func TestSubAgentGuard_BlockHookReceivesAgentAndReason(t *testing.T) {
	s := newTestStore(t)
	var agents, reasons []string
	guard := NewWriterStopGuard(s, func(agent, reason string, _ int32) {
		agents = append(agents, agent)
		reasons = append(reasons, reason)
	})
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	for i := 0; i < subagentMaxConsecutiveBlocks+1; i++ {
		guard(context.Background(), normalStop)
	}
	if len(reasons) != subagentMaxConsecutiveBlocks+1 {
		t.Fatalf("hook called %d times, want %d", len(reasons), subagentMaxConsecutiveBlocks+1)
	}
	for i, agent := range agents {
		if agent != "writer" {
			t.Fatalf("hook call %d: agent = %q, want writer", i, agent)
		}
	}
	for i := 0; i < subagentMaxConsecutiveBlocks; i++ {
		if reasons[i] != "blocked" {
			t.Fatalf("reason[%d] = %q, want blocked", i, reasons[i])
		}
	}
	if last := reasons[len(reasons)-1]; last != "escalated" {
		t.Fatalf("last reason = %q, want escalated", last)
	}

	// hard_stop must be reported too.
	var hardReasons []string
	hardGuard := NewWriterStopGuard(s, func(_, reason string, _ int32) {
		hardReasons = append(hardReasons, reason)
	})
	hardGuard(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReason("safety")},
	})
	if len(hardReasons) != 1 || hardReasons[0] != "hard_stop" {
		t.Fatalf("hard stop hook reasons = %v, want [hard_stop]", hardReasons)
	}
}

// TestEditorStopGuard_TaskAware verifies task awareness: when dispatched to produce an arc summary, a
// mere save_review (a re-check) does not count as done and arc_summary must be produced before it is
// let through — closing off Defect C, the entry point of the mid-volume skeleton-arc livelock.
func TestEditorStopGuard_TaskAware(t *testing.T) {
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// Summary task + only a review saved → must block (a review does not satisfy the arc_summary requirement).
	t.Run("summary task blocks on review only", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); d.Allow {
			t.Fatal("summary task must NOT be satisfied by a review checkpoint")
		}
	})

	// Summary task + arc_summary already saved → let through.
	t.Run("summary task allows on arc_summary", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "arc_summary", "summaries/arc-v05a01.json", "d1"); err != nil {
			t.Fatalf("append arc_summary: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("summary task must be satisfied by an arc_summary checkpoint")
		}
	})

	// Review task + review saved → let through (the default lenient behaviour is unchanged).
	t.Run("review task allows on review", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "对第 5 卷第 1 弧做弧级评审（scope=arc）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("review task must be satisfied by a review checkpoint")
		}
	})
}
