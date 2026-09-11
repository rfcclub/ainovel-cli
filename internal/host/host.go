package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	runtimelog "github.com/voocel/ainovel-cli/internal/logger"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/revision"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
)

// Host is the runtime shell: lifecycle, intervention entry points, event projection and model
// management.
// Scheduling and execution live in engine (a deterministic loop); semantic adjudication lives
// in arbiter (LLM-as-function).
type Host struct {
	cfg             bootstrap.Config
	bundle          assets.Bundle
	store           *storepkg.Store
	bookLease       *bookLease
	styleStats      *tools.StyleStatsIndex
	models          *bootstrap.ModelSet
	engine          *engine
	thinkingApplier agents.ApplyThinking // Wires every Worker when /model changes reasoning effort
	writerRestore   *ctxpack.WriterRestorePack
	userRules       *userrules.Service
	observer        *observer
	usage           *UsageTracker
	usageCancel     context.CancelFunc  // Stops autoSaveLoop and triggers a final flush
	budget          *BudgetSentinel     // Budget policy; nil when disabled (methods are nil-safe)
	gate            *ChapterAdvanceGate // Unified policy for chapter permits and one-shot pauses
	notifier        *notify.Notifier    // Unattended alerting; nil when disabled (Send is nil-safe)
	configPath      string              // Config write target: /config and /model write the file currently in effect (project-level if present, otherwise global)
	logCleanup      func()
	fileLogErr      error

	events   chan Event
	streamCh chan string
	done     chan struct{}

	mu         sync.Mutex
	lifecycle  lifecycle
	cocreating bool   // Stage co-creation occupancy: blocks concurrent import/simulate/continue during the paused window
	exclusive  string // Background exclusive job occupancy (import/simulation/revision): non-empty means a job runs, blocking other exclusive entries
	// exclusiveCancel cancels the current exclusive job: a hard budget stop or manual pause
	// must be able to halt a costly import, not just the Engine — abortWithEvent cancels it
	// while the Engine is not running (the budget sentinel's abort callback and manual Abort
	// share this one stop mechanism). releaseExclusive clears it along with the rest.
	exclusiveCancel context.CancelFunc
	closeOnce       sync.Once
	asyncWG         sync.WaitGroup
	closing         bool

	interMu sync.Mutex // Serialises intervention decisions FIFO (at most one consultation in flight)

	outputMu     sync.RWMutex
	outputClosed bool

	// runCtx bounds the host-side LLM adjudication calls (start adjudication, intervention
	// triage); Close cancels it so no adjudication is left in flight and uninterruptible on
	// exit.
	runCtx    context.Context
	runCancel context.CancelFunc
}

type lifecycle string

const (
	lifecycleIdle      lifecycle = "idle"
	lifecycleRunning   lifecycle = "running"
	lifecyclePaused    lifecycle = "paused"
	lifecycleCompleted lifecycle = "completed"
)

// New creates a Host.
func New(cfg bootstrap.Config, bundle assets.Bundle, options ...NewOption) (*Host, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	var opts newOptions
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}

	bookLease, err := acquireBookLease(cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	keepBookLease := false
	var logCleanup func()
	defer func() {
		if keepBookLease {
			return
		}
		if err := bookLease.Close(); err != nil {
			slog.Error("giải phóng thư mục tiểu thuyết thất bại", "module", "host", "dir", cfg.OutputDir, "err", err)
		}
		if logCleanup != nil {
			logCleanup()
		}
	}()

	var fileLogErr error
	if opts.logFile != "" {
		logCleanup, fileLogErr = runtimelog.SetupFile(cfg.OutputDir, opts.logFile, opts.logAlsoStderr, opts.logAttrs...)
		if fileLogErr != nil {
			logCleanup = nil
			slog.Warn("không dùng được log ghi file, tiếp tục dùng log của tiến trình hiện tại", "module", "host", "file", opts.logFile, "err", fileLogErr)
		}
	}

	slog.Info("khởi động", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	// Spawns a background goroutine refreshing model metadata (window/pricing) from OpenRouter, cached on disk for 24h.
	modelreg.StartPricingRefresh(modelreg.DefaultRegistry(), bootstrap.DefaultConfigDir())

	store := storepkg.NewStore(cfg.OutputDir)
	// Labels in derived Markdown follow the work's language: novel_context reads these views
	// back into context, and labels in a different language from the prose drag the model
	// toward that other language.
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}
	// RunMeta is the source of truth for every control semantic, so validation must finish
	// before models or background tasks are built. An unknown advance mode returns a structured
	// error outright — never guess a degraded value and keep writing to disk.
	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		return nil, fmt.Errorf("init run meta: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("model sẵn sàng", "module", "boot", "summary", models.Summary())

	usage := NewUsageTracker(models, store)
	// meta/usage.json is read first; the following cases all fall back to a one-shot refill
	// from sessions/*.jsonl:
	//   - the file does not exist (before the first persist)
	//   - the schema version does not match (discard the old format after a future upgrade)
	//   - the file exists but is corrupt or an IO error occurred (bad data must not zero the
	//     totals permanently)
	// SaveNow runs immediately after the refill to lock the result in, so the next start hits
	// the Load path directly.
	loaded, loadErr := usage.LoadFromStore()
	if loadErr != nil {
		slog.Warn("nạp usage thất bại, sẽ thử điền lại từ sessions", "module", "usage", "err", loadErr)
	}
	if !loaded {
		if n, err := usage.ReplaySessions(cfg.OutputDir); err != nil {
			slog.Warn("replay usage thất bại", "module", "usage", "err", err)
		} else if n > 0 {
			slog.Info("điền lại usage từ session xong", "module", "usage", "messages", n)
			if err := usage.SaveNow(); err != nil {
				slog.Warn("lưu usage sau khi điền lại thất bại", "module", "usage", "err", err)
			}
		}
	}
	usageCtx, usageCancel := context.WithCancel(context.Background())
	usage.StartAutoSave(usageCtx)

	// onGuardBlock is declared up front: the event-surfacing closure can only be attached once h is constructed.
	var onGuardBlock func(agent, reason string, consecutive int32)
	styleStats := tools.NewStyleStatsIndex(store)
	workers, restore, applyThinking := agents.BuildWorkers(cfg, store, styleStats, models, bundle, usage.Record,
		func(agent, reason string, consecutive int32) {
			if onGuardBlock != nil {
				onGuardBlock(agent, reason, consecutive)
			}
		})
	store.Signals.ClearStaleSignals()

	h := &Host{
		cfg:             cfg,
		bundle:          bundle,
		store:           store,
		bookLease:       bookLease,
		styleStats:      styleStats,
		models:          models,
		thinkingApplier: applyThinking,
		writerRestore:   restore,
		userRules:       userrules.NewService(store, models.Default, rules.DefaultOptions()),
		usage:           usage,
		usageCancel:     usageCancel,
		configPath:      bootstrap.EffectiveConfigPath(),
		logCleanup:      logCleanup,
		fileLogErr:      fileLogErr,
		events:          make(chan Event, 100),
		streamCh:        make(chan string, 256),
		done:            make(chan struct{}, 4),
		lifecycle:       lifecycleIdle,
	}
	h.runCtx, h.runCancel = context.WithCancel(context.Background())
	h.observer = newObserver(store, h.emitEvent, h.emitDelta, h.emitClear)
	// The host-side Arbiter and the Worker share one ToolProgress → observer → workbench chain.
	h.runCtx = agentcore.WithToolProgress(h.runCtx, h.observer.workerProgress)
	if cfg.Notify.IsEnabled() {
		h.notifier = notify.New(cfg.Notify.Command, cfg.Notify.Events)
	}
	// Budget sentinel: the Engine calls HandleBoundary directly at each loop boundary (no longer via event subscription).
	if sentinel := NewBudgetSentinel(cfg.Budget,
		func() float64 { c, _, _, _, _ := usage.Totals(); return c },
		func(reason string) { h.abortWithEvent(reason, "error") },
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: notify.KindBudget, Level: level, Title: "ainovel: ngân sách", Body: summary})
		},
	); sentinel != nil {
		h.budget = sentinel
		usage.SetOnCost(sentinel.OnCost)
		// Billing blind-spot warning: with no usage reported, cost stays 0 and the budget never triggers — a disconnected fuse must shout.
		usage.SetOnMissingUsage(func() {
			const blind = "Điểm mù ngân sách: model không trả dữ liệu usage, thống kê chi phí bằng 0, giới hạn ngân sách sẽ không kích hoạt (model tùy chỉnh hãy xác nhận giá trong registry hoặc include_usage từ thượng nguồn)"
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: blind, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindBudget, Level: "warn", Title: "ainovel: ngân sách", Body: blind})
		})
	}
	// Unified advance gate: performs one-shot holds and blocks unlicensed new chapters under review mode.
	h.gate = NewChapterAdvanceGate(store,
		func(reason string) {
			h.abortWithEvent(reason, "info")
			h.notifier.Send(notify.Notification{Kind: notify.KindAdvanceGate, Level: "info", Title: "ainovel: chờ nghiệm thu", Body: reason})
		},
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: notify.KindAdvanceGate, Level: level, Title: "ainovel: đẩy chương", Body: summary})
		},
	)
	// StopGuard interception surfacing: blocked is a high-frequency self-healing action and goes
	// only into the on-screen event stream (pushing would flood); escalated / hard_stop means the
	// round's subtask is scrapped, so an event and a notify are emitted as a pair (architecture
	// §2.3).
	onGuardBlock = func(agent, reason string, n int32) {
		switch reason {
		case "escalated":
			body := fmt.Sprintf("%s chạy không tải %d lần liên tiếp không ghi sản phẩm cần thiết, nhiệm vụ lần này kết thúc, trả về cho Engine xử lý", agent, n)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard nâng mức: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindStopGuard, Level: "warn", Title: "ainovel: StopGuard", Body: body})
		case "hard_stop":
			body := fmt.Sprintf("%s bị provider từ chối trả lời (safety/content_filter), nhiệm vụ lần này kết thúc ngay", agent)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard nâng mức: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindStopGuard, Level: "warn", Title: "ainovel: StopGuard", Body: body})
		default: // blocked
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent,
				Summary: fmt.Sprintf("StopGuard: %s chưa hoàn thành sản phẩm cần thiết đã định kết thúc, đã chặn và thúc giục (lần liên tiếp thứ %d)", agent, n), Level: "info"})
		}
	}
	// Engine: the deterministic execution engine (docs/engine-rfc.md). The arbiter uses the
	// Default model (a transitional constraint, see engine-arbiter.md §4.2).
	h.engine = &engine{
		store:           store,
		workers:         workers,
		arbiterModel:    newUsageTrackedModel(models.Default, "arbiter", usage.Record),
		failurePrompt:   bundle.Prompts.ArbiterFailure,
		planStartPrompt: bundle.Prompts.ArbiterPlanStart,
		style:           cfg.Style,
		// Synchronous re-query: blocking the engine loop for one adjudication (a few seconds) buys the guarantee that the intervention takes effect before later writing.
		reconsult: h.handleIntervention,
		observer:  h.observer,
		budget:    h.budget,
		gate:      h.gate,
		refresh:   h.refreshWriterRestore,
		emitEvent: h.emitEvent,
		notify: func(kind, level, title, body string) {
			h.notifier.Send(notify.Notification{Kind: kind, Level: level, Title: title, Body: body})
		},
		onPause: func(summary string) { h.abortWithEvent(summary, "warn") },
		onDone:  h.runEnded,
	}

	keepBookLease = true
	return h, nil
}

