package host

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// Budget state machine: monotonic, each transition fires exactly one side effect, never goes back.
// Raising the budget is a fresh authorisation from the user — a config change and a restart or new Host
// instance — so state never rolls back within one instance.
const (
	budgetNormal      int32 = iota // Below the warning watermark
	budgetWarned                   // Warning emitted, limit not crossed
	budgetStopPending              // Limit crossed, waiting to stop at a subagent boundary
	budgetStopped                  // Stop already executed
)

// BudgetSentinel watches accumulated cost and enforces the user's budget policy (the config budget block).
//
// Constitutional position (architecture.md §8.3/§10): it does not evaluate model behaviour — crossing
// the line and stopping is equivalent to the user hitting Abort at that moment, with the Host merely
// executing a pre-signed instruction. It affects control flow, so it is not an observer but a Host
// policy component on a par with flow.Dispatcher; the Route/tool layers are unaware of it.
//
// Stop timing: at subagent boundaries by default (the Host calls HandleBoundary synchronously) so an
// in-flight chapter is not wasted; with hardStop=true it stops the moment the line is crossed. Boundary
// handling precedes flow.Dispatcher's next dispatch, and the Route/tool layers are unaware of the
// budget.
type BudgetSentinel struct {
	limit     float64
	warnRatio float64
	hardStop  bool

	costNow func() float64              // Current cumulative cost (wraps usage.Totals; injectable test stub)
	abort   func(reason string)         // Host stop wrapper (emits an event with the reason)
	report  func(level, summary string) // Warning sink (emitEvent + notify, injected by the Host)

	state atomic.Int32

	// Billing blind-spot detection: for a model with no registry price and no self-reported cost, each
	// accounting entry adds $0 and the budget silently stops working. The test is "several consecutive
	// zero increments" rather than total==0 — the latter misses a /model switch to an unpriced model
	// mid-run (total sits at a non-zero history value but stops growing). Free models trigger it too, and
	// "the budget will not fire" holds for them just the same.
	lastTotal   atomic.Uint64 // math.Float64bits(cumulative cost from the last callback)
	zeroStreak  atomic.Int32
	blindWarned atomic.Bool
}

// blindZeroStreak is how many consecutive zero-increment entries trigger the warning. A normally priced
// model always increments by more than 0 (cost accumulates as an unrounded float), so 5 is only there to
// dodge extreme spikes; it is not a tunable policy threshold.
const blindZeroStreak = 5

// NewBudgetSentinel creates the budget sentinel; it returns nil when the policy is disabled (all methods are nil-safe).
func NewBudgetSentinel(cfg bootstrap.BudgetConfig, costNow func() float64, abort func(reason string), report func(level, summary string)) *BudgetSentinel {
	if !cfg.Enabled() {
		return nil
	}
	return &BudgetSentinel{
		limit:     cfg.BookUSD,
		warnRatio: cfg.WarnRatio,
		hardStop:  cfg.HardStop,
		costNow:   costNow,
		abort:     abort,
		report:    report,
	}
}

// OnCost is called by UsageTracker after each accounting entry with the latest cumulative cost (outside
// the lock). One callback may cross two levels at once (normal→warned→stopPending), firing each side
// effect once.
func (s *BudgetSentinel) OnCost(total float64) {
	if s == nil {
		return
	}
	if prev := s.lastTotal.Swap(math.Float64bits(total)); total == math.Float64frombits(prev) {
		if s.zeroStreak.Add(1) >= blindZeroStreak && s.blindWarned.CompareAndSwap(false, true) {
			s.report("warn", fmt.Sprintf("Điểm mù ngân sách: vẫn ghi sổ liên tục nhưng chi phí tích lũy dừng ở $%.2f không tăng nữa (model hiện tại không có giá trong registry và provider không tự báo cost, hoặc là model miễn phí) — giới hạn ngân sách sẽ không kích hoạt", total))
		}
	} else {
		s.zeroStreak.Store(0)
	}
	if total >= s.limit*s.warnRatio && s.state.CompareAndSwap(budgetNormal, budgetWarned) {
		s.report("warn", fmt.Sprintf("Cảnh báo ngân sách: đã chi $%.2f, đạt %.0f%% của ngân sách $%.2f", total, s.warnRatio*100, s.limit))
	}
	if total >= s.limit && s.state.CompareAndSwap(budgetWarned, budgetStopPending) {
		if s.hardStop {
			s.report("error", fmt.Sprintf("Hết ngân sách: đã chi $%.2f, vượt ngân sách $%.2f, dừng ngay", total, s.limit))
			s.stop(total)
			return
		}
		s.report("error", fmt.Sprintf("Hết ngân sách: đã chi $%.2f, vượt ngân sách $%.2f, sẽ dừng sau khi nhiệm vụ subagent hiện tại kết thúc", total, s.limit))
	}
}

// HandleEvent performs a pending stop at a subagent boundary. Subscription must precede the Dispatcher.
// IsError is not skipped — an error return is still a boundary, and a stop must not be delayed because a
// subagent failed.
func (s *BudgetSentinel) HandleEvent(ev agentcore.Event) {
	if s == nil {
		return
	}
	if ev.Type != agentcore.EventToolExecEnd || ev.Tool != "subagent" {
		return
	}
	s.HandleBoundary()
}

func (s *BudgetSentinel) HandleBoundary() bool {
	if s == nil || s.state.Load() != budgetStopPending {
		return false
	}
	s.stop(s.costNow())
	return true
}

func (s *BudgetSentinel) stop(total float64) {
	if s.state.CompareAndSwap(budgetStopPending, budgetStopped) {
		s.abort(fmt.Sprintf("Dừng do ngân sách: đã chi $%.2f, vượt ngân sách $%.2f; nâng budget.book_usd lên rồi có thể chạy tiếp", total, s.limit))
	}
}

// Refuse is the start precondition: it returns a refusal error when the budget is exhausted (called on the Start/Resume/Continue recovery paths).
// Raising the budget is a fresh authorisation, so Refuse lets it through naturally under the new config.
func (s *BudgetSentinel) Refuse() error {
	if s == nil {
		return nil
	}
	if cost := s.costNow(); cost >= s.limit {
		return fmt.Errorf("sách này đã chi $%.2f, đạt giới hạn ngân sách $%.2f; hãy nâng budget.book_usd trong cấu hình rồi thử lại", cost, s.limit)
	}
	return nil
}

// Limit returns the budget cap (for UI display); 0 when disabled.
func (s *BudgetSentinel) Limit() float64 {
	if s == nil {
		return 0
	}
	return s.limit
}
