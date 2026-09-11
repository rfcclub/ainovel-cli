package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"

	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/notify"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// engine is the deterministic execution engine: read facts → Route → precheck → run the Worker
// directly → check the advance → loop; semantic scenarios consult the Arbiter on demand. It
// executes decisions and takes no part in literary judgement (docs/engine-rfc.md). A single
// goroutine runs serially, and control state changes only at loop boundaries.
type engine struct {
	store   *storepkg.Store
	workers *subagent.Runner

	arbiterModel    agentcore.ChatModel
	failurePrompt   string
	planStartPrompt string // Plan-start system prompt: when no decision exists the engine re-decides from StartPrompt
	style           string // Style name, passed to DecidePlanStart when re-deciding
	// reconsult sends a stale intervention back down the host's full adjudication path
	// (persistence / audit / complete action application) and runs asynchronously — the engine only
	// discards the stale dispatch and never performs a partial re-adjudication itself.
	reconsult func(text string)

	observer  *observer
	budget    *BudgetSentinel
	gate      *ChapterAdvanceGate
	refresh   func() // Refresh the RestorePack before each writer dispatch
	emitEvent func(Event)
	notify    func(kind, level, title, body string)
	onPause   func(summary string) // Engine self-pause (deadlock/failure decision abort): goes through the host unified pause semantics (lifecycle=paused)
	onDone    func()               // Run ended (for any reason); the host derives the final state from store facts

	mu      sync.Mutex
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	running bool
	pending []controlOp       // Control-state actions from interventions, committed at boundaries
	next    *flow.Instruction // Instruction to run first in the next round (plan_start / arbiter dispatch)
	// deferGateForNext lives and dies with next alone: hold+dispatch must first run the paired
	// editor/writer so it builds the rework queue, after which the Gate can judge rewrites_drained.
	deferGateForNext bool

	// Deadlock tracking: the count rises when Route still produces the same instruction key after
	// the previous round executed.
	// A Router instruction projects the task's postcondition, so genuine completion changes the next
	// instruction.
	lastKey string
	repeats int
	// Failure retry: the same instruction key is retried once only, and a second failure consults the Arbiter.
	failedKey string
	// Keeps the latest Worker error for the same instruction so deadlock adjudication sees the real failure cause.
	lastWorkerErrorKey string
	lastWorkerError    error
}

// deadlockConsultAt / deadlockAbortAt: reaching the former consults the Arbiter, reaching the latter
// trips a hard circuit breaker.
// A deterministic Engine must give an explicit upper bound to a no-progress loop (RFC §5).
const (
	deadlockConsultAt = 3
	deadlockAbortAt   = 5
)

// controlOp is an intervention adjudication action that modifies control state (boundary commit; RFC §3).
// text/facts keep the original consultation context so dispatch can re-query with new facts when reconciliation fails.
type controlOp struct {
	hold     *arbiter.AdvanceHoldOp
	reopen   *arbiter.ReopenOp
	dispatch *arbiter.DispatchOp
	text     string
	facts    arbiter.InterventionFacts
}

// start launches the engine loop; it is a no-op (returning false) when already running.
func (e *engine) start(initial *flow.Instruction) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = agentcore.WithToolProgress(ctx, e.observer.workerProgress)
	e.cancel = cancel
	e.running = true
	// An empty initial does not overwrite e.next — a during-downtime intervention may already have
	// queued an adjudicated dispatch through applyControlOp (an editor rework, say), and start(nil)
	// wiping it would let Route dispatch the writer to continue, contrary to the user's intent.
	if initial != nil {
		e.next = initial
		e.deferGateForNext = false
	}
	e.lastKey, e.repeats, e.failedKey = "", 0, ""
	e.lastWorkerErrorKey, e.lastWorkerError = "", nil
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.run(ctx)
	}()
	return true
}

// abort cancels the current loop (pause semantics; checkpoints guarantee no loss).
func (e *engine) abort() {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wait blocks until the current Engine goroutine has fully exited. Host.Close cancels first and then
// calls it, so the event channels and the process only close once write tools and runEnded have
// finished.
func (e *engine) wait() {
	e.wg.Wait()
}

func (e *engine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// enqueue puts an intervention's control action on the boundary queue (while the engine runs); a
// false result means it is not running and the caller should execute immediately itself.
func (e *engine) enqueue(op controlOp) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return false
	}
	e.pending = append(e.pending, op)
	return true
}

