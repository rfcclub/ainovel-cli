package imp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/llmretry"
	"github.com/voocel/litellm"
)

// callModel is the kernel's minimal dependency on a model, making it easy to inject a mock in tests.
type callModel interface {
	Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// errTruncated means the model stopped for length (a capacity error). It carries the raw text so the caller can decide between failing and salvaging a prefix (§9.5).
type errTruncated struct {
	Raw string
}

func (e *errTruncated) Error() string { return "đầu ra của model bị cắt do độ dài (stop=length)" }

// errSemantic means an output-layer failure that a re-query cannot fix; it carries the raw response so the runner
// can uniformly land a failures/ artifact (§14.2), shared by every semantic function.
type errSemantic struct {
	Raw string
	Err error
}

func (e *errSemantic) Error() string { return e.Err.Error() }
func (e *errSemantic) Unwrap() error { return e.Err }

// callProfile carries thinking and observability options, derived from the ModelRuntime the Host probed.
// The structured protocol is chosen independently by callStructured from model facts and the static Contract.
type callProfile struct {
	thinking agentcore.ThinkingLevel
	// notify is optional: it echoes request backoff retries / validation re-queries to the interface; silent when nil (§14.1).
	// A non-zero retryAt is the deadline of the next retry, from which the UI renders a per-second countdown (events carry only the deadline; remaining time is computed at render).
	notify func(msg string, retryAt time.Time)
	// progress is optional: it echoes the internal advance of long stages (segmentation block N/M, range digest N/M); silent when nil.
	// Segmentation/synthesis call the model per block / per range inside the function and one block can take minutes; without it the panel goes silent for the whole stretch and looks hung (§14.1).
	progress func(current, total int, msg string)
	// log is optional: the import-specific log (logs/import.log); nil falls back to the default logger.
	log *slog.Logger
}

func (p callProfile) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// step echoes one ordinary progress line (the internal advance of a long stage).
func (p callProfile) step(current, total int, format string, args ...any) {
	if p.progress != nil {
		p.progress(current, total, fmt.Sprintf(format, args...))
	}
}

// say echoes a long-call status. Retries can be silent for minutes (exponential backoff accumulating past two
// minutes), and without an echo the user would think it hung.
func (p callProfile) say(format string, args ...any) {
	p.sayRetry(time.Time{}, format, args...)
}

// sayRetry echoes a status carrying a retry deadline for the UI to count down.
func (p callProfile) sayRetry(retryAt time.Time, format string, args ...any) {
	if p.notify != nil {
		p.notify(fmt.Sprintf(format, args...), retryAt)
	}
}

// snippet compresses multi-line text into a one-line short summary for the interface: collapse whitespace, cut to max runes.
func snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// briefErr compresses an error into one short line for the interface (the full error chain still goes to logs and
// failure artifacts).
// The adapter's structured facts come first: on truncation, which error class and status code are preserved before
// the gateway message, which may be sacrificed.
func briefErr(err error) string {
	s := err.Error()
	if d := modelErrDetail(err); d != "" {
		s = d + "：" + s
	}
	return snippet(s, 100)
}

// errTypeLabels turns a litellm error category into a short, readable label.
var errTypeLabels = map[litellm.ErrorType]string{
	litellm.ErrorTypeAuth:            "xác thực thất bại",
	litellm.ErrorTypeRateLimit:       "giới hạn tần suất",
	litellm.ErrorTypeNetwork:         "lỗi mạng",
	litellm.ErrorTypeValidation:      "tham số yêu cầu không hợp lệ",
	litellm.ErrorTypeProvider:        "lỗi dịch vụ thượng nguồn",
	litellm.ErrorTypeTimeout:         "quá thời gian",
	litellm.ErrorTypeQuota:           "không đủ hạn mức",
	litellm.ErrorTypeModel:           "model không khả dụng",
	litellm.ErrorTypeInternal:        "lỗi nội bộ",
	litellm.ErrorTypeContextOverflow: "ngữ cảnh vượt giới hạn",
	litellm.ErrorTypeOverloaded:      "thượng nguồn quá tải",
	litellm.ErrorTypeContentFilter:   "bị chặn bởi lọc nội dung",
}

// modelErrDetail extracts the adapter's structured facts from the error chain (error class, HTTP status, provider,
// model).
// A gateway message is often just a vague "Provider returned error", which alone cannot tell a config mistake from
// an upstream fault or rate limiting; litellm carries these facts all along, they just never reach the Error()
// text. The agentcore adapter's Unwrap explicitly lets a caller that knows litellm use errors.As to get the raw
// error. It returns an empty string for a non-model-call error.
func modelErrDetail(err error) string {
	var le *litellm.LiteLLMError
	if !errors.As(err, &le) {
		return ""
	}
	parts := make([]string, 0, 4)
	if label := errTypeLabels[le.Type]; label != "" {
		parts = append(parts, label)
	}
	if le.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", le.StatusCode))
	}
	if le.Provider != "" {
		parts = append(parts, le.Provider)
	}
	if le.Model != "" {
		parts = append(parts, le.Model)
	}
	return strings.Join(parts, "，")
}