// ── Lifecycle ──

// PrepareUserRules generates this book's user-rules snapshot in new-book mode (deterministic on the startup side; outside the main creation Run).
//
// The argument is the user's **raw** creation request (not wrapped by BuildStartPrompt) —
// normalisation wants the user rules themselves, not the startup scaffolding. Entries must call
// this once before StartPrepared (both new-book paths, quick and cocreate, go through here).
//
// A normalisation failure only degrades rather than erroring (it is an enhancement path); only
// an unwritable snapshot returns an error and aborts book creation — later runs would have no
// stable source of truth (see the design's §failures and degradation).
func (h *Host) PrepareUserRules(rawPrompt string) error {
	if err := h.refuseNewBookOverExisting(); err != nil {
		return err
	}
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.Build(context.Background(), rawPrompt)
	if err != nil {
		return fmt.Errorf("ghi snapshot quy tắc người dùng xuống đĩa thất bại, không thể tiếp tục: %w", err)
	}
	logUserRulesSnapshot(snap)
	return nil
}

// ensureUserRules guarantees the snapshot on recovery paths, generating it from
// system_defaults + the rules file when absent.
func (h *Host) ensureUserRules() {
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.GetOrBuild(context.Background())
	if err != nil {
		slog.Warn("đọc/sinh snapshot quy tắc người dùng thất bại, runtime sẽ lùi về mặc định tích hợp", "module", "rules", "err", err)
		return
	}
	logUserRulesSnapshot(snap)
}

// logUserRulesSnapshot echoes at startup so the user sees what the system understood the rules to be (reusing logging, no new mechanism).
func logUserRulesSnapshot(snap *rules.Snapshot) {
	if snap == nil {
		return
	}
	slog.Info("snapshot quy tắc người dùng",
		"module", "rules",
		"status", string(snap.Status),
		"sources", snap.Sources,
		"forbidden_phrases", len(snap.Structured.ForbiddenPhrases),
		"fatigue_words", len(snap.Structured.FatigueWords),
	)
	if snap.Status == rules.StatusDegraded {
		slog.Warn("một phần quy tắc không phân tích được, đang chạy theo raw preferences (có thể sinh lại snapshot)",
			"module", "rules", "uncertain", snap.Uncertain)
	}
}

// StartPrepared begins creation from the user's **raw** creation request: the plan_start
// adjudication picks a planner and expands the requirements, and the result is frozen as a fact
// (PlanStartRecord) before the Engine starts — recovery always relies on persisted facts and
// never redoes an adjudication it already has. The input fact (StartPrompt) is persisted before
// the adjudication: if that fails it is the basis for the engine to adjudicate later, so a
// failed start can self-heal from any recovery entry point (Resume / continue) rather than being
// a dead end.
func (h *Host) StartPrepared(rawRequirement string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("đang đồng sáng tác giai đoạn, hãy kết thúc đồng sáng tác trước")
	}
	h.mu.Unlock()

	rawRequirement = strings.TrimSpace(rawRequirement)
	if rawRequirement == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := h.refuseNewBookOverExisting(); err != nil {
		return err
	}
	if err := upgradeProject(h.store); err != nil {
		return err
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	if err := h.store.Checkpoints.Reset(); err != nil {
		return fmt.Errorf("reset checkpoints: %w", err)
	}
	if err := h.store.Progress.Init(0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}
	// The input fact is persisted before the adjudication: after a failed adjudication (model
	// fault and the like) StartPrompt is still there, and engine uses it to adjudicate later
	// (planStartFallback) on recovery/continue, so a failed start is no longer a dead end.
	if err := h.store.RunMeta.SetStartPrompt(rawRequirement); err != nil {
		return fmt.Errorf("ghi nhu cầu sáng tác: %w", err)
	}

	// Start adjudication: on failure, error out explicitly and abort (the user is present during startup, and an error beats a guess).
	start := time.Now()
	decision, derr := runObservedDecision(h.observer, "phán định khởi động", func() (arbiter.PlanStartDecision, error) {
		return arbiter.DecidePlanStart(h.runCtx, h.arbiterModel(),
			h.bundle.Prompts.ArbiterPlanStart, rawRequirement, h.cfg.Style)
	})
	rec := storepkg.DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: rawRequirement,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	var recErr error
	if rec, recErr = h.store.Decisions.Append(rec); recErr != nil {
		slog.Warn("ghi audit phán định khởi động thất bại", "module", "host", "err", recErr)
	}
	if derr != nil {
		return fmt.Errorf("phán định khởi động thất bại: %w", derr)
	}
	if err := h.store.RunMeta.SetPlanStart(domain.PlanStartRecord{
		RawPrompt: rawRequirement, Planner: decision.Planner, PlannerTask: decision.Task, DecisionID: rec.ID,
	}); err != nil {
		return fmt.Errorf("ghi phán định khởi động: %w", err)
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
		Summary: fmt.Sprintf("Bắt đầu sáng tác (kiến trúc sư: %s — %s)", decision.Planner, decision.Reason), Level: "info"})
	if !h.startEngine(&flow.Instruction{Agent: decision.Planner, Task: decision.Task, Reason: decision.Reason}) {
		return fmt.Errorf("Engine đang chạy hoặc đang dừng, không thể khởi động sách mới")
	}
	return nil
}

// refuseNewBookOverExisting refuses to start a new book in a directory that already has
// completed chapters: StartPrepared resets checkpoints and progress, so a stray trigger would
// silently wipe the whole book's progress chain (pressing Enter by mistake on the welcome page
// after an import finishes is the classic case). Only the completed-chapter count is
// considered — planning-stage or failed-start leftovers have no completed chapters, and letting
// them through preserves the self-healing paths of a same-session Ctrl+S retry in cocreation
// and of adjudication catch-up on recovery.
func (h *Host) refuseNewBookOverExisting() error {
	progress, err := h.store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if progress == nil || len(progress.CompletedChapters) == 0 {
		return nil
	}
	book, err := h.store.Book.Load()
	if err != nil {
		return err
	}
	if book == nil {
		return fmt.Errorf("thư mục đầu ra đã có chương, nhưng thông tin tác phẩm không tồn tại")
	}
	name := book.Title
	return fmt.Errorf("thư mục đầu ra đã có tiến độ sáng tác của 《%s》 gồm %d chương, tạo mới sẽ đặt lại tiến độ và checkpoint của nó: muốn viết tiếp hãy đi qua cửa khôi phục (khởi động lại ứng dụng sẽ tự khôi phục), muốn tạo sách mới hãy đổi thư mục đầu ra",
		name, len(progress.CompletedChapters))
}