func (e *engine) run(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		leftover := e.pending
		e.pending = nil
		e.mu.Unlock()
		// Exit race: an intervention action left over when enqueue and exit interleave must not be
		// silently dropped — hold/reopen are idempotent fact writes and are executed with an
		// independent ctx, while dispatch has no engine to dispatch to, so PendingSteer persistence is
		// restored (the host may already have cleared it as "queued successfully") and the next
		// Resume/Continue replays the whole intervention.
		for _, op := range leftover {
			if op.dispatch != nil {
				if op.text != "" {
					if err := e.store.RunMeta.SetPendingSteer(op.text); err != nil {
						slog.Warn("lưu lại can thiệp còn sót thất bại", "module", "engine", "err", err)
					}
				}
				e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "Engine đã dừng, việc giao việc theo phán định chưa thực hiện; can thiệp đã được giữ lại, khi viết tiếp sẽ tự phán định lại"})
				op.dispatch = nil
			}
			if op.hold != nil || op.reopen != nil {
				if err := e.applyControlOp(context.Background(), op); err != nil {
					e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
						Summary: "Bổ sung can thiệp khi engine thoát thất bại: " + err.Error()})
				}
			}
		}
		e.onDone()
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		// hold+dispatch must first let the paired dispatch establish rework facts; everything else
		// checks the Gate uniformly before dispatching, so a boundary hold and an unlicensed review
		// cannot run one extra Worker.
		deferGate := e.applyPendingOps(ctx) || e.nextDefersGate()
		if !deferGate {
			if e.gate.HandleBoundary() {
				return
			}
		}

		inst := e.takeNext()
		if inst == nil {
			state, err := flow.LoadState(e.store)
			if err != nil {
				e.pauseWithNotify(notify.KindWorkerFailure, "Đọc sự thật định tuyến thất bại, đã tạm dừng: "+err.Error())
				return
			}
			// The volume summary may be on disk before the process managed to MarkComplete. When every
			// aggregate artifact is present, the completion verdict is settled from facts first and only
			// then handed to the Router, so the closing volume is not misdispatched into a new one.
			if state.AggregateRefresh == nil && state.Progress != nil && state.Progress.Layered &&
				state.Progress.Phase == domain.PhaseWriting && state.ArcBoundary != nil &&
				state.ArcBoundary.IsVolumeEnd && state.HasArcReview && state.HasArcSummary && state.HasVolumeSummary {
				complete, reconcileErr := tools.ReconcileLayeredCompletion(e.store)
				if reconcileErr != nil {
					e.pauseWithNotify(notify.KindWorkerFailure, "Khôi phục trạng thái kết thúc thất bại, đã tạm dừng: "+reconcileErr.Error())
					return
				}
				if complete {
					continue
				}
			}
			inst = flow.Route(state)
		}
		if inst == nil {
			var err error
			inst, err = e.planStartFallback(ctx)
			if err != nil {
				e.pauseWithNotify(notify.KindPlanStart, "Đọc sự thật khôi phục quy hoạch thất bại, đã tạm dừng: "+err.Error())
				return
			}
		}
		if inst == nil {
			// Semantic scenario or terminal state: book complete → deterministic wrap-up; anything else
			// (leftover Steering and the like) → natural stop, waiting for the user's Continue / an
			// intervention.
			return
		}
		replaced, err := e.precheck(inst)
		if err != nil {
			e.pauseWithNotify(notify.KindWorkerFailure, "Kiểm tra tiên quyết trước khi giao việc thất bại, đã tạm dừng: "+err.Error())
			return
		}
		if replaced != nil {
			inst = replaced
		}
		allowed, gateErr := e.gate.Allow(inst)
		if gateErr != nil {
			e.pauseWithNotify(notify.KindAdvanceGate, "Lỗi điều khiển đẩy chương, đã tạm dừng: "+gateErr.Error())
			return
		}
		if !allowed {
			return
		}
		if stop := e.trackDeadlock(ctx, &inst); stop {
			return
		}
		if inst == nil {
			continue // The deadlock decision requires recomputing the route.
		}

		err = e.runWorker(ctx, inst)
		if ctx.Err() != nil {
			return
		}
		e.rememberWorkerError(inst, err)
		if err != nil {
			// trackDeadlock records this attempt before the dispatch. An error that never entered real
			// Worker semantic execution must not count as "no progress on the same task".
			e.discardNonSemanticDeadlockAttempt(inst, err)
			if stop := e.handleWorkerError(ctx, inst, err); stop {
				return
			}
		}

		// Policy boundary: budget stops take precedence over review/advance pauses.
		if e.budget.HandleBoundary() {
			return
		}
		if e.gate.HandleBoundary() {
			return
		}
	}
}

