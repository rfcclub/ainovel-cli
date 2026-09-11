package eval

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
)

// RunOptions controls a single case run.
type RunOptions struct {
	OutputDir string        // Isolated output directory (required)
	Timeout   time.Duration // Wall-clock cap per case; 0 means unlimited
	Progress  io.Writer     // Progress line sink (optional; nil prints nothing)
}

// RunCase drives one case: assemble host -> start -> advance up to the chapter cap -> Abort at the mark.
// The bundle has already had any variant override applied by the caller. A returned error is a
// "runtime error" (the basis for a hard fail); a normal finish or a normal stop both return nil.
//
// RunCase takes exclusive ownership of and resets OutputDir: StartPrepared only resets
// progress/checkpoints and does not clear artefacts such as chapters/ or foundation, so reusing an
// old directory would let leftover output pollute diag and novel_context. It is therefore emptied
// before the run to guarantee isolation.
func RunCase(cfg bootstrap.Config, bundle assets.Bundle, c Case, opts RunOptions) error {
	if strings.TrimSpace(opts.OutputDir) == "" {
		return fmt.Errorf("RunCase: thiếu OutputDir")
	}
	if err := os.RemoveAll(opts.OutputDir); err != nil {
		return fmt.Errorf("dọn thư mục đầu ra: %w", err)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("tạo thư mục đầu ra: %w", err)
	}
	cfg.OutputDir = opts.OutputDir
	if c.Style != "" {
		cfg.Style = c.Style
	}

	eng, err := host.New(cfg, bundle, host.WithFileLog("headless.log", false))
	if err != nil {
		return fmt.Errorf("lắp ghép host: %w", err)
	}
	defer eng.Close()
	if logErr := eng.FileLogError(); logErr != nil {
		return fmt.Errorf("không dùng được log ghi file khi đánh giá: %w", logErr)
	}

	prompt, err := startup.PrepareQuick(c.Prompt)
	if err != nil {
		return err
	}
	if err := eng.PrepareUserRules(prompt); err != nil {
		return fmt.Errorf("chuẩn bị quy tắc người dùng: %w", err)
	}
	if err := eng.StartPrepared(prompt); err != nil {
		return fmt.Errorf("khởi động: %w", err)
	}

	return drive(eng, c.MaxChapters, opts)
}

// driveEngine is the minimal engine interface drive consumes (*host.Host satisfies it naturally).
// It is extracted so the drain-to-Done discipline can be covered by a deterministic test — this
// concurrency logic once hit a send-on-closed-channel trap.
type driveEngine interface {
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Snapshot() host.UISnapshot
	Abort() bool
}

// drive consumes the engine event stream, aborting at the chapter cap or on timeout, and waits for
// Done to wrap up.
//
// The key discipline: whether it finishes normally, stops at the chapter cap or times out, it must
// drain all the way to Done before returning. The host's background waitDone sends to done once,
// while eng.Close() (RunCase's defer) closes done — returning early makes Close race waitDone's
// send against the close and panic (send on closed channel). headless likewise relies on "Done
// first
// then Close". It must also drain Events and Stream so the engine is never blocked.
func drive(eng driveEngine, maxChapters int, opts RunOptions) error {
	var timeoutCh <-chan time.Time
	if opts.Timeout > 0 {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		timeoutCh = t.C
	}

	aborted, timedOut := false, false
	// finish is called after draining to Done (or the channel closing): it returns an error on timeout
// and otherwise finishes normally.
	finish := func() error {
		if timedOut {
			return fmt.Errorf("chạy quá thời gian (%s)", opts.Timeout)
		}
		return nil
	}
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				return finish()
			}
			if opts.Progress != nil && strings.TrimSpace(ev.Summary) != "" {
				fmt.Fprintf(opts.Progress, "    [%s] %s\n", ev.Category, ev.Summary)
			}
			if !aborted && capReached(eng.Snapshot(), maxChapters) {
				eng.Abort()
				aborted = true
				timeoutCh = nil // The stop condition was reached; move to normal wrap-up and drop the timeout constraint (so a successful stop is not misread as a timeout).
			}
		case <-eng.Stream():
			// Drain the streaming deltas without consuming the content — eval does not care about the prose
// stream, only about the facts landed on disk.
		case _, ok := <-eng.Done():
			if !ok {
				return finish()
			}
			return finish()
		case <-timeoutCh:
			eng.Abort() // aborted is necessarily false here (a cap stop sets timeoutCh to nil).
			aborted, timedOut = true, true
			timeoutCh = nil // Disable the timer, keep draining until Done, then let finish return the timeout error.
		}
	}
}

// capReached reports whether the stop condition was reached. For maxChapters>0 it counts completed
// chapters; for <=0 the case is treated as "planning-only",
// stopping once planning completes (writing entered or complete reached).
func capReached(snap host.UISnapshot, maxChapters int) bool {
	if maxChapters <= 0 {
		return snap.Phase == string(domain.PhaseWriting) || snap.Phase == string(domain.PhaseComplete)
	}
	return snap.CompletedCount >= maxChapters
}
