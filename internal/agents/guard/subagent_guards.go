package guard

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// subagentMaxConsecutiveBlocks escalates to termination after N consecutive blocks, so a weak
// model cannot loop forever.
const subagentMaxConsecutiveBlocks = 3

// BlockHook is StopGuard's audit callback, called synchronously on every block/escalation. The
// Host uses it to surface the blocking fact into the TUI event stream and off-screen
// notifications — otherwise the block only reaches the log and the user sees merely "a stall
// plus faster tokens", with no way to tell self-healing from spinning (issue #75).
// The callback takes no part in guard decisions. reason values:
//   - "blocked"    a nudge message was injected and the model will carry on.
//   - "escalated"  consecutive spinning exceeded the limit; this run ends and returns to the
//     caller.
//   - "hard_stop"  the provider refused (safety/content_filter); terminate at once.
type BlockHook func(agent, reason string, consecutive int32)

// hardStopReasons are provider-side refusals that a nudge message cannot recover from.
// Injecting "you must commit" has no effect on them, only costing a full LLM call in tokens
// each time, and once it escalates the Engine re-runs the whole Worker task, multiplying the
// waste (measured on ch02 hitting safety: one chapter write produced 3 re-dispatches and 17 LLM
// calls, with the hit rate falling from 50% to 2.8%).
//
// Note that StopReasonError / StopReasonAborted need not be listed: when agentcore receives
// those two stop reasons in loop.go it terminates the run outright and never calls the
// StopGuard. Only provider refusal semantics that genuinely reach the StopGuard are listed
// here.
var hardStopReasons = map[agentcore.StopReason]struct{}{
	"safety":         {},
	"content_filter": {},
}

// newCheckpointDeltaGuard builds a StopGuard that refuses end_turn when no checkpoint of the
// named step appears after the baseline.
// The baseline is captured by the caller at factory time, keeping the per-run semantics
// correct.
//
// blockMsg receives the set of checkpoint steps observed after the baseline and assembles the
// nudge message from real progress — a static message misleads when the required tool itself
// keeps failing (it would push the model to call a tool that is already erroring, see #75).
//
// The counting semantics reset on progress: any new checkpoint appearing between two blocks
// (a fresh draft / check and so on) counts as the model advancing and zeroes the consecutive
// counter. Only spinning with no output at all accumulates and escalates to termination.
func newCheckpointDeltaGuard(st *store.Store, agentName string, requiredSteps []string, blockMsg func(seen map[string]struct{}) string, onBlock BlockHook) agentcore.StopGuard {
	var baseline int64
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		baseline = cp.Seq
	}
	need := make(map[string]struct{}, len(requiredSteps))
	for _, s := range requiredSteps {
		need[s] = struct{}{}
	}
	var consecutive atomic.Int32
	var lastBlockSeq atomic.Int64 // Latest checkpoint Seq seen at the last block; -1 means never blocked yet
	lastBlockSeq.Store(-1)
	return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
		// Unrecoverable error: escalate straight away without wasting a nudge.
		if _, hard := hardStopReasons[info.Message.StopReason]; hard {
			slog.Error("subagent stop_guard phát hiện dừng không thể khôi phục, nâng mức ngay",
				"module", "agent.guard", "agent", agentName,
				"turn", info.TurnIndex, "stop_reason", info.Message.StopReason)
			if onBlock != nil {
				onBlock(agentName, "hard_stop", consecutive.Load())
			}
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		// Scan checkpoints after the baseline in reverse, collecting the steps already seen
		// (shared by the pass decision and the progress message). New checkpoints are at the
		// tail, so break as soon as one is <= baseline.
		all := st.Checkpoints.All()
		latestSeq := baseline
		seen := make(map[string]struct{})
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.Seq <= baseline {
				break
			}
			if cp.Seq > latestSeq {
				latestSeq = cp.Seq
			}
			seen[cp.Step] = struct{}{}
		}
		for s := range need {
			if _, ok := seen[s]; ok {
				consecutive.Store(0)
				return agentcore.StopDecision{Allow: true}
			}
		}
		// A new artefact on disk since the last block means the model is advancing (e.g. after a
		// nudge it drafts again and probes for the finish), so reset the counter. Escalation
		// should punish only spinning with no progress, not throw away a whole run's worth of
		// accumulated blocks.
		if prev := lastBlockSeq.Load(); prev >= 0 && latestSeq > prev {
			consecutive.Store(0)
		}
		lastBlockSeq.Store(latestSeq)
		n := consecutive.Add(1)
		if n > subagentMaxConsecutiveBlocks {
			slog.Error("subagent stop_guard chặn liên tiếp vượt giới hạn, nâng mức thành kết thúc",
				"module", "agent.guard", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
			if onBlock != nil {
				onBlock(agentName, "escalated", n)
			}
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		slog.Warn("subagent stop_guard chặn end_turn",
			"module", "agent.guard", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
		if onBlock != nil {
			onBlock(agentName, "blocked", n)
		}
		return agentcore.StopDecision{Allow: false, InjectMessage: blockMsg(seen)}
	}
}

// staticBlockMsg adapts fixed wording to the blockMsg signature (the architect/editor artefacts
// land through a single tool with no multi-step progress, so a static nudge suffices).
func staticBlockMsg(msg string) func(map[string]struct{}) string {
	return func(map[string]struct{}) string { return msg }
}

// NewWriterStopGuard requires the writer to produce at least one successful commit_chapter this
// round. The nudge message is assembled from the steps already on disk: the writer is the only
// subagent with a multi-step tool chain, so a static "you must call commit_chapter" misleads
// when a prerequisite is missing or commit itself errors.
func NewWriterStopGuard(st *store.Store, onBlock BlockHook) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "writer", []string{"commit"}, writerBlockMsg, onBlock)
}