func (e *engine) takeNext() *flow.Instruction {
	e.mu.Lock()
	defer e.mu.Unlock()
	inst := e.next
	e.next = nil
	e.deferGateForNext = false
	return inst
}

func (e *engine) nextDefersGate() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.next != nil && e.deferGateForNext
}

// planStartFallback covers the two windows where planning facts are absent and Route cannot derive
// a planner:
//  1. The adjudication is on disk but the first save_foundation has not happened → continue from the
//     frozen PlanStartRecord without re-adjudicating (RFC §6); once the first foundation lands the
//     tier is in place and the catch-up branch takes over.
//  2. The adjudication never completed (a model fault at startup) while the input fact StartPrompt
//     exists → adjudicate on the spot. This retries the first adjudication and does not violate
//     "recovery must not depend on re-adjudication" — that discipline governs adjudications that
//     already exist.
//     A failed catch-up adjudication pauses explicitly: a failed start is not allowed to stop
//     silently.
func (e *engine) planStartFallback(ctx context.Context) (*flow.Instruction, error) {
	progress, err := e.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return nil, nil
	}
	if progress.Phase == domain.PhaseWriting || progress.Phase == domain.PhaseComplete {
		return nil, nil
	}
	meta, err := e.store.RunMeta.Load()
	if err != nil {
		return nil, fmt.Errorf("load run meta: %w", err)
	}
	if meta == nil || meta.PlanningTier != "" {
		return nil, nil
	}
	missing, err := e.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w", err)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if meta.PlanStart != nil {
		return &flow.Instruction{
			Agent:  meta.PlanStart.Planner,
			Task:   meta.PlanStart.PlannerTask,
			Reason: "Bắt đầu quy hoạch theo phán định khởi động đã được cố định",
		}, nil
	}
	if meta.StartPrompt == "" {
		return nil, nil
	}
	return e.retryPlanStart(ctx, meta.StartPrompt), nil
}

// retryPlanStart re-adjudicates the start decision and freezes it (the adjudication lands as a fact before execution, isomorphic to StartPrepared).
func (e *engine) retryPlanStart(ctx context.Context, prompt string) *flow.Instruction {
	start := time.Now()
	decision, derr := runObservedDecision(e.observer, "bổ sung phán định khởi động", func() (arbiter.PlanStartDecision, error) {
		return arbiter.DecidePlanStart(ctx, e.arbiterModel, e.planStartPrompt, prompt, e.style)
	})
	rec := storepkg.DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: prompt,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	rec, recErr := e.store.Decisions.Append(rec)
	if recErr != nil {
		slog.Warn("ghi audit bổ cứu khởi động xuống đĩa thất bại", "module", "engine", "err", recErr)
	}
	if derr != nil {
		e.pauseWithNotify(notify.KindPlanStart, "phán định khởi động thất bại, đã tạm dừng (kiểm tra cấu hình model/mạng rồi tiếp tục): "+derr.Error())
		return nil
	}
	if err := e.store.RunMeta.SetPlanStart(domain.PlanStartRecord{
		RawPrompt: prompt, Planner: decision.Planner, PlannerTask: decision.Task, DecisionID: rec.ID,
	}); err != nil {
		e.pauseWithNotify(notify.KindPlanStart, "không ghi được phán định khởi động xuống đĩa, đã tạm dừng: "+err.Error())
		return nil
	}
	e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
		Summary: fmt.Sprintf("phán định khởi động đã bổ sung (nhà quy hoạch: %s — %s)", decision.Planner, decision.Reason)})
	return &flow.Instruction{Agent: decision.Planner, Task: decision.Task, Reason: decision.Reason}
}

