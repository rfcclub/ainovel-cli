// Package notify provides the unattended alerting channel.
//
// Constitutional positioning (architecture.md §2.3): a purely observational action —
// alerting never enters the control flow (it does not retry, re-dispatch or stop anything);
// it only shouts the events already shown in the TUI beyond the screen. Send runs
// asynchronously, never blocks the Host, and logs failures to slog only.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notification carries every fact about one alert.
type Notification struct {
	Kind  string `json:"kind"`  // Stable event name returned by Kinds
	Level string `json:"level"` // info / warn / error
	Title string `json:"title"`
	Body  string `json:"body"`
}

const (
	KindRunEnd        = "run_end"
	KindBudget        = "budget"
	KindAdvanceGate   = "advance_gate"
	KindStopGuard     = "stop_guard"
	KindPlanStart     = "plan_start"
	KindDeadlock      = "deadlock"
	KindWorkerFailure = "worker_failure"
)

// Kinds returns every event name usable in notify.events for this version.
// This is the single source of truth for the notification event contract.
func Kinds() []string {
	return []string{
		KindRunEnd,
		KindBudget,
		KindAdvanceGate,
		KindStopGuard,
		KindPlanStart,
		KindDeadlock,
		KindWorkerFailure,
	}
}

func IsKnownKind(kind string) bool {
	for _, known := range Kinds() {
		if kind == known {
			return true
		}
	}
	return false
}

// Notifier dispatches notifications per configuration. The zero value is unusable and must
// be created through New; it is nil-safe (Send becomes a no-op).
type Notifier struct {
	command string          // When non-empty, replaces the system channel (phone push goes here)
	events  map[string]bool // nil = all kinds allowed
	timeout time.Duration
}

// New creates a Notifier. An empty command uses the built-in system channel (Windows toast /
// macOS osascript / Linux notify-send); a non-empty events list allows only the listed kinds.
func New(command string, events []string) *Notifier {
	n := &Notifier{command: strings.TrimSpace(command), timeout: 10 * time.Second}
	if len(events) > 0 {
		n.events = make(map[string]bool, len(events))
		for _, ev := range events {
			n.events[ev] = true
		}
	}
	return n
}

// Send dispatches one notification asynchronously. Filtering, execution and failure
// handling never affect the caller.
func (n *Notifier) Send(nt Notification) {
	if !n.allows(nt.Kind) {
		return
	}
	go n.deliver(nt)
}

// allows reports whether this kind passes (blocked for a nil Notifier or when absent from events).
func (n *Notifier) allows(kind string) bool {
	if n == nil {
		return false
	}
	return n.events == nil || n.events[kind]
}

// deliver performs one send synchronously and records failures; Send calls it in a goroutine.
func (n *Notifier) deliver(nt Notification) {
	if err := n.deliverError(nt); err != nil {
		slog.Warn("gửi thông báo thất bại", "module", "notify", "kind", nt.Kind, "err", err)
	}
}

// deliverError performs one send synchronously and returns the raw error. Send calls deliver
// in a goroutine to record failures; tests call this method directly so the error is not
// masked by a secondary symptom.
func (n *Notifier) deliverError(nt Notification) error {
	ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
	defer cancel()

	if n.command != "" {
		return runCommand(ctx, n.command, nt)
	}
	return runSystem(ctx, nt)
}

// runCommand executes the user-configured command: fields arrive as environment variables
// (a one-line curl needs no dependency and carries no injection risk) and the full JSON is
// also written to stdin (complex dispatch setups parse it themselves). A timeout kills hard
// via ctx.
func runCommand(ctx context.Context, command string, nt Notification) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		powershell, err := findPowerShell()
		if err != nil {
			return err
		}
		cmd = exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Env = notificationEnv(nt)
	payload, _ := json.Marshal(nt)
	cmd.Stdin = strings.NewReader(string(payload))
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("lệnh thông báo quá thời gian: %w", ctxErr)
		}
		return err
	}
	return nil
}

func notificationEnv(nt Notification) []string {
	return append(os.Environ(),
		"NOTIFY_KIND="+nt.Kind,
		"NOTIFY_LEVEL="+nt.Level,
		"NOTIFY_TITLE="+nt.Title,
		"NOTIFY_BODY="+nt.Body,
	)
}

// runSystem is the built-in desktop notification: it only covers the "person at the computer"
// case and degrades silently when the command is missing.
func runSystem(ctx context.Context, nt Notification) error {
	switch runtime.GOOS {
	case "windows":
		return runWindowsNotification(ctx, nt)
	case "darwin":
		script := "display notification " + appleScriptString(nt.Body) + " with title " + appleScriptString(nt.Title)
		return exec.CommandContext(ctx, "osascript", "-e", script).Run()
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			slog.Info("thông báo hạ cấp thành log (không có notify-send)", "module", "notify", "title", nt.Title, "body", nt.Body)
			return nil
		}
		return exec.CommandContext(ctx, "notify-send", nt.Title, nt.Body).Run()
	default:
		slog.Info("thông báo hạ cấp thành log (nền tảng không có kênh system)", "module", "notify", "title", nt.Title, "body", nt.Body)
		return nil
	}
}

// runWindowsNotification uses the system PowerShell plus a WinForms NotifyIcon.
// Windows 10/11 shows the toast in the top-right corner and folds it into the system
// notification experience; it needs no module install and no app registration.
// or carry an extra binary. Callers already run asynchronously, so this brief keep-alive
// exists only to let the system receive the toast.
func runWindowsNotification(ctx context.Context, nt Notification) error {
	powershell, err := findPowerShell()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", windowsNotificationScript)
	cmd.Env = notificationEnv(nt)
	return cmd.Run()
}

func findPowerShell() (string, error) {
	// PowerShell 7 is preferred: on GitHub Windows runners and in modern Windows
	// environments pwsh handles redirected stdin more reliably, with Windows PowerShell
	// 5.1 kept only as a compatibility fallback.
	for _, name := range []string{"pwsh.exe", "pwsh", "powershell.exe", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("thông báo trên Windows cần PowerShell, nhưng hệ thống không tìm thấy powershell.exe hoặc pwsh.exe")
}

const windowsNotificationScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$notify = New-Object System.Windows.Forms.NotifyIcon
$notify.Icon = [System.Drawing.SystemIcons]::Information
$notify.BalloonTipTitle = $env:NOTIFY_TITLE
$notify.BalloonTipText = $env:NOTIFY_BODY
$notify.BalloonTipIcon = switch ($env:NOTIFY_LEVEL) {
  'error' { [System.Windows.Forms.ToolTipIcon]::Error; break }
  'warn'  { [System.Windows.Forms.ToolTipIcon]::Warning; break }
  default { [System.Windows.Forms.ToolTipIcon]::Info }
}
$notify.Visible = $true
$notify.ShowBalloonTip(4000)
Start-Sleep -Milliseconds 4500
$notify.Dispose()`

// appleScriptString wraps arbitrary text as an AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
