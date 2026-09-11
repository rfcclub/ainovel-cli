package tui

import (
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
	buildversion "github.com/voocel/ainovel-cli/internal/version"
)

// Run starts the TUI.
// Layering convention for startup modes:
// 1. quick mode and cocreation mode belong to "startup orchestration";
// 2. a formal creation session enters host.Host;
// 3. a future shared mode such as "continue an existing novel" lands uniformly in internal/entry/startup.
func Run(cfg bootstrap.Config, bundle assets.Bundle, build buildversion.Info) error {
	rt, err := host.New(cfg, bundle, host.WithFileLog("tui.log", false,
		slog.String("version", build.Version),
		slog.String("commit", build.Commit),
		slog.String("built", build.Date),
	))
	if err != nil {
		return err
	}
	defer rt.Close()

	m := NewModel(rt, build.Version)
	if logErr := rt.FileLogError(); logErr != nil {
		logWarning := fmt.Errorf("không dùng được log ghi file, đã tiếp tục dùng log ra terminal: %w", logErr)
		m.err = logWarning
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: logWarning.Error(), Detail: logWarning.Error(),
		})
	}
	// Mouse reporting is not enabled globally at startup: the welcome page has no use for the mouse and disabling reporting
	// preserves the terminal's native drag-select copy. enterRunning enables reporting on entering the creation workbench
	// (modeRunning) to support click-to-switch panes, the scroll wheel and sidebar dragging.
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