// precheck is the deterministic incarnation of the old ToolGate: an invalid dispatch is rewritten directly, with no teaching copy needed.
func (e *engine) precheck(inst *flow.Instruction) (*flow.Instruction, error) {
	progress, err := e.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w", err)
	}
	if progress != nil && progress.Phase == domain.PhaseComplete {
		// The only legitimate exit from the completed period is reopen (an intervention action), so any dispatch is dropped outright.
		slog.Warn("đã bỏ dispatch trong giai đoạn hoàn tất", "module", "engine", "agent", inst.Agent)
		return &flow.Instruction{}, nil // Empty: the next Route yields nil and it stops naturally
	}
	if inst.Agent == "writer" {
		if progress == nil || progress.Phase != domain.PhaseWriting {
			phase := "<nil>"
			if progress != nil {
				phase = string(progress.Phase)
			}
			return nil, fmt.Errorf("writer chỉ được dispatch ở giai đoạn writing (phase hiện tại=%s): %w", phase, errInvalidWriteTarget)
		}
		ch, err := writerTargetChapter(e.store)
		if err != nil {
			return nil, err
		}
		if ch > 0 {
			if err := tools.EnsureChapterExpanded(e.store, ch); err != nil {
				if !errors.Is(err, errs.ErrToolPrecondition) {
					return nil, err
				}
				// Target chapter not expanded yet → deterministically re-dispatch architect_long to expand it
				// (the old gate had teaching copy aimed at an LLM; the Engine simply does the right thing).
				return &flow.Instruction{
					Agent:  "architect_long",
					Task:   fmt.Sprintf("Cung kế tiếp mới chỉ là khung (%s). Gọi save_foundation(type=expand_arc) để triển khai cung kế; nếu tập hiện tại đã viết xong, đổi sang type=append_volume để thêm và triển khai tập kế.", err),
					Reason: "chương mục tiêu chưa được triển khai, triển khai trước rồi mới viết tiếp",
				}, nil
			}
		}
		e.refresh()
	}
	return nil, nil
}

// writerTargetChapter derives the chapter the writer's next dispatch will actually write (the head of the rework queue, otherwise the next chapter).
func writerTargetChapter(st *storepkg.Store) (int, error) {
	progress, err := st.Progress.Load()
	if err != nil {
		return 0, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return 0, fmt.Errorf("progress chưa được khởi tạo")
	}
	if len(progress.PendingRewrites) > 0 {
		return progress.PendingRewrites[0], nil
	}
	return progress.NextChapter(), nil
}

// trackDeadlock maintains the deadlock counter: the same Agent+Task appearing back to back means the
// previous round did not satisfy the route postcondition. Intermediate checkpoints inside a Worker
// (plan/draft/edit and the like) serve recovery and observation only and cannot reset an Engine-level
// count (issue #84).
// Reaching the consult threshold consults the Arbiter; reaching the hard cap trips the breaker.
// A stop=true result means this round should end the loop; the Arbiter may rewrite inst (reroute) or
// nil it (recompute).
func (e *engine) trackDeadlock(ctx context.Context, inst **flow.Instruction) (stop bool) {
	in := *inst
	if in == nil || in.Agent == "" {
		*inst = nil
		return false
	}
	key := instructionKey(in)
	if key == e.lastKey {
		e.repeats++
	} else {
		e.lastKey, e.repeats = key, 1
	}
	if e.repeats < deadlockConsultAt {
		return false
	}
	if e.repeats >= deadlockAbortAt {
		e.pauseStuck(notify.KindDeadlock, in, fmt.Sprintf("ngắt mạch bế tắc: chỉ thị lặp %d lần không tiến triển (%s), đã tạm dừng chờ can thiệp thủ công", e.repeats, in.Agent))
		return true
	}
	// Arbiter deadlock consultation (repeats ∈ [consultAt, abortAt)). A retry verdict does not zero the count.
	facts := e.failureFacts("deadlock", in, e.workerErrorFor(in))
	decision, err := runObservedDecision(e.observer, "phán định bế tắc", func() (arbiter.FailureDecision, error) {
		return arbiter.DecideFailure(ctx, e.arbiterModel, e.failurePrompt, facts)
	})
	e.recordFailureDecision("deadlock", in, facts, decision, err)
	if err != nil {
		e.pauseWithNotify(notify.KindDeadlock, "phán định bế tắc thất bại, đã tạm dừng chờ can thiệp thủ công: "+err.Error())
		return true
	}
	switch decision.Action {
	case "retry":
		return false
	case "reroute":
		*inst = &flow.Instruction{Agent: decision.Dispatch.Agent, Task: decision.Dispatch.Task, Reason: decision.Reason}
		return false
	default: // abort
		e.pauseStuck(notify.KindDeadlock, in, "phán định bế tắc: "+decision.Reason)
		return true
	}
}