// startEngine is the single engine start entry point (shared by Start/Resume/Continue and
// intervention restarts).
// lifecycle must be set to running before the goroutine starts: the engine may finish
// immediately (book complete / no route) and runEnded drops lifecycle to a terminal state; with
// the order reversed, runEnded would run first and this would then write running, leaving the UI
// permanently showing "running" while the engine has in fact stopped.
func (h *Host) startEngine(initial *flow.Instruction) bool {
	// Cross-restart gate: while an unfinished import workspace exists, the normal Engine must not consume half-published state (RFC §12.5).
	active, done, importErr := imp.ResumeStatus(h.store)
	if importErr != nil {
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
			Summary: "Đọc trạng thái nhập liệu thất bại, đã chặn sáng tác thường ghi đè sản phẩm hiện có: " + importErr.Error()})
		return false
	}
	if active && !done {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "Còn một lần nhập tiểu thuyết bên ngoài chưa hoàn tất, hãy chạy /import để hoàn tất khôi phục rồi mới viết tiếp"})
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	// While a background exclusive job (import / imitation) runs, the engine must not jump the
	// gun and race it on writes. This is the unified backstop for every engine start path
	// (Resume / Continue restart / automatic relay / next) — the entry guards are the first line,
	// this is the last.
	if h.exclusive != "" {
		return false
	}
	// lifecycle may already be paused while the old Engine goroutine is still running its exit
	// defer. The Engine's real state must be checked as well; otherwise lifecycle would be set
	// back to running while start is in fact a no-op, and the old runEnded would then drop it to
	// idle.
	if h.engine.isRunning() {
		return false
	}
	h.observer.setAborting(false)
	previous := h.lifecycle
	h.lifecycle = lifecycleRunning
	if !h.engine.start(initial) {
		h.lifecycle = previous
		return false
	}
	return true
}

// Reopen forces a completed book back into a writable state. Both completing and reopening are
// heavyweight decisions: completion may be adjudicated by the architect, while reopening can
// only be initiated explicitly by the user (/reopen) and never goes through a model
// adjudication. A non-empty direction registers a pending intervention, which on recovery is
// first adjudicated and injected by the Arbiter (the same channel as during-downtime
// interventions) before the engine resumes (the volume-end route dispatches the next volume).
func (h *Host) Reopen(direction string) error {
	h.mu.Lock()
	switch {
	case h.lifecycle == lifecycleRunning:
		h.mu.Unlock()
		return fmt.Errorf("engine sáng tác đang chạy, không cần mở lại")
	case h.cocreating:
		h.mu.Unlock()
		return fmt.Errorf("đang đồng sáng tác giai đoạn, hãy kết thúc đồng sáng tác trước")
	case h.exclusive != "":
		ex := h.exclusive
		h.mu.Unlock()
		return fmt.Errorf("%s đang chạy, hãy hoàn tất rồi mới mở lại", ex)
	}
	h.mu.Unlock()
	if err := h.requireCleanChapters(); err != nil {
		return err
	}

	if err := h.store.Progress.ReopenContinue(); err != nil {
		return err
	}
	reopenEvent := Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đã mở lại sách này về trạng thái sáng tác (người dùng huỷ phán định kết thúc)", Level: "info"}
	if d := strings.TrimSpace(direction); d != "" {
		reopenEvent.Detail = reopenEvent.Summary + "\nHướng viết tiếp: " + d
	}
	h.emitEvent(reopenEvent)
	if d := strings.TrimSpace(direction); d != "" {
		if err := h.store.RunMeta.SetPendingSteer(d); err != nil {
			return fmt.Errorf("đã mở lại, nhưng ghi hướng viết tiếp thất bại: %v, hãy nhập lại hướng trực tiếp trong ô nhập", err)
		}
	}
	return nil
}

// Resume is recovery mode: builds a resume prompt from checkpoint + progress and starts.
func (h *Host) Resume() (string, error) {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("đang đồng sáng tác giai đoạn, hãy kết thúc đồng sáng tác trước")
	}
	if h.exclusive != "" {
		ex := h.exclusive
		h.mu.Unlock()
		return "", fmt.Errorf("%s đang chạy, hãy hoàn tất rồi mới khôi phục sáng tác", ex)
	}
	h.mu.Unlock()
	if err := upgradeProject(h.store); err != nil {
		return "", err
	}

	label, err := resumeLabel(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		return "", nil // New-book mode, nothing to resume.
	}
	if err := h.requireCleanChapters(); err != nil {
		return label, err
	}
	if err := h.budget.Refuse(); err != nil {
		return "", err
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Khôi phục sáng tác: " + label, Level: "info"})
	for _, w := range h.store.CheckConsistency() {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Cảnh báo nhất quán: " + w, Level: "warn"})
	}
	// Ensure the user-rules snapshot exists; read it cheaply when it does.
	h.ensureUserRules()
	h.refreshWriterRestore()
	// Pending interventions (left over from downtime or from a crash during adjudication) must be
	// adjudicated before the engine resumes — otherwise the engine could continue writing
	// chapters that contradict the intervention before it is adjudicated. This runs synchronously
	// (blocking for a few seconds is acceptable; the UI already shows "recovering creation");
	// after success doIntervention clears PendingSteer itself and starts the engine with
	// restart=true. With no pending intervention it simply resumes.
	meta, err := h.store.RunMeta.Load()
	if err != nil {
		return label, fmt.Errorf("đọc can thiệp đang chờ xử lý: %w", err)
	}
	if meta != nil && meta.PendingSteer != "" {
		if err := h.doIntervention(meta.PendingSteer, true); err != nil {
			return label, err
		}
	} else {
		// Only facts are restored, not the session (RFC §6): the Engine recomputes the route from
		// the store and continues.
		if !h.startEngine(nil) {
			return label, fmt.Errorf("Engine đang hoàn tất lần dừng trước, hãy thử khôi phục lại sau")
		}
	}
	// lifecycle is managed by startEngine / runEnded and is not overwritten here — when the
	// engine finishes immediately (book complete and the like) an overwrite would turn a terminal
	// state back into running.
	return label, nil
}

// handleIntervention adapts the Engine's valueless re-query callback; errors are already surfaced as events by doIntervention.
func (h *Host) handleIntervention(text string) {
	_ = h.doIntervention(text, false)
}

// doIntervention is the unified adjudication path for user interventions: Collect → Decide →
// execute.
// FIFO serialisation (at most one consultation in flight at a time); answer/rules execute
// immediately, while control actions (hold/reopen/dispatch) queue for a boundary commit while
// the engine runs and execute at once while it is stopped.
// With restart=true (Continue semantics) the engine is guaranteed to run once the intervention
// is handled.
func (h *Host) doIntervention(text string, restart bool) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()

	// Crash protection: persist before adjudicating (PendingSteer), then clear atomically once
	// applied successfully or once the failure has been shown inline (ClearHandledSteer also
	// resets FlowSteering). A crash during adjudication replays on the next Resume.
	if err := h.store.RunMeta.SetPendingSteer(text); err != nil {
		wrapped := fmt.Errorf("lưu can thiệp thất bại, đã dừng phán định: %w", err)
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: "arbiter",
			Summary: wrapped.Error(), Detail: wrapped.Error(), Level: "error"})
		return wrapped
	}
	clearPending := func() error {
		if err := h.store.ClearHandledSteer(); err != nil {
			return fmt.Errorf("xóa can thiệp đã xử lý thất bại: %w", err)
		}
		return nil
	}

	facts, err := arbiter.CollectInterventionFacts(h.store)
	if err != nil {
		wrapped := fmt.Errorf("thu thập sự thật can thiệp thất bại, chưa gọi Arbiter: %w", err)
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: "arbiter",
			Summary: wrapped.Error(), Detail: wrapped.Error(), Level: "error"})
		return wrapped
	}
	facts.Running = h.engine.isRunning()

	start := time.Now()
	decision, derr := runObservedDecision(h.observer, "phán định can thiệp người dùng", func() (arbiter.InterventionDecision, error) {
		return arbiter.DecideIntervention(h.runCtx, h.arbiterModel(),
			h.bundle.Prompts.ArbiterIntervention, facts, text)
	})

	rec := storepkg.DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: text,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		rec.CheckpointSeq = cp.Seq
	}
	if data, err := json.Marshal(facts); err == nil {
		rec.Facts = data
	}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	if _, err := h.store.Decisions.Append(rec); err != nil {
		wrapped := fmt.Errorf("ghi audit phán định can thiệp thất bại, từ chối thực hiện hành động: %w", err)
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: "arbiter",
			Summary: wrapped.Error(), Detail: wrapped.Error(), Level: "error"})
		return wrapped
	}

	if derr != nil {
		// Better to do nothing than to act wrongly: no writes are produced. Call errors and output
		// validation errors share one error channel and must be echoed verbatim, never uniformly
		// disguised as "could not understand".
		// Already shown inline → clear pending (otherwise the next Resume would automatically replay the same failed intervention).
		h.emitEvent(newInterventionFailureEvent(derr))
		if err := clearPending(); err != nil {
			return fmt.Errorf("%v；%w", derr, err)
		}
		return derr
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Phán định: " + decision.Reason, Level: "info"})
	if decision.Answer != "" {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: decision.Answer, Level: "info"})
	}
	// A persistence failure on any action keeps PendingSteer (recovery replays the whole
	// intervention and re-adjudicates; hold/reopen are idempotent and dispatch re-queries through
	// new facts, so replay is safe).
	var actionErr error
	if decision.Rules != "" {
		if snap, _, err := h.userRules.AddRuntimeRule(h.runCtx, decision.Rules); err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "Ghi quy tắc viết xuống đĩa thất bại: " + err.Error(), Level: "error"})
			actionErr = err
		} else if snap != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Quy tắc viết đã được cập nhật và lưu trữ", Level: "info"})
		}
	}

	if decision.Hold != nil || decision.Reopen != nil || decision.Dispatch != nil {
		op := controlOp{hold: decision.Hold, reopen: decision.Reopen, dispatch: decision.Dispatch, text: text, facts: facts}
		if !h.engine.enqueue(op) {
			// The engine is not running: execute at once; on a persistence failure keep PendingSteer and replay the whole intervention on recovery.
			if err := h.engine.applyControlOp(context.Background(), op); err != nil {
				h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "Thực hiện hành động can thiệp thất bại, đã giữ lại; khi khôi phục/viết tiếp sẽ tự thử lại"})
				return err
			}
			// reopen/dispatch express an intent to keep creating, so start the engine.
			if decision.Reopen != nil || decision.Dispatch != nil {
				restart = true
			}
		}
	}
	if actionErr != nil {
		// Keep PendingSteer: recovery/continue replays the whole intervention and re-adjudicates.
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "Một phần hành động can thiệp không thành công, can thiệp đã được giữ lại; khi khôi phục/viết tiếp sẽ tự thử lại"})
		return actionErr
	}
	// The action applied or was queued successfully, so the crash protection is cleared (a later
	// engine-side failure or exit race is caught by the engine writing PendingSteer back).
	if err := clearPending(); err != nil {
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error", Summary: err.Error()})
		return err
	}

	if restart && !h.engine.isRunning() {
		if err := h.budget.Refuse(); err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: err.Error(), Level: "warn"})
			return err
		}
		h.refreshWriterRestore()
		if !h.startEngine(nil) {
			// The intervention has now taken effect and PendingSteer is cleared; only the immediate engine start failed — do not claim it was "saved".
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "Can thiệp đã có hiệu lực, nhưng Engine chưa chạy tiếp ngay được; hãy tiếp tục trong ô nhập sau, hoặc khởi động lại ứng dụng để khôi phục"})
			return fmt.Errorf("can thiệp đã có hiệu lực, nhưng Engine chưa chạy tiếp ngay được")
		}
	}
	return nil
}

