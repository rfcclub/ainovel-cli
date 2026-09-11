package diag

import (
	"fmt"
	"sort"

	"github.com/voocel/ainovel-cli/internal/store"
)

// ── Diagnostic thresholds ─────────────────────────────────────────────

const (
	ThresholdDimScoreLow      = 70  // ChronicLowDimension: warn when a dimension's average score falls below this
	ThresholdContractMissRate = 0.3 // ContractMissPattern: upper bound on the contract miss rate
	ThresholdRewriteRate      = 0.5 // ExcessiveRewrites: upper bound on the rewrite rate
	ThresholdWordShortRatio   = 0.4 // WordCountAnomaly: below this share of the average counts as anomalous
	ThresholdWordLongRatio    = 2.5 // WordCountAnomaly: above this multiple of the average counts as anomalous
	ThresholdHookWeakScore    = 75  // HookWeakChain: a hook below this score counts as weak
	ThresholdHookWeakChain    = 3   // HookWeakChain: consecutive weak chapters threshold
	ThresholdPayoffMissRate   = 0.4 // PayoffMissPattern: upper bound on the payoff miss rate
	ThresholdCompassDrift     = 15  // CompassDrift: max chapters without a compass update
	ThresholdTimelineGapRate  = 0.3 // TimelineGaps: tolerated gap rate
	ThresholdForeshadowMin    = 8   // StaleForeshadow: minimum chapters for a stalled foreshadow
)

// allRules is ordered flow → quality → planning → context.
var allRules = []RuleFunc{
	// Flow
	InvalidPendingRewrites,
	RewritePendingPressure,
	OrphanedSteer,
	PhaseFlowMismatch,
	ChapterGaps,
	// Quality
	ChronicLowDimension,
	ContractMissPattern,
	HookWeakChain,
	PayoffMissPattern,
	ExcessiveRewrites,
	WordCountAnomaly,
	// Planning
	StaleForeshadow,
	CompassDrift,
	OutlineExhausted,
	MissingSummaries,
	// Context
	GhostCharacter,
	TimelineGaps,
	RelationshipStagnation,
}

// Analyze is the diagnostic system's single entry point.
func Analyze(s *store.Store) Report {
	snap := Load(s)

	var findings []Finding
	for _, e := range snap.LoadErrors {
		findings = append(findings, Finding{
			Rule:       "LoadError",
			Category:   CatFlow,
			Severity:   SevWarning,
			Confidence: ConfHigh,
			AutoLevel:  AutoNone,
			Target:     "runtime.flow",
			Title:      fmt.Sprintf("Nạp sản phẩm thất bại: %s", e),
			Suggestion: "File có thể bị hỏng hoặc thiếu quyền, kết quả của các luật chẩn đoán liên quan có thể không đầy đủ.",
		})
	}
	for _, rule := range allRules {
		findings = append(findings, rule(&snap)...)
	}
	sortFindings(findings)

	return Report{
		Stats:    buildStats(&snap),
		Findings: findings,
		Actions:  PlanActions(findings),
	}
}

func buildStats(snap *Snapshot) Stats {
	st := Stats{}
	if snap.Progress == nil {
		return st
	}
	p := snap.Progress
	st.CompletedChapters = len(p.CompletedChapters)
	st.TotalChapters = p.TotalChapters
	st.TotalWords = p.TotalWordCount
	st.Phase = string(p.Phase)
	st.Flow = string(p.Flow)

	if st.CompletedChapters > 0 {
		st.AvgWordsPerCh = st.TotalWords / st.CompletedChapters
	}

	if snap.RunMeta != nil {
		st.PlanningTier = string(snap.RunMeta.PlanningTier)
	}

	// Review statistics
	st.ReviewCount = len(snap.Reviews)
	var totalScore float64
	var dimCount int
	for _, r := range snap.Reviews {
		if r.Verdict == "rewrite" {
			st.RewriteCount++
		}
		for _, d := range r.Dimensions {
			totalScore += float64(d.Score)
			dimCount++
		}
	}
	if dimCount > 0 {
		st.AvgReviewScore = totalScore / float64(dimCount)
	}

	// Foreshadow statistics
	latest := snap.LatestCompleted()
	for _, f := range snap.Foreshadow {
		if f.Status == "planted" || f.Status == "advanced" {
			st.ForeshadowOpen++
			if f.Status == "planted" && latest-f.PlantedAt > staleForeshadowThreshold(st.CompletedChapters) {
				st.ForeshadowStale++
			}
		}
	}
	return st
}

// sortFindings sorts by severity: critical > warning > info.
func sortFindings(findings []Finding) {
	order := map[Severity]int{SevCritical: 0, SevWarning: 1, SevInfo: 2}
	sort.SliceStable(findings, func(i, j int) bool {
		return order[findings[i].Severity] < order[findings[j].Severity]
	})
}

// staleForeshadowThreshold computes the foreshadow-stagnation threshold from the total chapter count.
func staleForeshadowThreshold(completedChapters int) int {
	t := completedChapters / 3
	if t < ThresholdForeshadowMin {
		return ThresholdForeshadowMin
	}
	return t
}
