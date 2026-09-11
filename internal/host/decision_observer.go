package host

import "time"

// runObservedDecision gives one complete Arbiter adjudication an observable lifecycle.
// The Arbiter remains a non-streaming LLM function; this only reuses the existing event-ID
// start/finish in-place update mechanism, introducing no extra state and mixing no structured JSON into
// the Worker's live output panel.
func runObservedDecision[T any](o *observer, label string, call func() (T, error)) (T, error) {
	if o == nil {
		return call()
	}
	started := time.Now()
	id := nextEventID()
	o.emitAndLog(Event{
		ID:       id,
		Time:     started,
		Category: "DECISION",
		Agent:    "arbiter",
		Summary:  label,
		Level:    "info",
	})

	result, err := call()
	finished := time.Now()
	ev := Event{
		ID:         id,
		Time:       started,
		FinishedAt: finished,
		Failed:     err != nil,
		Category:   "DECISION",
		Agent:      "arbiter",
		Summary:    label,
		Level:      "success",
		Duration:   finished.Sub(started),
	}
	if err != nil {
		ev.Level = "error"
		ev.Detail = err.Error()
		ev.Kind = errorKind(err, err.Error())
	}
	o.emitEv(ev)
	o.persistEvent(ev)
	return result, err
}