// runWorker runs one subagent directly: DISPATCH event + progress relay + result parsing.
func (e *engine) runWorker(ctx context.Context, inst *flow.Instruction) error {
	e.observer.dispatchStart(inst.Agent, inst.Task, inst.Reason)
	// The Writer task is pre-marked in progress (as in the old Dispatcher: the UI outline immediately shows "▸ in progress").
	if inst.Agent == "writer" && inst.Chapter > 0 {
		if err := e.store.Progress.ValidateChapterWork(inst.Chapter); err != nil {
			runErr := fmt.Errorf("%w: %w", errInvalidWriteTarget, err)
			e.observer.dispatchFinish(inst.Agent, runErr)
			return runErr
		}
		if err := e.store.Progress.StartChapter(inst.Chapter); err != nil {
			runErr := fmt.Errorf("%w: đánh dấu trước chương %d là đang chạy thất bại: %w", errInvalidWriteTarget, inst.Chapter, err)
			e.observer.dispatchFinish(inst.Agent, runErr)
			return runErr
		}
	}

	// Worker progress is relayed to the observer through ctx ToolProgress.
	runCtx := agentcore.WithToolProgress(ctx, func(p agentcore.ProgressPayload) {
		e.observer.workerProgress(p)
	})
	_, err := e.workers.Run(runCtx, inst.Agent, inst.Task)
	if err == nil {
		// Success clears the failure tracking: a later failure on the same key regains its "retry once first" allowance.
		e.failedKey = ""
	}
	e.observer.dispatchFinish(inst.Agent, err)
	return err
}

// handleWorkerError retries the same instruction once before handing the error type and current facts
// to the Arbiter.
// The Engine does not hardcode which execution errors are "necessarily unrecoverable"; semantic
// re-dispatch is the model's call, while the Store boundary keeps blocking illegitimate writes.
func (e *engine) handleWorkerError(ctx context.Context, inst *flow.Instruction, werr error) (stop bool) {
	msg := werr.Error()

	key := instructionKey(inst)
	if e.failedKey != key {
		// First failure: retry the original instruction once (the next Route recomputes, and fact-driven behaviour is naturally idempotent).
		e.failedKey = key
		return false
	}
	e.failedKey = ""
	facts := e.failureFacts("worker_failure", inst, werr)
	decision, err := runObservedDecision(e.observer, "phán định thất bại", func() (arbiter.FailureDecision, error) {
		return arbiter.DecideFailure(ctx, e.arbiterModel, e.failurePrompt, facts)
	})
	e.recordFailureDecision("worker_failure", inst, facts, decision, err)
	if err != nil {
		e.pauseWithNotify(notify.KindWorkerFailure, "phán định thất bại không khả dụng, đã tạm dừng chờ can thiệp thủ công: "+msg+contentFilterAdvice(werr))
		return true
	}
	switch decision.Action {
	case "retry":
		return false
	case "reroute":
		e.mu.Lock()
		e.next = &flow.Instruction{Agent: decision.Dispatch.Agent, Task: decision.Dispatch.Task, Reason: decision.Reason}
		e.deferGateForNext = false
		e.mu.Unlock()
		return false
	default: // abort
		e.pauseStuck(notify.KindWorkerFailure, inst, "phán định thất bại: "+decision.Reason+contentFilterAdvice(werr))
		return true
	}
}

// pauseStuck pauses when the engine abandons an instruction: the rework chapter leaves the queue
// before the stop. It applies only when the engine has already judged the instruction a dead end
// (deadlock breaker, deadlock/failure verdict abort); infrastructure failures such as an unavailable
// verdict still go through pauseWithNotify — those are external problems that must not cost a
// chapter's rework.
func (e *engine) pauseStuck(kind string, inst *flow.Instruction, body string) {
	if e.dropStuckRewrite(inst) {
		body += fmt.Sprintf("; chương %d đã bị đưa ra khỏi hàng đợi làm lại (giữ bản cuối trước đó), việc viết tiếp sẽ đi từ các chương sau", inst.Chapter)
	}
	e.pauseWithNotify(kind, body)
}

// dropStuckRewrite removes a stuck rework chapter from the queue. PendingRewrites is a persisted
// fact, so leaving it queued when the engine abandons the instruction would make a restart replay the
// same dead instruction at once and lock the whole book permanently (issue #110).
// A true result means it really was dequeued.
func (e *engine) dropStuckRewrite(inst *flow.Instruction) bool {
	if inst == nil || inst.Agent != "writer" || inst.Chapter <= 0 {
		return false
	}
	progress, err := e.store.Progress.Load()
	if err != nil || progress == nil || !slices.Contains(progress.PendingRewrites, inst.Chapter) {
		return false
	}
	if err := e.store.Progress.CompleteRewrite(inst.Chapter); err != nil {
		slog.Warn("đưa chương làm lại bị kẹt ra khỏi hàng đợi thất bại", "module", "engine", "chapter", inst.Chapter, "err", err)
		return false
	}
	return true
}