func newInterventionFailureEvent(err error) Event {
	detail := err.Error()
	return Event{
		Time:     time.Now(),
		Category: "ERROR",
		Agent:    "arbiter",
		Summary:  "Phán định can thiệp thất bại: " + detail + " (không sửa gì cả)",
		Detail:   detail,
		Kind:     errorKind(err, detail),
		Level:    "error",
	}
}

// arbiterModel returns the adjudication model with usage tracking (tokens/cost feed the budget and usage systems).
func (h *Host) arbiterModel() agentcore.ChatModel {
	return newUsageTrackedModel(h.models.Default, "arbiter", h.usage.Record)
}

// Continue is called when the user types in the input box after a stop: adjudicate the intervention and make the engine run again.
func (h *Host) Continue(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("đang đồng sáng tác giai đoạn, hãy kết thúc đồng sáng tác trước")
	}
	if h.exclusive != "" {
		ex := h.exclusive
		h.mu.Unlock()
		// During an exclusive job this must block before adjudication: otherwise the Arbiter would already have changed PendingSteer / rules / control state by the time the gate stops the engine.
		return fmt.Errorf("%s đang chạy, hãy hoàn tất rồi mới viết tiếp", ex)
	}
	h.mu.Unlock()
	if err := h.requireCleanChapters(); err != nil {
		return err
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}

	err, launched := h.runAsync(func() error {
		h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[viết tiếp] " + text, Level: "info"})
		return h.doIntervention(text, true)
	})
	if !launched {
		return fmt.Errorf("Host đang đóng, không thể viết tiếp")
	}
	return err
}

// SetAdvanceMode switches the chapter-advance mode deterministically. It only records the
// user's run intent: it neither calls the Arbiter nor implicitly starts a paused Engine.
func (h *Host) SetAdvanceMode(mode domain.ChapterAdvanceMode) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.store.RunMeta.SetAdvanceMode(mode); err != nil {
		return err
	}
	label := "tự động đẩy"
	if mode == domain.ChapterAdvanceReview {
		label = "nghiệm thu từng chương"
	}
	summary := "Chế độ đẩy chương đã chuyển thành " + label
	h.mu.Lock()
	state := h.lifecycle
	h.mu.Unlock()
	if mode == domain.ChapterAdvanceAuto && state != lifecycleRunning && state != lifecycleCompleted {
		summary += "; hiện vẫn tạm dừng, nhập chỉ thị viết tiếp để chạy lại"
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
	return nil
}

// AdvanceOneChapter authorises one exact chapter under per-chapter review mode and starts the Engine.
func (h *Host) AdvanceOneChapter() error {
	h.interMu.Lock()
	defer h.interMu.Unlock()

	h.mu.Lock()
	running, cocreating, ex := h.lifecycle == lifecycleRunning, h.cocreating, h.exclusive
	h.mu.Unlock()
	if running || h.engine.isRunning() {
		return fmt.Errorf("sáng tác vẫn đang chạy hoặc đang hoàn tất tạm dừng, hãy chạy /next sau")
	}
	if cocreating {
		return fmt.Errorf("đang đồng sáng tác giai đoạn, hãy kết thúc đồng sáng tác trước")
	}
	if ex != "" {
		return fmt.Errorf("%s đang chạy, hãy hoàn tất rồi mới chạy /next", ex)
	}
	if err := h.requireCleanChapters(); err != nil {
		return err
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("RunMeta chưa được khởi tạo")
	}
	if meta.AdvanceMode != domain.ChapterAdvanceReview {
		return fmt.Errorf("/next chỉ dùng ở chế độ nghiệm thu từng chương, hãy chạy /review on trước")
	}
	if meta.AdvanceHold != nil {
		return fmt.Errorf("vẫn còn ý định tạm dừng một lần đang chờ xử lý (%s), hãy khôi phục hoặc hoàn tất can thiệp hiện tại trước", meta.AdvanceHold.Reason)
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	progress, err := h.store.Progress.Load()
	if err != nil {
		return err
	}
	if progress == nil || progress.Phase != domain.PhaseWriting {
		phase := "<nil>"
		if progress != nil {
			phase = string(progress.Phase)
		}
		return fmt.Errorf("giai đoạn hiện tại không thể cấp phép chương mới (phase=%s)", phase)
	}
	target := progress.NextChapter()
	if target <= 0 {
		return fmt.Errorf("không thể suy ra chương kế tiếp từ tiến độ hiện tại")
	}
	if err := h.store.RunMeta.GrantAdvancePermit(target); err != nil {
		return err
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
		Summary: fmt.Sprintf("Đã cho qua chương %d; sau khi chương đó được commit sẽ hoàn tất thẩm duyệt và bảo trì cấu trúc cung/tập cần thiết, rồi chờ cho qua lần nữa", target), Level: "info"})
	h.refreshWriterRestore()
	if !h.startEngine(nil) {
		// The licence is persisted per chapter number and is idempotent for the same target, so a later caller retry does not authorise twice.
		return fmt.Errorf("giấy phép chương đã lưu, nhưng Engine vẫn đang hoàn tất lần dừng trước; hãy thử /next lại sau")
	}
	return nil
}

// Steer submits a user intervention (available any time while running; while stopped, the action decides after adjudication whether to start the engine).
// The TUI waits for the result through a tea.Cmd, so it receives real adjudication/persistence errors without blocking the interface.
func (h *Host) Steer(text string) error {
	err, launched := h.runAsync(func() error {
		h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[can thiệp người dùng] " + text, Level: "info"})
		return h.doIntervention(text, false)
	})
	if !launched {
		return fmt.Errorf("Host đang đóng, không thể gửi can thiệp")
	}
	return err
}

// Abort pauses the current engine loop.
func (h *Host) Abort() bool {
	return h.abortWithEvent("Người dùng tạm dừng sáng tác thủ công", "warn")
}

// abortWithEvent performs the pause with an event carrying the given reason. Budget stops and
// manual pauses share one stop mechanism, differing only in event wording (a budget stop is an
// Abort instruction the user pre-signed, semantically identical to a manual pause).
func (h *Host) abortWithEvent(summary, level string) bool {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	if running {
		h.lifecycle = lifecyclePaused
	}
	cancelExclusive := h.exclusiveCancel
	h.mu.Unlock()
	if running {
		// The flag must be set before engine.abort: cancellation propagates immediately into
		// stream init / worker failure events, and the observer uses this flag to recognise and
		// suppress them as abort-derived noise.
		h.observer.setAborting(true)
		h.engine.abort()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
		return true
	}
	// The Engine is not running but an exclusive job (an import) is: it burns money too, so a hard
	// budget stop or manual pause must be able to halt it — otherwise the budget policy is void
	// for imports (docs/import-pipeline.md §13.1).
	if cancelExclusive != nil {
		cancelExclusive()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
		return true
	}
	return false
}