// writerBlockMsg decides from this round's checkpoint steps where the writer is stuck.
// Step names map to each tool's persisted value: plan / draft / edit / consistency_check / commit.
func writerBlockMsg(seen map[string]struct{}) string {
	_, hasDraft := seen["draft"]
	_, hasEdit := seen["edit"]
	_, hasCheck := seen["consistency_check"]
	switch {
	case !hasDraft && !hasEdit:
		return "Cấm kết thúc: lượt này chưa ghi bất kỳ chính văn nào xuống đĩa. Hãy hoàn thành chương theo đúng trình tự plan_chapter → draft_chapter → check_consistency → commit_chapter; chính văn chỉ xuất ra trong khung chat thì coi như mất, bắt buộc phải ghi xuống đĩa qua công cụ và commit."
	case !hasCheck:
		return "Cấm kết thúc: chính văn đã ghi xuống đĩa nhưng chưa hoàn tất. Hãy gọi check_consistency để đối chiếu tính nhất quán trước, rồi gọi commit_chapter để commit chương này. draft_chapter / edit_chapter chỉ là lưu bản nháp, không tính là hoàn thành."
	default:
		return "Cấm kết thúc: chương này chỉ còn thiếu bước commit_chapter. Hãy gọi commit_chapter ngay; nếu nó trả về lỗi, trước tiên xử lý theo thông báo lỗi (đối chiếu số chương, bổ sung các bước tiên quyết theo hướng dẫn) rồi thử commit lại, đừng kết thúc khi chưa commit."
	}
}

// NewArchitectStopGuard requires the architect to persist at least one planning artefact this round.
func NewArchitectStopGuard(st *store.Store, onBlock BlockHook) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "architect",
		[]string{
			"book", "premise", "outline", "layered_outline", "characters", "world_rules",
			"foundation_audit", "expand_arc", "append_volume", "update_compass", "complete_book", "revise_outline", "resolve_outline_feedback", "plan",
		},
		staticBlockMsg("Bạn phải gọi một trong save_book, save_foundation, revise_outline, resolve_outline_feedback, audit_foundation hoặc plan_chapter (để viết chỉ thị viết lại cho chương trong hàng đợi) ghi sản phẩm xuống đĩa rồi mới được kết thúc. Chỉ xuất văn bản Markdown/JSON thì coi như mất."),
		onBlock,
	)
}

// NewEditorStopGuard requires the editor to persist an artefact matching the *task* before it may finish.
//
// Task-aware: when dispatched to produce a summary, a save_review (re-check) alone does not
// count — the matching summary must be produced.
// Otherwise an editor "dispatched for an arc summary but reviewing first" satisfies the old
// lenient criterion and exits early, so the arc summary never lands (together with a silent
// dispatcher dedupe this caused a livelock on skeleton arcs mid-volume; see
// outline-exhaustion-livelock).
// A terminal-tool exit also consults the StopGuard (contract test
// TestContract_TerminalToolExitConsultsStopGuard), so hard-stopping save_review in build.go is
// safe: during a summary task this guard vetoes the exit when the editor reviews first and
// nudges until the matching summary lands.
func NewEditorStopGuard(st *store.Store, task string, onBlock BlockHook) agentcore.StopGuard {
	switch {
	case strings.Contains(task, "save_volume_summary") || strings.Contains(task, "tóm tắt tập"):
		return newCheckpointDeltaGuard(st, "editor", []string{"volume_summary"},
			staticBlockMsg("Nhiệm vụ lần này là tạo tóm tắt tập: bạn phải gọi save_volume_summary ghi xuống đĩa rồi mới được kết thúc, save_review (phúc tra) không tính là hoàn thành."), onBlock)
	case strings.Contains(task, "save_arc_summary") || strings.Contains(task, "tóm tắt cung"):
		return newCheckpointDeltaGuard(st, "editor", []string{"arc_summary"},
			staticBlockMsg("Nhiệm vụ lần này là tạo tóm tắt cung: bạn phải gọi save_arc_summary ghi xuống đĩa rồi mới được kết thúc, save_review (phúc tra) không tính là hoàn thành."), onBlock)
	default:
		// Review or ad-hoc task: any review/summary landing is enough (keeping the existing lenient behaviour).
		return newCheckpointDeltaGuard(st, "editor",
			[]string{"review", "arc_summary", "volume_summary"},
			staticBlockMsg("Bạn phải gọi một trong save_review / save_arc_summary / save_volume_summary để ghi kết quả xuống đĩa rồi mới được kết thúc."), onBlock)
	}
}