// discardNonSemanticDeadlockAttempt undoes the semantic attempt trackDeadlock pre-recorded for this
// dispatch. It excludes only the stable error types where the model call never ran in full;
// content_filter stays on its original self-healing path, while genuinely unprogressed cases such as
// max_turns, stop_guard and cancellation still count.
func (e *engine) discardNonSemanticDeadlockAttempt(inst *flow.Instruction, werr error) {
	if inst == nil || !isNonSemanticWorkerFailure(werr) {
		return
	}
	key := instructionKey(inst)
	if e.lastKey != key || e.repeats <= 0 {
		return
	}
	e.repeats--
	if e.repeats == 0 {
		e.lastKey = ""
	}
}

// isNonSemanticWorkerFailure recognises only errors where "this model execution produced no judgeable semantics".
// It relies on agentcore's error-chain contract first, and falls back to the log classification when a provider flattens the chain.
func isNonSemanticWorkerFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, agentcore.ErrContextOverflow) || errors.Is(err, agentcore.ErrStreamPartial) {
		return true
	}
	providerErr := agentcore.ClassifyProvider(err)
	classified := errors.Is(providerErr, agentcore.ErrProviderStreamIdle) ||
		errors.Is(providerErr, agentcore.ErrProviderQuota) ||
		errors.Is(providerErr, agentcore.ErrProviderRateLimit) ||
		errors.Is(providerErr, agentcore.ErrProviderTimeout) ||
		errors.Is(providerErr, agentcore.ErrProviderAuth) ||
		errors.Is(providerErr, agentcore.ErrProviderNetwork) ||
		errors.Is(providerErr, agentcore.ErrProviderOverloaded)
	return classified || errorKind(err, err.Error()) == "overloaded"
}

func instructionKey(inst *flow.Instruction) string {
	if inst == nil {
		return ""
	}
	return inst.Agent + "\x00" + inst.Task
}

func (e *engine) rememberWorkerError(inst *flow.Instruction, workerErr error) {
	if workerErr == nil || inst == nil {
		e.lastWorkerErrorKey, e.lastWorkerError = "", nil
		return
	}
	e.lastWorkerErrorKey, e.lastWorkerError = instructionKey(inst), workerErr
}

func (e *engine) workerErrorFor(inst *flow.Instruction) error {
	if e.lastWorkerErrorKey != instructionKey(inst) {
		return nil
	}
	return e.lastWorkerError
}

// contentFilterAdvice attaches a user-actionable way out to a content-moderation pause.
// Moderation is a provider black box where pre-checking and evasion are both impossible, so the only
// real move is to put the decision in the user's hands; the block itself does not trip the breaker
// early because re-dispatching with different context genuinely self-heals it (measured on ch21-24),
// so it goes through the free retry and arbitration before pausing.
func contentFilterAdvice(werr error) string {
	if !errors.Is(werr, agentcore.ErrProviderContentFilter) {
		return ""
	}
	return ". Đây là chặn kiểm duyệt nội dung của nhà cung cấp (không phải lỗi cục bộ). Có thể: dùng /model chuyển sang nhà cung cấp không có lớp kiểm duyệt rồi nhập \"tiếp tục\"; hoặc sửa cách diễn đạt trong bản nháp chương này (drafts/) rồi tiếp tục; thử lại nguyên trạng nhiều khả năng vẫn bị chặn"
}

// errInvalidWriteTarget marks an illegitimate write target stopped by runWorker's precheck, giving the
// error chain and the Arbiter's facts a stable semantic; whether to retry or re-dispatch is still
// decided by the unified failure flow.
var errInvalidWriteTarget = errors.New("mục tiêu viết không hợp lệ")

