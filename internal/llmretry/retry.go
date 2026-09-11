// Package llmretry is the shared request-layer retry kernel for direct model calls: it
// retries only errors the model adapter explicitly marks as retryable, honours
// Retry-After/exponential backoff, and feeds progress into the existing workbench observation
// chain via ToolProgress. Terminal errors such as account, authentication and permission
// problems are returned immediately; retryable errors keep retrying, with their lifetime
// controlled solely by the context.
package llmretry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/voocel/agentcore"
)

const maxRetryDelay = 60 * time.Second

// Generator is the minimal model interface required for request retries.
type Generator interface {
	Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// Event describes one upcoming request retry.
type Event struct {
	Attempt int
	Delay   time.Duration
	Err     error
}

// Config supplies retry observability; it does not change retry semantics.
type Config struct {
	Agent   string
	OnRetry func(Event)
}

// Generate calls model.Generate. Retryable errors keep retrying after backoff until they
// succeed or the context ends; non-retryable errors are returned immediately.
func Generate(ctx context.Context, model Generator, cfg Config, messages []agentcore.Message, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	for retry := 1; ; retry++ {
		resp, err := model.Generate(ctx, messages, nil, opts...)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !isRetryable(err) {
			return nil, err
		}

		delay := retryDelay(err, retry-1)
		event := Event{Attempt: retry, Delay: delay, Err: err}
		if cfg.OnRetry != nil {
			cfg.OnRetry(event)
		}
		meta, _ := json.Marshal(struct {
			DelayMS int64 `json:"retry_delay_ms"`
		}{DelayMS: delay.Milliseconds()})
		agentcore.ReportToolProgress(ctx, agentcore.ProgressPayload{
			Kind:    agentcore.ProgressRetry,
			Agent:   cfg.Agent,
			Attempt: retry,
			Message: err.Error(),
			Meta:    meta,
		})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func isRetryable(err error) bool {
	var retryable agentcore.RetryableError
	return errors.As(err, &retryable) && retryable.Retryable()
}

func retryDelay(err error, attempt int) time.Duration {
	var hinter agentcore.RetryHinter
	if errors.As(err, &hinter) {
		if delay := hinter.RetryAfter(); delay > 0 {
			if delay > maxRetryDelay {
				return maxRetryDelay
			}
			return delay
		}
	}
	delay := time.Second
	for i := 0; i < attempt && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}