// Close terminates the engine and closes the event channels.
//
// Usage persistence semantics: cancel autoSaveLoop first (it flushes the last dirty state
// itself), then add one synchronous SaveNow to finish. The last few hundred tokens of an
// in-flight LLM call lost after termination are made up automatically by the session jsonl
// replay on the next start.
func (h *Host) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closing = true
		cancelExclusive := h.exclusiveCancel
		h.mu.Unlock()

		h.observer.setAborting(true)
		if h.runCancel != nil {
			h.runCancel() // Interrupt in-flight host-side decision calls and supervisor forwarding.
		}
		if cancelExclusive != nil {
			cancelExclusive()
		}
		h.engine.abort()
		h.engine.wait()
		h.asyncWG.Wait()

		if h.usageCancel != nil {
			h.usageCancel()
			h.usageCancel = nil
		}
		h.usage.WaitAutoSave()
		if err := h.usage.SaveNow(); err != nil {
			slog.Warn("ghi usage xuống đĩa trước khi thoát thất bại", "module", "usage", "err", err)
		}
		h.closeOutputChannels()
		if err := h.bookLease.Close(); err != nil {
			slog.Error("giải phóng thư mục tiểu thuyết thất bại", "module", "host", "dir", h.cfg.OutputDir, "err", err)
		}
		if h.logCleanup != nil {
			h.logCleanup()
			h.logCleanup = nil
		}
	})
}

// FileLogError returns the file-log initialisation error from construction; it never changes over the Host's lifetime.
func (h *Host) FileLogError() error {
	return h.fileLogErr
}

// runEnded is the engine.onDone callback when the engine loop ends (for any reason): it fixes the
// terminal state from store facts.
//   - Phase=Complete  → mark completed and emit a "creation complete" event
//   - anything else   → mark idle/paused and emit a "creation stopped" event
func (h *Host) runEnded() {
	h.observer.finalize()

	h.mu.Lock()
	progress, err := h.store.Progress.Load()
	if err != nil {
		if h.lifecycle == lifecycleRunning {
			h.lifecycle = lifecycleIdle
		}
		h.mu.Unlock()
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
			Summary: "Đọc tiến độ khi engine kết thúc thất bại: " + err.Error()})
		select {
		case h.done <- struct{}{}:
		default:
		}
		return
	}
	book, err := h.store.Book.Load()
	if err != nil {
		h.lifecycle = lifecycleIdle
		h.mu.Unlock()
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
			Summary: "Đọc thông tin tác phẩm khi engine kết thúc thất bại: " + err.Error()})
		select {
		case h.done <- struct{}{}:
		default:
		}
		return
	}
	if progress != nil && progress.Phase == domain.PhaseComplete {
		if book == nil {
			h.lifecycle = lifecycleIdle
			h.mu.Unlock()
			h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
				Summary: "Thông tin tác phẩm không tồn tại khi engine kết thúc"})
			select {
			case h.done <- struct{}{}:
			default:
			}
			return
		}
		h.lifecycle = lifecycleCompleted
		// Book-completion wrap-up: deterministic generation (the store already holds every fact, so no LLM call is spent; RFC final section).
		summary := completionSummary(*progress, *book)
		h.mu.Unlock()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
		h.notifier.Send(notify.Notification{
			Kind: notify.KindRunEnd, Level: "info", Title: "ainovel: sáng tác hoàn tất",
			Body: h.runEndBody("", summary),
		})
	} else {
		wasRunning := h.lifecycle == lifecycleRunning
		if wasRunning {
			h.lifecycle = lifecycleIdle
		}
		completed := 0
		title := ""
		if progress != nil {
			completed = len(progress.CompletedChapters)
		}
		if book != nil {
			title = book.Title
		}
		h.mu.Unlock()
		if wasRunning {
			summary := fmt.Sprintf("Engine dừng (đã hoàn thành %d chương)", completed)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
			h.notifier.Send(notify.Notification{
				Kind: notify.KindRunEnd, Level: "warn", Title: "ainovel: sáng tác dừng",
				Body: h.runEndBody(title, summary),
			})
		}
	}

	select {
	case h.done <- struct{}{}:
	default:
	}
}

// runEndBody assembles the run_end notification body: book title + progress summary + accumulated cost.
func (h *Host) runEndBody(title, summary string) string {
	if name := strings.TrimSpace(title); name != "" {
		summary = "《" + name + "》" + summary
	}
	cost, _, _, _, _ := h.usage.Totals()
	if cost > 0 {
		summary += fmt.Sprintf(" · chi phí $%.2f", cost)
	}
	return summary
}

// ── Channels ──

// StreamClearSentinel is sent as a single item through streamCh to signal "clear the current
// streaming round".
// A separate clearCh is no longer used — two unordered channels made the ✻ header often land at
// the end of the previous round.
const StreamClearSentinel = "\x00\x00CLEAR\x00\x00"

func (h *Host) Events() <-chan Event  { return h.events }
func (h *Host) Stream() <-chan string { return h.streamCh }
func (h *Host) Done() <-chan struct{} { return h.done }
func (h *Host) Dir() string           { return h.store.Dir() }

// ── Event emission ──

func (h *Host) emitEvent(ev Event) {
	h.outputMu.RLock()
	defer h.outputMu.RUnlock()
	if h.outputClosed {
		return
	}
	// The read lock guarantees events are written in full before the close; events after the close are rejected outright.
	LogEvent(ev)
	select {
	case h.events <- ev:
	default:
		select {
		case <-h.events:
		default:
		}
		select {
		case h.events <- ev:
		default:
		}
	}
}

func (h *Host) emitDelta(delta string) {
	h.outputMu.RLock()
	defer h.outputMu.RUnlock()
	if h.outputClosed {
		return
	}
	select {
	case h.streamCh <- delta:
	default:
		select {
		case <-h.streamCh:
		default:
		}
		select {
		case h.streamCh <- delta:
		default:
		}
	}
}

func (h *Host) closeOutputChannels() {
	h.outputMu.Lock()
	defer h.outputMu.Unlock()
	if h.outputClosed {
		return
	}
	h.outputClosed = true
	close(h.done)
	close(h.events)
	close(h.streamCh)
}

func (h *Host) emitClear() {
	// It goes through streamCh as a sentinel, guaranteeing ordered delivery to the TUI on the same channel as emitDelta.
	h.emitDelta(StreamClearSentinel)
}

// ── Snapshot (TUI state aggregation) ──

func (h *Host) Snapshot() UISnapshot {
	h.mu.Lock()
	state := h.lifecycle
	provider, model, _ := h.models.CurrentSelection("default")
	modelWindow, _ := h.cfg.ResolveContextWindow(provider, model)
	thinkingLevel := h.cfg.ResolveReasoningEffort("default")
	style := h.cfg.Style
	h.mu.Unlock()

	// Resolves the current model's context window dynamically; the next Snapshot reflects a /model or /config switch automatically.
	cost, tokIn, tokOut, cacheRead, cacheWrite := h.usage.Totals()
	saved := h.usage.SavedUSD()
	overallCapable := h.usage.OverallCacheCapable()
	recentRead, recentInput, recentSamples := h.usage.OverallRecent()
	perAgent := h.usage.PerAgent()
	cacheStats := make([]AgentCacheStat, 0, len(perAgent))
	for _, a := range perAgent {
		cacheStats = append(cacheStats, AgentCacheStat{
			Role:            a.Role,
			Input:           a.Input,
			Output:          a.Output,
			CacheRead:       a.CacheRead,
			CacheWrite:      a.CacheWrite,
			Cost:            a.Cost,
			Saved:           a.Saved,
			CacheCapable:    a.CacheCapable,
			RecentCacheRead: a.RecentCacheRead,
			RecentInput:     a.RecentInput,
			RecentSamples:   a.RecentSamples,
		})
	}
	perModel := h.usage.PerModel()
	modelStats := make([]AgentCacheStat, 0, len(perModel))
	for _, a := range perModel {
		modelStats = append(modelStats, AgentCacheStat{
			Model:        a.Model,
			Input:        a.Input,
			Output:       a.Output,
			CacheRead:    a.CacheRead,
			CacheWrite:   a.CacheWrite,
			Cost:         a.Cost,
			Saved:        a.Saved,
			CacheCapable: a.CacheCapable,
		})
	}

	snap := UISnapshot{
		Provider:               provider,
		ModelName:              model,
		ModelContextWindow:     modelWindow,
		ThinkingLevel:          thinkingLevel,
		Style:                  style,
		RuntimeState:           string(state),
		IsRunning:              state == lifecycleRunning,
		TotalInputTokens:       tokIn,
		TotalOutputTokens:      tokOut,
		TotalCacheReadTokens:   cacheRead,
		TotalCacheWriteTokens:  cacheWrite,
		TotalCostUSD:           cost,
		TotalSavedUSD:          saved,
		BudgetLimitUSD:         h.budget.Limit(),
		OverallCacheCapable:    overallCapable,
		OverallRecentCacheRead: recentRead,
		OverallRecentInput:     recentInput,
		OverallRecentSamples:   recentSamples,
		TotalCacheBreaks:       h.usage.OverallCacheBreaks(),
		CachePerAgent:          cacheStats,
		CachePerModel:          modelStats,
		MissingAssistantUsage:  h.usage.MissingAssistantUsage(),
	}

	if book, _ := h.store.Book.Load(); book != nil {
		snap.BookTitle = book.Title
		snap.Synopsis = truncate(book.Synopsis, 200)
	}
	progress, _ := h.store.Progress.Load()
	if progress != nil {
		snap.Phase = string(progress.Phase)
		snap.Flow = string(progress.Flow)
		snap.CurrentChapter = progress.CurrentChapter
		snap.TotalChapters = progress.TotalChapters
		snap.CompletedCount = len(progress.CompletedChapters)
		snap.TotalWordCount = progress.TotalWordCount
		snap.InProgressChapter = progress.InProgressChapter
		snap.PendingRewrites = progress.PendingRewrites
		snap.RewriteReason = progress.RewriteReason
		snap.Layered = progress.Layered
		if progress.CurrentVolume > 0 {
			snap.CurrentVolumeArc = fmt.Sprintf("tập %d·cung %d", progress.CurrentVolume, progress.CurrentArc)
		}
	}
	if meta, _ := h.store.RunMeta.Load(); meta != nil {
		snap.PendingSteer = meta.PendingSteer
		snap.AdvanceMode = string(meta.AdvanceMode)
		snap.AdvancePermitChapter = meta.AdvancePermitChapter
		if meta.AdvanceHold != nil {
			snap.HasAdvanceHold = true
			snap.AdvanceHoldReason = meta.AdvanceHold.Reason
		}
	}

	snap.Agents = h.observer.agentSnapshots()
	snap.StatusLabel = deriveStatusLabel(snap)

	// Restore labels
	if label, err := resumeLabel(h.store); err == nil && label != "" {
		snap.RecoveryLabel = label
	}

	h.fillDetails(&snap, progress)

	return snap
}