func (e *engine) failureFacts(kind string, inst *flow.Instruction, workerErr error) arbiter.FailureFacts {
	f := arbiter.FailureFacts{Kind: kind, Agent: inst.Agent, Task: inst.Task, Repeats: e.repeats}
	if workerErr != nil {
		f.Error = workerErr.Error()
		f.ErrorKind = errorKind(workerErr, f.Error)
		if f.ErrorKind == "" {
			f.ErrorKind = "unknown"
		}
	}
	missing, err := e.store.FoundationMissing()
	if err != nil {
		f.FactWarnings = append(f.FactWarnings, "đọc trạng thái thiết lập nền tảng thất bại: "+err.Error())
	} else {
		f.FoundationGap = missing
	}
	p, err := e.store.Progress.Load()
	if err != nil {
		f.FactWarnings = append(f.FactWarnings, "đọc tiến độ sáng tác thất bại: "+err.Error())
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.NextChapter = p.NextChapter()
		f.PendingQueue = p.PendingRewrites
	}
	return f
}

func (e *engine) recordFailureDecision(kind string, inst *flow.Instruction, facts arbiter.FailureFacts, d arbiter.FailureDecision, derr error) {
	rec := storepkg.DecisionRecord{Kind: kind, Decider: "arbiter", Input: inst.Agent + ": " + inst.Task, Reason: d.Reason}
	if data, err := json.Marshal(facts); err == nil {
		rec.Facts = data
	}
	if derr == nil {
		if data, err := json.Marshal(d); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	if _, err := e.store.Decisions.Append(rec); err != nil {
		slog.Warn("ghi audit phán định xuống đĩa thất bại", "module", "engine", "kind", kind, "err", err)
	}
}

// applyPendingOps commits intervention control actions at a loop boundary and drains the queue — a
// synchronous re-consult (reconsult) appends new actions while applying, and these must be consumed
// within this boundary or an extra worker would be dispatched in the meantime (an intervention must
// take effect before later writing).
// It reports whether a hold+dispatch must run the paired dispatch first; in that case the caller holds
// back the Gate check.
func (e *engine) applyPendingOps(ctx context.Context) (deferGate bool) {
	for {
		e.mu.Lock()
		ops := e.pending
		e.pending = nil
		e.mu.Unlock()
		if len(ops) == 0 {
			return deferGate
		}
		for _, op := range ops {
			pairedHoldDispatch := op.hold != nil && !op.hold.Cancel && op.dispatch != nil
			err := e.applyControlOp(ctx, op)
			if err != nil {
				// Action persistence failed: the host already cleared PendingSteer as "queued successfully",
				// so the whole intervention is written back here and recovery/continue re-adjudicates and retries
				// (actions are idempotent and re-queries follow new facts).
				if op.text != "" {
					if serr := e.store.RunMeta.SetPendingSteer(op.text); serr != nil {
						slog.Warn("ghi lại can thiệp thất bại", "module", "engine", "err", serr)
					}
				}
				e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "thực thi hành động can thiệp thất bại, đã giữ lại; sẽ tự động thử lại khi khôi phục/tiếp tục"})
			} else if pairedHoldDispatch && e.nextDefersGate() {
				// Bypassing this Gate is allowed only when both the hold and the paired dispatch landed
				// successfully. Continuing to bypass after a failed hold write or a dispatch dropped for stale
				// facts would advance an unprotected Worker.
				deferGate = true
			}
		}
	}
}

