package imp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ChapterCommitter is the minimal interface needed to publish chapters, satisfied by tools.CommitChapterTool.
// It reuses that tool's PendingCommit saga, checkpoints and completed-chapter idempotency check rather than
// duplicating a second commit logic (RFC §12.3).
type ChapterCommitter interface {
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// publishFoundation publishes the Foundation in official dependency order, matching the Architect's long-form
// persistence order (RFC §12.2).
// Republishing identical content is idempotent (the Store overwrites the same content and checkpoints dedupe).
func publishFoundation(st *store.Store, f *Foundation) error {
	// Conflict reconciliation before publication: an existing official artifact that differs is refused
	// overwriting (§12.2 / invariant 6). Identical content proceeds idempotently (the Store overwrites the same
	// content and checkpoints dedupe).
	if err := checkFoundationConflicts(st, f); err != nil {
		return err
	}
	if err := st.Book.Save(f.Book); err != nil {
		return fmt.Errorf("book：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "book", "meta/book.json"); err != nil {
		return fmt.Errorf("checkpoint book：%w", err)
	}
	if err := st.RunMeta.SetPlanningTier(f.PlanningTier); err != nil {
		return fmt.Errorf("planning tier：%w", err)
	}
	// premise
	if err := st.Outline.SavePremise(f.Premise); err != nil {
		return fmt.Errorf("premise：%w", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhasePremise); err != nil {
		return fmt.Errorf("phase premise：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "premise", "premise.md"); err != nil {
		return fmt.Errorf("checkpoint premise：%w", err)
	}
	// characters
	if err := st.Characters.Save(f.Characters); err != nil {
		return fmt.Errorf("characters：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "characters", "characters.json"); err != nil {
		return fmt.Errorf("checkpoint characters：%w", err)
	}
	// world rules
	if err := st.World.SaveWorldRules(f.WorldRules); err != nil {
		return fmt.Errorf("world_rules：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "world_rules", "world_rules.json"); err != nil {
		return fmt.Errorf("checkpoint world_rules：%w", err)
	}
	// The layered outline is the single source, and the Store rebuilds the flat outline in step.
	if err := st.Outline.SaveLayeredOutline(f.Volumes); err != nil {
		return fmt.Errorf("layered outline：%w", err)
	}
	// The outline-stage progress is the basis on which the engine recomputes routing (chapter capacity /
	// layering / current volume-arc), so a failed write would leave an inconsistent published state and must be
	// surfaced rather than swallowed (RFC §12.2).
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		return fmt.Errorf("phase outline：%w", err)
	}
	if err := st.Progress.SetTotalChapters(domain.EstimatedChapterCapacity(f.Volumes)); err != nil {
		return fmt.Errorf("total chapters：%w", err)
	}
	if err := st.Progress.SetLayered(true); err != nil {
		return fmt.Errorf("set layered：%w", err)
	}
	if len(f.Volumes) > 0 && len(f.Volumes[0].Arcs) > 0 {
		if err := st.Progress.UpdateVolumeArc(f.Volumes[0].Index, f.Volumes[0].Arcs[0].Index); err != nil {
			return fmt.Errorf("volume arc：%w", err)
		}
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "layered_outline", "layered_outline.json"); err != nil {
		return fmt.Errorf("checkpoint layered outline：%w", err)
	}
	// compass
	if err := st.Outline.SaveCompass(f.Compass); err != nil {
		return fmt.Errorf("compass：%w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "compass", "meta/compass.json"); err != nil {
		return fmt.Errorf("checkpoint compass：%w", err)
	}
	// Every official write of the import Foundation has succeeded, so writing can be entered explicitly.
	// The ordinary creation flow's FoundationMissing cannot be reused: an import allows empty world_rules, and
	// treating a legitimate empty value as missing would leave progress stuck at outline forever, after which
	// StartChapter is refused by the stage gate.
	p, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress：%w", err)
	}
	if p == nil {
		return fmt.Errorf("load progress: progress chưa được khởi tạo")
	}
	if p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete {
		if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
			return fmt.Errorf("phase writing：%w", err)
		}
	}
	return nil
}

// checkFoundationConflicts validates that the Foundation about to be published agrees with existing official
// artifacts: empty existing means first publication; identical means idempotent; different reports a conflict and
// refuses to overwrite (RFC §12.2 / invariant 6).
// The compass and flat outline derive from the layered outline, so a consistent layering implies consistent
// derivation and the derived artifacts are not checked separately.
// A read error must not be swallowed as "file does not exist": the store loader returns (zero value, nil) for a
// miss, so any non-nil error is a genuine one (corruption / permissions / invalid JSON) and continuing with a zero
// value would overwrite an unreadable official artifact (RFC §12.2).
func checkFoundationConflicts(st *store.Store, f *Foundation) error {
	wantBook := f.Book.Normalized()
	book, err := st.Book.Load()
	if err != nil {
		return fmt.Errorf("đọc book chính thức: %w", err)
	}
	if book != nil && !jsonEqual(book, wantBook) {
		return fmt.Errorf("book chính thức xung đột với kết quả tổng hợp khi nhập (đã có phiên bản khác), từ chối ghi đè")
	}
	cur, err := st.Outline.LoadPremise()
	if err != nil {
		return fmt.Errorf("đọc premise chính thức: %w", err)
	}
	if cur != "" && cur != f.Premise {
		return fmt.Errorf("premise chính thức xung đột với kết quả tổng hợp khi nhập (đã có phiên bản khác), từ chối ghi đè")
	}
	chars, err := st.Characters.Load()
	if err != nil {
		return fmt.Errorf("đọc characters chính thức: %w", err)
	}
	if len(chars) > 0 && !jsonEqual(chars, f.Characters) {
		return fmt.Errorf("characters chính thức xung đột với kết quả tổng hợp khi nhập (đã có phiên bản khác), từ chối ghi đè")
	}
	rules, err := st.World.LoadWorldRules()
	if err != nil {
		return fmt.Errorf("đọc world_rules chính thức: %w", err)
	}
	if len(rules) > 0 && !jsonEqual(rules, f.WorldRules) {
		return fmt.Errorf("world_rules chính thức xung đột với kết quả tổng hợp khi nhập (đã có phiên bản khác), từ chối ghi đè")
	}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("đọc layered_outline chính thức: %w", err)
	}
	if len(layered) > 0 && !jsonEqual(layered, f.Volumes) {
		return fmt.Errorf("layered_outline chính thức xung đột với kết quả tổng hợp khi nhập (đã có phiên bản khác), từ chối ghi đè")
	}
	return nil
}

// jsonEqual compares two values for equivalence by canonical JSON bytes.
func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// publishChapter reuses commit_chapter to publish one chapter; completed chapters are skipped by its idempotency check (RFC §12.3).
func publishChapter(ctx context.Context, st *store.Store, commit ChapterCommitter, chapter int, content string, f ImportedChapterFacts) error {
	completed, err := st.Progress.IsChapterCompleted(chapter)
	if err != nil {
		return fmt.Errorf("load progress ch%d：%w", chapter, err)
	}
	if completed {
		// A crash can land between MarkChapterComplete and ClearPendingCommit, leaving a pending_commit residue
		// pointing at this chapter. Skipping outright would bypass the cleanup branch the commit tool prepared for
		// exactly this window (restore the checkpoint + clear the residue), and the next chapter's Execute would
		// refuse with "there is an unrecovered chapter commit" — the import would die at the same point on every
		// rerun and need meta/pending_commit.json deleted by hand to unlock. On a residue hit it still goes through
		// the tool's idempotent path to complete the cleanup.
		pending, err := st.Signals.LoadPendingCommit()
		if err != nil {
			return fmt.Errorf("load pending commit ch%d：%w", chapter, err)
		}
		if pending != nil && pending.Chapter == chapter {
			raw, err := json.Marshal(commitArgs(chapter, f))
			if err != nil {
				return fmt.Errorf("marshal commit ch%d：%w", chapter, err)
			}
			if _, err := commit.Execute(ctx, raw); err != nil {
				return fmt.Errorf("commit ch%d：%w", chapter, err)
			}
		}
		return nil
	}
	if err := st.Drafts.SaveDraft(chapter, content); err != nil {
		return fmt.Errorf("save draft ch%d：%w", chapter, err)
	}
	if err := st.Progress.StartChapter(chapter); err != nil {
		return fmt.Errorf("start ch%d：%w", chapter, err)
	}
	raw, err := json.Marshal(commitArgs(chapter, f))
	if err != nil {
		return fmt.Errorf("marshal commit ch%d：%w", chapter, err)
	}
	if _, err := commit.Execute(ctx, raw); err != nil {
		return fmt.Errorf("commit ch%d：%w", chapter, err)
	}
	return nil
}

// commitArgs maps per-chapter facts onto commit_chapter arguments.
func commitArgs(chapter int, f ImportedChapterFacts) map[string]any {
	keyEvents := f.KeyEvents
	if len(keyEvents) == 0 {
		keyEvents = []string{f.CoreEvent} // core_event is already validated non-empty.
	}
	args := map[string]any{
		"chapter":         chapter,
		"title":           f.Title,
		"summary":         f.Summary,
		"characters":      f.Characters,
		"key_events":      keyEvents,
		"hook_type":       f.HookType,
		"dominant_strand": f.DominantStrand,
	}
	if len(f.TimelineEvents) > 0 {
		args["timeline_events"] = f.TimelineEvents
	}
	if len(f.ForeshadowUpdates) > 0 {
		args["foreshadow_updates"] = f.ForeshadowUpdates
	}
	if len(f.RelationshipChanges) > 0 {
		args["relationship_changes"] = f.RelationshipChanges
	}
	if len(f.StateChanges) > 0 {
		args["state_changes"] = f.StateChanges
	}
	return args
}

// isPublished reports whether official state already reflects the complete import: the Foundation is on disk and
// the completed chapters match expectations.
// It reconciles only the artifacts the import genuinely produces — book, premise, the flat outline covering every
// chapter, completed chapters — rather than reusing FoundationMissing(), which is the ordinary creation flow's
// "writable" gate and would misjudge legitimately empty world_rules as unfinished, leaving publication
// reconciliation never converging (RFC §12.3).
func isPublished(st *store.Store, expected int) (bool, error) {
	if expected == 0 {
		return false, nil
	}
	book, err := st.Book.Load()
	if err != nil {
		return false, fmt.Errorf("đọc book chính thức: %w", err)
	}
	if book == nil {
		return false, nil
	}
	p, err := st.Outline.LoadPremise()
	if err != nil {
		return false, fmt.Errorf("đọc premise chính thức: %w", err)
	}
	if p == "" {
		return false, nil
	}
	o, err := st.Outline.LoadOutline()
	if err != nil {
		return false, fmt.Errorf("đọc outline chính thức: %w", err)
	}
	if len(o) < expected {
		return false, nil
	}
	prog, err := st.Progress.Load()
	if err != nil {
		return false, fmt.Errorf("đọc progress chính thức: %w", err)
	}
	return prog != nil && len(prog.CompletedChapters) >= expected, nil
}