// fillDetails populates the details pane: premise, characters, the latest commit/review/summary.
func (h *Host) fillDetails(snap *UISnapshot, progress *domain.Progress) {
	if premise, _ := h.store.Outline.LoadPremise(); premise != "" {
		snap.Premise = truncate(premise, 80)
	}
	if outline, _ := h.store.Outline.LoadOutline(); len(outline) > 0 {
		completed := make(map[int]struct{})
		if progress != nil {
			completed = make(map[int]struct{}, len(progress.CompletedChapters))
			for _, chapter := range progress.CompletedChapters {
				completed[chapter] = struct{}{}
			}
		}
		for _, e := range outline {
			title := e.Title
			if _, ok := completed[e.Chapter]; ok {
				committedTitle, err := h.store.Summaries.LoadSummaryTitle(e.Chapter)
				if err != nil {
					slog.Warn("chiếu tiêu đề chương thất bại", "module", "host.snapshot", "chapter", e.Chapter, "err", err)
				} else if strings.TrimSpace(committedTitle) != "" {
					title = committedTitle
				}
			}
			snap.Outline = append(snap.Outline, OutlineSnapshot{
				Chapter: e.Chapter, Title: title, CoreEvent: e.CoreEvent,
			})
		}
	}
	if progress != nil && progress.Layered {
		if compass, _ := h.store.Outline.LoadCompass(); compass != nil {
			snap.CompassDirection = compass.EndingDirection
			snap.CompassScale = compass.EstimatedScale
		}
		if volumes, _ := h.store.Outline.LoadLayeredOutline(); len(volumes) > 0 {
			for _, v := range volumes {
				if v.Index > progress.CurrentVolume {
					snap.NextVolumeTitle = v.Title
					break
				}
			}
		}
	}
	if chars, _ := h.store.Characters.Load(); len(chars) > 0 {
		for _, c := range chars {
			label := c.Name
			if c.Role != "" {
				label += "（" + c.Role + "）"
			}
			snap.Characters = append(snap.Characters, label)
		}
	}
	if ledger, _ := h.store.Cast.Load(); len(ledger) > 0 {
		snap.SupportingCount = len(ledger)
		recent, _ := h.store.Cast.RecentActive(5)
		for _, e := range recent {
			label := e.Name
			if e.BriefRole != "" {
				label += "（" + e.BriefRole + "）"
			}
			snap.RecentSupporting = append(snap.RecentSupporting, label)
		}
	}
	if progress != nil && len(progress.CompletedChapters) > 0 {
		lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
		wc := progress.ChapterWordCounts[lastCh]
		snap.LastCommitSummary = fmt.Sprintf("chương %d %d chữ", lastCh, wc)
	}
	currentCh := 1
	if progress != nil && len(progress.CompletedChapters) > 0 {
		currentCh = progress.CompletedChapters[len(progress.CompletedChapters)-1]
	}
	if review, err := h.store.World.LoadLastReview(currentCh); err == nil && review != nil {
		snap.LastReviewSummary = fmt.Sprintf("verdict=%s %d vấn đề", review.Verdict, len(review.Issues))
		if len(review.AffectedChapters) > 0 {
			snap.LastReviewSummary += fmt.Sprintf(" ảnh hưởng %v", review.AffectedChapters)
		}
	}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		snap.LastCheckpointName = fmt.Sprintf("%s.%s", cp.Scope, cp.Step)
	}
	if progress != nil {
		for i := len(progress.CompletedChapters) - 1; i >= 0 && len(snap.RecentSummaries) < 2; i-- {
			ch := progress.CompletedChapters[i]
			if summary, err := h.store.Summaries.LoadSummary(ch); err == nil && summary != nil {
				snap.RecentSummaries = append(snap.RecentSummaries,
					fmt.Sprintf("chương %d: %s", ch, truncate(summary.Summary, 50)))
			}
		}
	}
}

func deriveStatusLabel(s UISnapshot) string {
	switch {
	case s.Phase == string(domain.PhaseComplete):
		return "COMPLETE"
	case s.Flow == string(domain.FlowReviewing):
		return "REVIEW"
	case s.Flow == string(domain.FlowRewriting) || s.Flow == string(domain.FlowPolishing):
		return "REWRITE"
	case s.RuntimeState == "running":
		return "RUNNING"
	default:
		return "READY"
	}
}

// ── Model management ──

func (h *Host) ConfiguredProviders() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	providers := make([]string, 0, len(h.cfg.Providers))
	for name := range h.cfg.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

func (h *Host) ConfiguredModels(provider string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.CandidateModels(provider)
}

func (h *Host) CurrentModelSelection(role string) (string, string, bool) {
	return h.models.CurrentSelection(role)
}

func (h *Host) SwitchModel(role, provider, model string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	if err := h.models.Swap(role, provider, model); err != nil {
		return err
	}
	if role == "" || role == "default" {
		h.cfg.Provider = provider
		h.cfg.ModelName = model
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.Provider = provider
		rc.Model = model
		h.cfg.Roles[role] = rc
	}
	// Switching models leaves the stored reasoning-effort intent untouched: clamping to the new model's capabilities happens only when dispatching.
	if h.configPath != "" {
		if err := bootstrap.SaveConfig(h.configPath, h.cfg); err != nil {
			slog.Warn("lưu cấu hình thất bại", "module", "host", "err", err)
		}
	}
	h.applyThinkingLocked(role)
	// Switching to an unregistered model logs one warn line, telling the user the 128k fallback is in effect — long works get compacted early.
	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	window, source := h.cfg.ResolveContextWindow(provider, model)
	bootstrap.LogContextWindowChoice(logRole, model, window, source)

	// No resident context needs updating: the writer/architect/editor ContextManagers go through
	// ContextManagerFactory and rebuild automatically for the new model's window on the next
	// spawn.

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("Đã chuyển model: %s → %s/%s", role, provider, model),
		Level:    "info",
	})
	return nil
}

// concreteThinkingRoles lists the concrete roles reasoning effort applies to (matching the agents.ApplyThinking routing).
// Setting default re-applies per role through each role's ResolveReasoningEffort.
var concreteThinkingRoles = []string{"architect", "writer", "editor"}

// CurrentThinking returns the raw string of a role's currently effective reasoning effort (so the /model panel can sync the current value).
func (h *Host) CurrentThinking(role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ResolveReasoningEffort(strings.ToLower(strings.TrimSpace(role)))
}

func (h *Host) AvailableThinking(role string) []agentcore.ThinkingLevel {
	h.mu.Lock()
	model := h.models.ForRole(strings.ToLower(strings.TrimSpace(role)))
	h.mu.Unlock()
	return agents.AvailableThinkingForModel(model)
}

// resolveThinkingForRoleLocked computes a role's effectively applied reasoning effort: take its
// raw intent (ResolveReasoningEffort: role level → top-level default), then clamp it to the
// capability of that role's current model.
// The clamp happens only on this "effective path" and never writes back to config — storage
// always keeps the user's raw intent.
func (h *Host) resolveThinkingForRoleLocked(role string) agentcore.ThinkingLevel {
	parsed, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role))
	resolved, _ := agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	return resolved
}

// applyThinkingLocked dispatches the effective effort to live agents, each clamped to its own model.
func (h *Host) applyThinkingLocked(role string) {
	if h.thinkingApplier == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		for _, r := range concreteThinkingRoles {
			h.thinkingApplier(r, h.resolveThinkingForRoleLocked(r))
		}
		return
	}
	h.thinkingApplier(role, h.resolveThinkingForRoleLocked(role))
}