// applyControlOp performs one control action (hold writes RunMeta directly, reopen calls the tool
// kernel, dispatch reconciles first).
// When the engine is not running the host calls it directly on the intervention path; it returns the
// first persistence failure so the caller can decide whether to keep PendingSteer for a recovery
// replay.
func (e *engine) applyControlOp(ctx context.Context, op controlOp) error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	if op.dispatch != nil {
		// Expect must be reconciled before paired actions such as hold are persisted. Otherwise a stale
		// hold would linger after the dispatch expires and conflict with the hold adjudicated from new
		// facts, ending up with a pause and the modification left undone.
		fresh, err := arbiter.CollectInterventionFacts(e.store)
		if err != nil {
			return fmt.Errorf("làm mới sự thật can thiệp: %w", err)
		}
		if fresh.Phase != op.facts.Phase || fresh.Flow != op.facts.Flow ||
			fresh.QueueHead() != op.facts.QueueHead() {
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "dispatch đã phán định bị lỗi thời (sự thật đã tiến triển), phán định lại theo sự thật mới nhất"})
			e.recordStale(op)
			if op.text != "" && e.reconsult != nil {
				// Synchronous re-query: the intervention must take effect before later writing — doing it
				// asynchronously would let the engine dispatch another worker before the new verdict lands. New
				// actions are drained by applyPendingOps at this boundary.
				e.reconsult(op.text)
			}
			return nil
		}
	}
	if op.hold != nil {
		if op.hold.Cancel {
			meta, err := e.store.RunMeta.Load()
			if err != nil {
				e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "đọc tạm dừng một lần thất bại: " + err.Error(), Level: "error"})
				return err
			}
			if meta != nil && meta.AdvanceHold != nil {
				if err := e.store.RunMeta.ClearAdvanceHold(*meta.AdvanceHold); err != nil {
					e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "hủy tạm dừng một lần thất bại: " + err.Error(), Level: "error"})
					return err
				}
			}
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "đã hủy tạm dừng một lần", Level: "info"})
		} else {
			hold := domain.AdvanceHold{After: op.hold.After, TargetChapter: op.hold.TargetChapter, Reason: op.hold.Reason}
			if err := e.store.RunMeta.SetAdvanceHold(hold); err != nil {
				e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "đặt tạm dừng một lần thất bại: " + err.Error(), Level: "error"})
				return err // When the hold is not on disk, the associated dispatch must not execute
			}
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "đã đặt tạm dừng một lần: " + op.hold.Reason, Level: "info"})
		}
	}
	if op.reopen != nil {
		args, _ := json.Marshal(map[string]any{"chapters": op.reopen.Chapters, "reason": op.reopen.Reason})
		if _, err := tools.NewReopenBookTool(e.store).Execute(ctx, args); err != nil {
			e.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "mở lại làm lại thất bại: " + err.Error(), Level: "error"})
			fail(err)
		} else {
			e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
				Summary: fmt.Sprintf("đã mở lại làm lại toàn sách: chương %v vào hàng đợi", op.reopen.Chapters), Level: "info"})
		}
	}
	if op.dispatch != nil {
		// Expect was already reconciled before any paired state write. CheckpointSeq is kept for audit
		// only and takes no part in reconciliation: the worker is usually running when an intervention
		// arrives, so seq necessarily advances.
		e.mu.Lock()
		// Known window (a best-effort boundary, see clarification ③ in engine-arbiter.md): the dispatch
		// lives in memory from here on, so a hard kill before the worker starts (kill -9, where defers do
		// not run) loses this dispatch's intent — normal exit and Abort are caught by run's defer writing
		// PendingSteer back.
		e.next = &flow.Instruction{Agent: op.dispatch.Agent, Task: interventionDispatchTask(op.dispatch.Task, op.text), Reason: "phán định can thiệp của người dùng"}
		e.deferGateForNext = op.hold != nil && !op.hold.Cancel
		e.mu.Unlock()
	}
	return firstErr
}

// interventionDispatchTask preserves the user's original intervention so the Arbiter cannot widen the
// edit target unintentionally while paraphrasing the task. Downstream may read broader context to
// judge, but the original text is the only source of action authorisation.
func interventionDispatchTask(task, original string) string {
	task = strings.TrimSpace(task)
	if strings.TrimSpace(original) == "" {
		return task
	}
	return task + "\n\nCan thiệp nguyên văn của người dùng (nguồn uỷ quyền sửa đổi duy nhất lần này; ngữ cảnh chỉ để hiểu, không được mở rộng mục tiêu hay phạm vi):\n" + original
}

func (e *engine) recordStale(op controlOp) {
	rec := storepkg.DecisionRecord{Kind: "decision_stale", Decider: "engine", Input: op.text}
	if data, err := json.Marshal(op.facts); err == nil {
		rec.Facts = data
	}
	if _, err := e.store.Decisions.Append(rec); err != nil {
		slog.Warn("ghi bản ghi stale thất bại", "module", "engine", "err", err)
	}
}

// pauseWithNotify is an autonomous engine pause (deadlock breaker / failure-verdict abort): an
// off-screen notification plus the host's unified pause semantics (onPause → abortWithEvent:
// lifecycle=paused + on-screen event + cancel ctx).
func (e *engine) pauseWithNotify(kind, body string) {
	e.notify(kind, "warn", "ainovel: engine tạm dừng", body)
	if e.onPause != nil {
		e.onPause(body)
		return
	}
	e.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: body, Level: "warn"})
	e.abort()
}

// completionSummary is the deterministic wrap-up report for a completed book, spending no LLM call.
func completionSummary(progress domain.Progress, book domain.BookMetadata) string {
	var b strings.Builder
	fmt.Fprintf(&b, "《%s》hoàn thành sáng tác: tổng %d chương %d chữ", book.Title, len(progress.CompletedChapters), progress.TotalWordCount)
	return b.String()
}
