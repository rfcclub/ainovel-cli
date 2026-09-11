// Package store provides filesystem-backed persistent storage.
//
// Architecture: one IO base + several sub-stores + one composition root.
// Each sub-store holds its own IO instance and its own sync.RWMutex.
// Reads and writes across the main domains (Progress, Outline, Drafts, Summaries and so on) never block one another; the
// WorldStore merges several low-frequency small domains onto a single shared lock.
//
// The composition root Store holds references to every sub-store and serialises cross-domain operations (ExpandArc,
// AppendVolume, ClearHandledSteer); multiple files do not form an atomic transaction, so callers rely on safe write
// ordering, explicit errors and idempotent replay with the same arguments to recover.
//
// Sub-store breakdown:
//   - ProgressStore: main progress state (meta/progress.json)
//   - OutlineStore: premise, outline (flat/layered), compass
//   - DraftStore: chapter plans, drafts, final drafts
//   - SummaryStore: chapter/arc/volume summaries
//   - RunMetaStore: run metadata (models, intervention history)
//   - SignalStore: one-shot signal files (PendingCommit recovery)
//   - CheckpointStore: step-level checkpoints (meta/checkpoints.jsonl)
//   - RuntimeStore: runtime event queue (meta/runtime/*.jsonl)
//   - CharacterStore: character files, state snapshots
//   - WorldStore: timeline, foreshadows, relationships, state changes, world rules, style rules, reviews
package store
