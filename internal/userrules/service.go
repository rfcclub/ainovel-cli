package userrules

import (
	"context"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Service orchestrates the creation and updating of the user-rules snapshot: normalise every
// source -> deterministic merge -> persist.
//
// Two callers share the same logic:
//   - Book start / refresh: Build / GetOrBuild, called deterministically by the Host.
//   - Runtime update: after the Arbiter extracts rules, the Host calls AddRuntimeRule.
type Service struct {
	store     *store.Store
	norm      *Normalizer
	rulesOpts rules.LoadOptions
}

// NewService builds the service. model is used for normalisation (it should be a
// capable model); when model is nil every source degrades to raw preferences (a
// snapshot is still produced and the mechanical checks are covered by system_defaults).
func NewService(st *store.Store, model agentcore.ChatModel, opts rules.LoadOptions) *Service {
	return &Service{store: st, norm: NewNormalizer(model), rulesOpts: opts}
}

// normalizeOrDegrade normalises one source; on failure it records the real error and
// degrades to raw preferences (snapshot Status=degraded, raw text preserved) — the
// degradation is a visible fact and the cause goes to the log.
func (s *Service) normalizeOrDegrade(ctx context.Context, source, text string) rules.Candidate {
	cand, err := s.norm.Normalize(ctx, source, text)
	if err != nil {
		slog.Warn("chuẩn hóa quy tắc thất bại, hạ cấp thành sở thích nguyên văn", "module", "rules", "source", source, "err", err)
		return degraded(source, text)
	}
	return cand
}

// Build normalises the static sources (system_defaults + rules files + startup prompt)
// into a snapshot and persists it. Called when a book starts or is refreshed;
// startupPrompt may be empty. The baseline follows the novel language so the fatigue
// words and forbidden phrases match the language actually being written.
func (s *Service) Build(ctx context.Context, startupPrompt string) (*rules.Snapshot, error) {
	cands := []rules.Candidate{rules.SystemDefaults()}
	for _, rs := range rules.RawFileSources(s.rulesOpts) {
		cands = append(cands, s.normalizeOrDegrade(ctx, rs.Label, rs.Text))
	}
	if strings.TrimSpace(startupPrompt) != "" {
		cands = append(cands, s.normalizeOrDegrade(ctx, "startup_prompt", startupPrompt))
	}
	snap := rules.BuildSnapshot(cands)
	if err := s.store.UserRules.Save(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// GetOrBuild returns the current snapshot, initialising it from system_defaults plus
// the rules files when absent. Every runtime read path goes through here.
func (s *Service) GetOrBuild(ctx context.Context) (*rules.Snapshot, error) {
	cur, err := s.store.UserRules.Load()
	if err != nil {
		return nil, err
	}
	if cur != nil {
		return cur, nil
	}
	return s.Build(ctx, "")
}

// AddRuntimeRule normalises one long-lived runtime rule, overlays it onto the current
// snapshot at the highest priority and persists the result. It never fails because of a
// normalisation error — that rule just degrades to raw preferences. Returns the overlaid
// snapshot and this round's normalisation candidate.
func (s *Service) AddRuntimeRule(ctx context.Context, text string) (*rules.Snapshot, rules.Candidate, error) {
	cur, err := s.GetOrBuild(ctx)
	if err != nil {
		return nil, rules.Candidate{}, err
	}
	cand := s.normalizeOrDegrade(ctx, "runtime_update", text)
	merged := rules.OverlaySnapshot(*cur, cand)
	if err := s.store.UserRules.Save(&merged); err != nil {
		return nil, cand, err
	}
	return &merged, cand, nil
}
