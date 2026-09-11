package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Setup initialises the default slog logger.
// w is the log sink and level is the minimum log level.
func Setup(w io.Writer, level slog.Level) {
	slog.SetDefault(slog.New(newTextHandler(w, level)))
}

func newTextHandler(w io.Writer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Keep the date, milliseconds and time zone so that when logs are appended
			// across processes they still line up precisely with the code version and session.
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02T15:04:05.000Z07:00"))
			}
			return a
		},
	})
}

func newSessionLogger(w io.Writer, level slog.Level, sessionAttrs ...slog.Attr) (*slog.Logger, string) {
	sessionID := fmt.Sprintf("%s-p%d", time.Now().Format("20060102T150405.000Z0700"), os.Getpid())
	attrs := make([]slog.Attr, 0, len(sessionAttrs)+1)
	attrs = append(attrs, slog.String("session", sessionID))
	attrs = append(attrs, sessionAttrs...)
	handler := newTextHandler(w, level).WithAttrs(attrs)
	return slog.New(handler), sessionID
}

// FileLogger returns a dedicated logger writing to outputDir/logs/filename plus a cleanup
// function, for subsystems that need their own log file (such as the import pipeline).
// On failure to open, it falls back to the default logger without interrupting the work, but
// the error must be returned to the caller for presentation to the user — otherwise the UI
// points the user at a log file that does not exist.
func FileLogger(outputDir, filename string) (*slog.Logger, func(), error) {
	f, err := openLogFile(outputDir, filename)
	if err != nil {
		return slog.Default(), func() {}, err
	}
	logger, sessionID := newSessionLogger(f, slog.LevelDebug)
	logger.Info("bắt đầu phiên log", "module", "logger", "session_id", sessionID)
	return logger, func() {
		logger.Info("kết thúc phiên log", "module", "logger", "session_id", sessionID)
		_ = f.Close()
	}, nil
}

// SetupFile initialises the default logger to a file and returns a cleanup function.
// When alsoStderr is true it writes to stderr as well.
// It returns an error when the log directory or file cannot be opened, and the caller must
// handle that explicitly; switching to io.Discard and carrying on is forbidden, because that
// loses the entire run log exactly when debugging matters most.
func SetupFile(outputDir, filename string, alsoStderr bool, sessionAttrs ...slog.Attr) (func(), error) {
	f, err := openLogFile(outputDir, filename)
	if err != nil {
		return nil, err
	}

	var w io.Writer = f
	if alsoStderr {
		w = io.MultiWriter(os.Stderr, f)
	}
	previous := slog.Default()
	logger, sessionID := newSessionLogger(w, slog.LevelDebug, sessionAttrs...)
	slog.SetDefault(logger)
	logger.Info("bắt đầu phiên log", "module", "logger", "session_id", sessionID)

	return func() {
		logger.Info("kết thúc phiên log", "module", "logger", "session_id", sessionID)
		slog.SetDefault(previous)
		_ = f.Close()
	}, nil
}

func openLogFile(outputDir, filename string) (*os.File, error) {
	logPath := filepath.Join(outputDir, "logs", filename)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory %q: %w", filepath.Dir(logPath), err)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", logPath, err)
	}
	return f, nil
}
