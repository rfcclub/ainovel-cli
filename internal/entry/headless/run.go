package headless

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Options struct {
	Prompt string
	Stdout io.Writer
	Stderr io.Writer
}

// Run drives the session kernel in headless mode, consuming Engine events and streaming output directly.
// A future shared startup path such as "continue an existing novel" should not be piled in here but should first land in
// internal/entry/startup, with the headless entry point calling it.
func Run(cfg bootstrap.Config, bundle assets.Bundle, opts Options) error {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	eng, err := host.New(cfg, bundle, host.WithFileLog("headless.log", false))
	if err != nil {
		return err
	}
	defer eng.Close()
	if logErr := eng.FileLogError(); logErr != nil {
		fmt.Fprintf(stderr, "Cảnh báo: không dùng được log ghi file, tiếp tục dùng log ra terminal: %v\n", logErr)
	}
	// A redacted diagnosis is written when the run ends or returns an error, so headless users can attach it to an issue.
	// (A hang from an external kill never reaches the defer and still needs a manual /diag in the TUI.)
	defer func() {
		if _, err := diag.Export(store.NewStore(eng.Dir())); err != nil {
			fmt.Fprintf(stderr, "Cảnh báo: xuất báo cáo chẩn đoán thất bại: %v\n", err)
		}
	}()

	prompt := strings.TrimSpace(opts.Prompt)
	if prompt != "" {
		prompt, err = startup.PrepareQuick(prompt)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "headless khởi động: %s\n", eng.Dir())
		// The startup side deterministically generates this book's user-rules snapshot (normalised from the raw prompt) before StartPrepared.
		if err := eng.PrepareUserRules(prompt); err != nil {
			return err
		}
		if err := eng.StartPrepared(prompt); err != nil {
			return err
		}
	} else {
		items, err := eng.ReplayQueue(0)
		if err != nil {
			return err
		}
		replayQueue(items, stderr)
		label, err := eng.Resume()
		if err != nil {
			return err
		}
		if label == "" {
			return fmt.Errorf("chế độ headless cần --prompt, hoặc thư mục đầu ra %q đã có phiên khôi phục được", eng.Dir())
		}
		fmt.Fprintf(stderr, "headless khôi phục: %s (%s)\n", eng.Dir(), label)
		return consume(eng, stdout, stderr, false)
	}

	return consume(eng, stdout, stderr, false)
}

func consume(eng *host.Host, stdout, stderr io.Writer, roundHasContent bool) error {
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				return nil
			}
			writeEvent(stderr, ev)
		case delta, ok := <-eng.Stream():
			if !ok {
				continue
			}
			if delta == host.StreamClearSentinel {
				if roundHasContent {
					if _, err := io.WriteString(stdout, "\n\n"); err != nil {
						return err
					}
					roundHasContent = false
				}
				continue
			}
			if delta == "" {
				continue
			}
			if _, err := io.WriteString(stdout, delta); err != nil {
				return err
			}
			roundHasContent = true
		case _, ok := <-eng.Done():
			if !ok {
				return nil
			}
			return drainPending(eng, stdout, stderr, roundHasContent)
		}
	}
}

func drainPending(eng *host.Host, stdout, stderr io.Writer, roundHasContent bool) error {
	for {
		select {
		case ev, ok := <-eng.Events():
			if ok {
				writeEvent(stderr, ev)
			}
		case delta, ok := <-eng.Stream():
			if !ok {
				continue
			}
			if delta == host.StreamClearSentinel {
				if roundHasContent {
					if _, err := io.WriteString(stdout, "\n\n"); err != nil {
						return err
					}
					roundHasContent = false
				}
				continue
			}
			if delta != "" {
				if _, err := io.WriteString(stdout, delta); err != nil {
					return err
				}
				roundHasContent = true
			}
		default:
			if roundHasContent {
				if _, err := io.WriteString(stdout, "\n"); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func writeEvent(w io.Writer, ev host.Event) {
	if w == nil || strings.TrimSpace(ev.Summary) == "" {
		return
	}
	ts := ev.Time.Format("15:04:05")
	if ts == "00:00:00" {
		ts = "--:--:--"
	}
	fmt.Fprintf(w, "[%s] [%s] %s\n", ts, ev.Category, ev.Summary)
}

func replayQueue(items []domain.RuntimeQueueItem, stderr io.Writer) {
	for _, item := range items {
		writeEvent(stderr, host.Event{
			Time:     item.Time,
			Category: item.Category,
			Summary:  item.Summary,
		})
	}
}
