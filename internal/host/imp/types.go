// Package imp implements the staged semantic import pipeline for external novels
// (docs/import-pipeline.md).
//
// The model understands open semantics while the code owns coordinates, coverage, types, hashing, ordering and
// idempotency; every semantic product is published to official book state only after it has been validated in the
// separate workspace (meta/import/). The next action is derived from artifacts alone (NextAction), no drifting
// stage enum is stored, and recovery does not depend on from=N.
package imp

import "time"

// Options controls one import. Fields may be empty on recovery, deriving everything from the active workspace and the saved Intent.
type Options struct {
	SourcePath      string // Required for a new import; may be empty when resuming
	AutoConfirm     bool   // --yes: auto-accept the segmentation once coverage validation passes
	StoryResolution string // --story=open|closed: pre-select only when synthesis returns uncertain
	ContinueAfter   bool   // --continue: do not create the import-complete Hold
	Guidance        string // --guide: natural-language segmentation guidance; persisting it into the workspace naturally invalidates the old segmentation and forces re-detection
	// AcceptSegmentation: the explicit human confirmation after the TUI preview (y). It lets the current segmentation
	// through for this run without writing intent; unlike --yes, which is blind authorisation without seeing the
	// preview and does not let through a segmentation carrying tolerance notes (Notes), y is a ruling made after
	// seeing the preview.
	AcceptSegmentation bool
}

// intent extracts the user authorisation that must be persisted from Options.
func (o Options) intent() Intent {
	return Intent{
		Version:             workspaceSchemaVersion,
		AutoConfirm:         o.AutoConfirm,
		StoryResolution:     o.StoryResolution,
		ContinueAfterImport: o.ContinueAfter,
	}
}

// Stage represents the import flow's current stage for UI display only; it is not the recovery source of truth (RFC §14.1).
type Stage string

const (
	StageIngesting            Stage = "ingesting"
	StageSegmenting           Stage = "segmenting"
	StageAwaitingConfirmation Stage = "awaiting_confirmation"
	StageAnalyzing            Stage = "analyzing"
	StageSynthesizing         Stage = "synthesizing"
	StageAwaitingStoryStatus  Stage = "awaiting_story_status"
	StageValidating           Stage = "validating"
	StagePublishing           Stage = "publishing"
	StageDone                 Stage = "done"
	StageError                Stage = "error"
)

// Event is the progress event the import flow emits. Event is a projection and takes no part in recovery.
type Event struct {
	Time      time.Time
	Stage     Stage
	Current   int       // Chapter/range progress
	Total     int       // Total count
	Message   string    // Human-readable description
	Level     string    // "" = normal progress; "warn" = cautionary state such as backoff retry or validation re-ask
	Key       string    // When non-empty the UI updates consecutive events with the same Key in place (e.g. 7 backoffs changing on one line), matching the event-panel ID mechanism
	RetryAt   time.Time // Non-zero = deadline of the next retry; the UI renders a per-second countdown and clears it on expiry (the request is already in flight)
	Err       error     // Carried when StageError
	Continued bool      // Set by the Host on StageDone: whether the Engine was automatically started to continue (--continue x auto)
}