// SetRoleThinking sets a role's (or the default's) reasoning effort: validate → persist → update live agents → emit event.
// Mirrors the structure of SwitchModel; orthogonal to model choice and adjustable on its own. An empty level means "do not override" (inherit).
func (h *Host) SetRoleThinking(role, level string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	parsed, err := agents.ParseThinkingLevel(level)
	if err != nil {
		return err
	}
	role = strings.ToLower(strings.TrimSpace(role))
	// Storage keeps the raw intent: the user-chosen effort is persisted directly, and clamping happens only at dispatch (applyThinkingLocked) per model capability.
	if role == "" || role == "default" {
		h.cfg.ReasoningEffort = string(parsed)
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.ReasoningEffort = string(parsed)
		h.cfg.Roles[role] = rc
	}
	if h.configPath != "" {
		if err := bootstrap.SaveConfig(h.configPath, h.cfg); err != nil {
			slog.Warn("lưu cấu hình thất bại", "module", "host", "err", err)
		}
	}

	// Update live agents: concrete roles apply directly; default walks every concrete role and re-applies through ResolveReasoningEffort
	// (roles already overridden at role level keep theirs; the rest pick up the new default).
	h.applyThinkingLocked(role)

	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	shown := string(parsed)
	if shown == "" {
		shown = "mặc định (kế thừa)"
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("Đã chuyển mức suy luận: %s → %s", logRole, shown),
		Level:    "info",
	})
	return nil
}

// ── Event replay ──

func (h *Host) ReplayQueue(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	if h.store == nil || h.store.Runtime == nil {
		return nil, nil
	}
	return h.store.Runtime.LoadQueueAfter(afterSeq)
}

// ── Cocreation ──

// CoCreateStream is cold-start cocreation: clarify requirements from scratch and produce the creation brief for the whole book.
func (h *Host) CoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, coCreateSystemPrompt, history, onProgress)
}

// StageCoCreateStream is stage cocreation: plan the next direction on top of what has been written.
// The system prompt is the stage prompt plus a summary of the current story state, so the assistant knows what has been written already.
func (h *Host) StageCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, stageSystemPrompt(h.store), history, onProgress)
}

// stagePlanPrefix wraps the "next direction brief" from cocreation into a stage-planning intervention for the Arbiter to adjudicate.
// It attaches only the [stage planning] fact marker plus a neutral statement and never hardcodes
// "how to land it" — the concrete route (compass / architect / user_rules) is left to the
// "stage planning" criterion in arbiter-intervention.md, avoiding a second source of truth
// competing with the prompt and keeping style requests free to go through user_rules (honouring
// "classification is adjudicated by the LLM"). Continue then layers on the [user intervention]
// prefix.
const stagePlanPrefix = "[Quy hoạch giai đoạn] Tôi đã tạm dừng sáng tác và cùng trợ lý đồng sáng tác sắp xếp hướng đi tiếp theo dưới đây; hãy phán định cách triển khai theo phân loại can thiệp của bạn rồi viết tiếp. Hướng đi tiếp theo như sau:\n\n"

// PauseForCoCreate enters stage cocreation: sets the cocreation occupancy flag and pauses the
// Engine as well while it is running.
// A false result means entry is impossible (the book is complete or cocreation is already
// active) and the caller may ignore it.
// The occupancy flag blocks concurrent import/simulate/start/resume/continue within the
// cocreation window — pausing a running book leaves lifecycle=paused, which defeats the existing
// ==running mutual exclusion, and this flag fills the gap; an already-stopped book (idle/paused)
// may also enter, and continues through Continue once planning is done.
func (h *Host) PauseForCoCreate() bool {
	h.mu.Lock()
	if h.cocreating || h.lifecycle == lifecycleCompleted {
		h.mu.Unlock()
		return false
	}
	h.cocreating = true
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	// While running, reuse abortWithEvent to stop (running→paused + setAborting + Abort + event),
	// the same sequence as a manual pause rather than a copy of it; while stopped (idle/paused)
	// only the flag is set, and it continues through Continue once planning is done.
	if running {
		h.abortWithEvent("Vào đồng sáng tác giai đoạn, sáng tác đã tạm dừng", "info")
	} else {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Vào đồng sáng tác giai đoạn", Level: "info"})
	}
	return true
}

// ResumeFromCoCreate ends stage cocreation: injects the resulting next direction as an
// intervention and resumes creation.
// After clearing the occupancy flag it reuses Continue's stopped-injection path (subject to the
// budget precondition).
// Note: returning early with the flag left set when draft is empty is deliberate (cocreation has
// not ended); the TUI's canStart() guard uses the same "non-empty" test as here, keeping that path
// unreachable so cocreating cannot leak.
func (h *Host) ResumeFromCoCreate(draft string) error {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return fmt.Errorf("draft is required")
	}
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("not in co-create")
	}
	h.cocreating = false
	h.mu.Unlock()

	// PauseForCoCreate's abort is asynchronous: wait for the engine loop to genuinely converge
	// before continuing, restoring the "truly stopped" premise that Continue after a manual pause
	// also relies on. The cocreation window is on a human-interaction timescale, so a short poll
	// is imperceptible.
	for h.engine.isRunning() {
		time.Sleep(20 * time.Millisecond)
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đồng sáng tác giai đoạn hoàn tất, đã tiêm hướng đi tiếp theo và khôi phục sáng tác", Level: "info"})
	return h.Continue(stagePlanPrefix + draft)
}

// CancelCoCreate abandons stage cocreation: clears the occupancy flag and stays paused (the user may continue from the input box or restart via Resume).
func (h *Host) CancelCoCreate() {
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return
	}
	h.cocreating = false
	h.mu.Unlock()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đã thoát đồng sáng tác giai đoạn, sáng tác vẫn tạm dừng (có thể viết tiếp trong ô nhập)", Level: "info"})
}

// ── Tools ──

func (h *Host) refreshWriterRestore() {
	if h.writerRestore != nil {
		h.writerRestore.Refresh(h.store)
	}
}

func (h *Host) CheckChapterRevisions() ([]int, error) {
	pending, err := h.store.Revisions.LoadPending()
	if err != nil {
		return nil, fmt.Errorf("đọc bản ghi khôi phục tu chỉnh: %w", err)
	}
	if pending != nil {
		chapters := make([]int, 0, len(pending.Items))
		for _, item := range pending.Items {
			chapters = append(chapters, item.Chapter)
		}
		return chapters, nil
	}
	changes, err := revision.Scan(h.store)
	if err != nil {
		return nil, err
	}
	return revision.ChangedChapters(changes), nil
}