// callOptions assembles this call's CallOptions: always an output cap, plus thinking when capable.
// thinking is sent only when non-Auto — sending any level (including off) to a model that does not support
// thinking is an invalid parameter (the same policy as the arbiter).
func (p callProfile) callOptions(maxTokens int) []agentcore.CallOption {
	opts := []agentcore.CallOption{agentcore.WithMaxTokens(maxTokens)}
	if p.thinking != agentcore.ThinkingAuto {
		opts = append(opts, agentcore.WithThinking(p.thinking))
	}
	return opts
}

// callStructured adapts the unified structured executor for the import layer and maps generic failures onto import-artifact semantics.
func callStructured[T any](ctx context.Context, m callModel, contract llmcontract.Contract, systemPrompt, payload string, maxTokens int, prof callProfile, validate func(*T) error) (T, error) {
	out, err := llmcontract.Execute(ctx, m, llmcontract.Request[T]{
		Contract:     contract,
		SystemPrompt: systemPrompt,
		Payload:      payload,
		Options:      prof.callOptions(maxTokens),
		Validate:     validate,
		Agent:        "import",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				prof.logger().Debug("chọn giao thức có cấu trúc cho imp",
					"contract", contract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider,
					"model", res.Model, "schema_fingerprint", contract.Fingerprint())
			},
			RequestRetry: func(ev llmretry.Event) {
				prof.sayRetry(time.Now().Add(ev.Delay), "yêu cầu tới model thất bại (%s), thực hiện lần thử lại thứ %d", briefErr(ev.Err), ev.Attempt)
				prof.logger().Warn("thử lại yêu cầu model của imp", "attempt", ev.Attempt, "delay", ev.Delay, "err", ev.Err)
			},
			Correction: func(ev llmcontract.Correction) {
				prof.say("kiểm tra đầu ra không đạt (%s), hỏi lại lần thứ %d kèm phản hồi lỗi", briefErr(ev.Err), ev.Attempt+1)
				prof.logger().Warn("tự sửa đầu ra có cấu trúc của imp", "attempt", ev.Attempt,
					"layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	var failure *llmcontract.Failure
	if !errors.As(err, &failure) {
		return out, fmt.Errorf("imp: %w", err)
	}
	switch failure.Kind {
	case llmcontract.FailureLength:
		return out, &errTruncated{Raw: failure.Raw}
	case llmcontract.FailureSafety, llmcontract.FailureContract, llmcontract.FailureProtocol:
		if failure.Raw != "" {
			return out, &errSemantic{Raw: failure.Raw, Err: fmt.Errorf("imp: %w", failure)}
		}
	case llmcontract.FailureRequest:
		if detail := modelErrDetail(failure); detail != "" {
			return out, fmt.Errorf("imp: gọi model thất bại (%s): %w", detail, failure)
		}
	}
	return out, fmt.Errorf("imp: %w", failure)
}
