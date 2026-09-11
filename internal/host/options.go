package host

import "log/slog"

type newOptions struct {
	logFile       string
	logAlsoStderr bool
	logAttrs      []slog.Attr
}

// NewOption configures Host construction; runtime resources are still owned by the Host.
type NewOption func(*newOptions)

// WithFileLog makes the Host hold a runtime log session. The log opens only after the novel directory
// lease is acquired and closes once every Host shutdown log has been written. On a failed open it keeps
// using the current process logger, and the caller must handle that error explicitly through
// FileLogError.
func WithFileLog(filename string, alsoStderr bool, attrs ...slog.Attr) NewOption {
	return func(opts *newOptions) {
		opts.logFile = filename
		opts.logAlsoStderr = alsoStderr
		opts.logAttrs = append([]slog.Attr(nil), attrs...)
	}
}