func (h *Host) SyncChapterRevisions(ctx context.Context) (*revision.Result, error) {
	if err := h.acquireExclusive("đồng bộ tu chỉnh chương"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()
	defer h.releaseExclusive()

	pending, err := h.store.Revisions.LoadPending()
	if err != nil {
		return nil, err
	}
	if pending == nil {
		changes, err := revision.Scan(h.store)
		if err != nil {
			return nil, err
		}
		if len(changes) == 0 {
			return &revision.Result{}, nil
		}
		if err := h.budget.Refuse(); err != nil {
			return nil, err
		}
	}
	model := h.models.ForRoleWithFailover("editor", func(ev bootstrap.FailoverEvent) {
		slog.Warn("chuyển provider khi tu chỉnh chương", "module", "revision", "role", ev.Role,
			"reason", ev.Reason, "from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel), "err", ev.Err)
	})
	model = newUsageTrackedModel(model, "editor", h.usage.Record)
	service := revision.NewService(h.store, model, h.bundle.Prompts.RevisionAnalyze, h.styleStats)
	return service.Sync(ctx)
}

func (h *Host) requireCleanChapters() error {
	chapters, err := h.CheckChapterRevisions()
	if err != nil {
		return fmt.Errorf("kiểm tra tu chỉnh chương từ bên ngoài: %w", err)
	}
	if len(chapters) > 0 {
		return fmt.Errorf("phát hiện chính văn chương đã bị sửa từ bên ngoài: %v; hãy chạy /sync trước", chapters)
	}
	return nil
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ImportFrom starts one semantic-compilation import of an external novel: ingest → segment → analyze → synthesize → publish.
// The model adjudicates only open semantics (boundaries / facts / synthesis) while Go owns
// coordinates, coverage and idempotency; it is mutually exclusive with a running Engine, and
// AdvanceHold decides after the import whether writing continues.
// The returned event channel is closed by imp.Run; the caller consumes it (dropping on a full buffer so the pipeline goroutine never blocks).
func (h *Host) ImportFrom(ctx context.Context, opts imp.Options) (<-chan imp.Event, error) {
	// The budget precondition follows the same discipline as Start/Resume/Continue: an import is
	// model calls end to end, so it must not start on an exhausted budget (§13.1 "joins the
	// existing budget sentinel").
	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := h.acquireExclusive("nhập liệu"); err != nil {
		return nil, err
	}
	// Register the cancel function: a hard budget stop or manual pause cancels the import's own
	// context through abortWithEvent (otherwise the sentinel would only pause an Engine that is not
	// running and the import would keep burning money).
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	deps := imp.Deps{
		Store:         h.store,
		CommitChapter: tools.NewCommitChapterTool(h.store, h.styleStats),
		Segment:       h.importCaller("segment"),
		Analyze:       h.importCaller("analyze"),
		Synthesize:    h.importCaller("synthesize"),
		Prompts: imp.Prompts{
			Segment:    h.bundle.Prompts.ImportSegment,
			Analyze:    h.bundle.Prompts.ImportAnalyze,
			Synthesize: h.bundle.Prompts.ImportSynthesize,
			Range:      h.bundle.Prompts.ImportRange,
		},
	}
	ch, err := imp.Run(ctx, deps, opts)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return h.superviseImport(ch, opts), nil
}

// ImportResumeHint returns a one-line hint about an unfinished import (empty when there is none), so the TUI can tell the user at startup (RFC §18.2).
// It is called once at startup only: it recomputes each workspace artifact's InputDigest and is unsuitable for snapshot polling.
func (h *Host) ImportResumeHint() string {
	return imp.ResumeSummary(h.store)
}

// importCaller resolves the model tier for one import semantic function (RFC §13.1): when the
// roles config has import_<fn> that tier is used (and usage is billed to that role), otherwise it
// falls to architect. This is call configuration and changes no semantic contract.
func (h *Host) importCaller(fn string) imp.Caller {
	role := "import_" + fn
	if _, _, explicit := h.models.CurrentSelection(role); !explicit {
		role = "architect"
	}
	model := h.models.ForRoleWithFailover(role, func(ev bootstrap.FailoverEvent) {
		slog.Warn("chuyển provider khi nhập liệu", "module", "import", "role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err)
	})
	model = newUsageTrackedModel(model, role, h.usage.Record)
	return imp.Caller{Model: model, Runtime: h.importModelRuntime(role, model)}
}

// importModelRuntime probes the call capabilities of the selected tier's role model for imp's dual budgets / thinking adaptation (RFC §13/§21).
// Fields that fail to probe stay zero, and imp falls back to conservative defaults so it still runs correctly without capability information.
// Structured output is read from live model facts by imp's llmcontract before each request, not cached again in Runtime.
func (h *Host) importModelRuntime(role string, model agentcore.ChatModel) imp.ModelRuntime {
	var rt imp.ModelRuntime
	provider, name, _ := h.models.CurrentSelection(role)
	if name == "" {
		name = bootstrap.ModelName(model)
		provider = bootstrap.ModelProvider(model)
	}
	// Context / completion limits: the registry is the only trustworthy source (the wrapped model's Info() carries no window).
	rt.ContextTokens, _ = h.cfg.ResolveContextWindow(provider, name)
	if entry, ok := modelreg.DefaultRegistry().Resolve(name); ok {
		rt.MaxOutputTokens = entry.MaxTokens
	}
	// thinking: resolved from the role's reasoning effort and the model's capability; withheld when unsupported (the same policy as the arbiter).
	if level, err := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role)); err == nil {
		if resolved, ok := agents.ResolveThinkingForModel(model, level); ok {
			rt.Thinking = resolved
		}
	}
	return rt
}

// Simulate reads the simulate directory and generates or incrementally updates an imitation profile.
func (h *Host) Simulate(ctx context.Context) (<-chan sim.Event, error) {
	if err := h.acquireExclusive("sinh hồ sơ mô phỏng"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	wd, err := os.Getwd()
	if err != nil {
		h.releaseExclusive()
		return nil, fmt.Errorf("get working dir: %w", err)
	}
	deps := sim.Deps{
		Store: h.store,
		LLM:   h.models.ForRole("architect"),
		Prompts: sim.Prompts{
			Source: h.bundle.Prompts.SimulationSource,
			Merge:  h.bundle.Prompts.SimulationMerge,
		},
	}
	ch, err := sim.Run(ctx, deps, sim.Options{SourceDir: filepath.Join(wd, "simulate")})
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// ImportSimulationProfile imports a previously generated imitation profile.
func (h *Host) ImportSimulationProfile(ctx context.Context, path string) (<-chan sim.Event, error) {
	if err := h.acquireExclusive("nhập hồ sơ mô phỏng"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()
	ch, err := sim.RunImport(ctx, h.store, path)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// acquireExclusive atomically claims the background exclusive-job slot (import/simulate/revision):
// it refuses while the Engine runs, inside a stage-cocreation window, or when another exclusive job
// is already running. Success registers the claim, and the job must call releaseExclusive when it
// ends — otherwise two imports, or an import plus an imitation, race on the same state. This closes
// the gap where only ==running/cocreating were checked and the job itself was never registered.
func (h *Host) acquireExclusive(action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closing:
		return fmt.Errorf("Host đang đóng, không thể %s", action)
	// engine.isRunning() must be checked: Abort sets lifecycle=paused first and then waits
	// asynchronously for the goroutine to exit, and within that window lifecycle is no longer running
	// while the engine may still be writing to the store (the same discipline as the start gate).
	case h.lifecycle == lifecycleRunning || h.engine.isRunning():
		return fmt.Errorf("engine sáng tác đang chạy hoặc đang dừng, hãy chờ rồi %s sau", action)
	case h.cocreating:
		return fmt.Errorf("đang đồng sáng tác giai đoạn, hãy kết thúc đồng sáng tác rồi %s sau", action)
	case h.exclusive != "":
		return fmt.Errorf("%s đang chạy, hãy hoàn tất rồi %s sau", h.exclusive, action)
	}
	h.exclusive = action
	return nil
}

// releaseExclusive releases the background exclusive-job slot (along with the registered cancel function).
func (h *Host) releaseExclusive() {
	h.mu.Lock()
	cancel := h.exclusiveCancel
	h.exclusive = ""
	h.exclusiveCancel = nil
	h.mu.Unlock()
	if cancel != nil {
		cancel() // Job finished: release the derived context; harmless for a runner that already exited.
	}
}

// superviseExclusive forwards exclusive-job events and releases the slot when the channel closes (the job ends).
func superviseExclusive[T any](h *Host, src <-chan T) <-chan T {
	out := make(chan T, 32)
	if !h.launchAsync(func() {
		defer close(out)
		defer h.releaseExclusive()
		for ev := range src {
			select {
			case out <- ev:
			case <-h.runCtx.Done():
				// Keep draining the source channel after close so the producer cannot block on a terminal event and fail to exit.
				for range src {
				}
				return
			}
		}
	}) {
		close(out)
		h.releaseExclusive()
	}
	return out
}

// superviseImport is the sole owner of "whether to relay after the import": it forwards import
// events, releases the exclusive slot on success, then decides and performs the relay, and finally
// writes the real relay outcome into the StageDone event's Continued field. The TUI renders only from
// that, no longer guessing run state from a local --continue flag (eliminating the timing races that
// came from Runner/Host/TUI each interpreting it separately).
func (h *Host) superviseImport(src <-chan imp.Event, opts imp.Options) <-chan imp.Event {
	out := make(chan imp.Event, 32)
	if !h.launchAsync(func() {
		defer close(out)
		released := false
		release := func() {
			if !released {
				released = true
				h.releaseExclusive()
			}
		}
		defer release()
		for ev := range src {
			if ev.Stage == imp.StageDone {
				release() // Release the exclusive slot first so the relaying startEngine can pass the exclusive gate.
				ev.Continued = h.continueAfterImport(opts)
			}
			select {
			case out <- ev:
			case <-h.runCtx.Done():
				for range src {
				}
				return
			}
		}
	}) {
		close(out)
		h.releaseExclusive()
	}
	return out
}

// launchAsync registers a background task for the Host's lifetime. closing and WaitGroup.Add share
// one mutex, guaranteeing no new Add appears after Close has begun Wait.
func (h *Host) launchAsync(fn func()) bool {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return false
	}
	h.asyncWG.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.asyncWG.Done()
		fn()
	}()
	return true
}

// runAsync reuses the Host's existing background-task registration while handing business errors back to the caller.
func (h *Host) runAsync(fn func() error) (error, bool) {
	result := make(chan error, 1)
	if !h.launchAsync(func() { result <- fn() }) {
		return nil, false
	}
	return <-result, true
}

// continueAfterImport decides and performs the real automatic relay for --continue, reporting
// whether the Engine started.
// A valid relay intent comes from this call's opts or the workspace's persisted intent (covering
// recovery through a bare /import after a crash). Only auto advance mode relays: adaptive arc
// expansion takes on an open story, or a completed one is wrapped up; review leaves it to the
// user's /next.
func (h *Host) continueAfterImport(opts imp.Options) bool {
	want := opts.ContinueAfter
	if !want {
		in, err := imp.OpenWorkspace(h.store.Dir()).LoadIntent()
		if err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "Nhập liệu đã xong, nhưng đọc ý định tự động tiếp sức thất bại: " + err.Error()})
		} else if in != nil {
			want = in.ContinueAfterImport
		}
	}
	if !want {
		return false
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil || meta == nil {
		slog.Warn("đọc RunMeta cho tự động tiếp sức khi nhập liệu thất bại", "module", "host", "err", err)
		return false
	}
	if meta.AdvanceMode != domain.ChapterAdvanceAuto {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
			Summary: "Nhập liệu hoàn tất; hiện là chế độ nghiệm thu từng chương, nhập viết tiếp hoặc /next để tiếp sức"})
		return false
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info", Summary: "Nhập liệu hoàn tất, tự động tiếp sức viết tiếp"})
	if !h.startEngine(nil) {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "Khởi động tự động tiếp sức thất bại, hãy nhập chỉ thị viết tiếp để khôi phục thủ công"})
		return false
	}
	return true
}

// Export writes completed chapters to an external file (TXT only for now).
//
// Unlike ImportFrom, export is read-only (it touches neither Progress nor Checkpoint), so it
// **does not require the Engine to stop** — a "current draft" can be exported at any point mid-writing.
// It reads only a consistent snapshot of Progress.CompletedChapters + the final chapter drafts +
// the outline + the premise.
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}
