package domain

// TimelineEvent is a timeline event.
type TimelineEvent struct {
	Chapter    int      `json:"chapter"`
	Time       string   `json:"time"`
	Event      string   `json:"event"`
	Characters []string `json:"characters,omitempty"`
}

// ForeshadowEntry is a foreshadow ledger entry.
type ForeshadowEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	PlantedAt   int    `json:"planted_at"`
	Status      string `json:"status"` // planted / advanced / resolved
	ResolvedAt  int    `json:"resolved_at,omitempty"`
}

// ForeshadowUpdate is an incremental foreshadow operation.
type ForeshadowUpdate struct {
	ID          string `json:"id"`
	Action      string `json:"action"` // plant / advance / resolve
	Description string `json:"description,omitempty"`
}

// RestoreOwnPlants puts back at the head of the queue the foreshadow plants this chapter
// declared in an older record but no longer declares in the new one.
// Which foreshadows a chapter planted is a historical fact about that chapter and rewriting
// its prose does not change it; dropping it means that when chapter records are replayed in
// full, the advance/resolve calls in this and later chapters find no preceding plant and the
// whole chain errors out.
func RestoreOwnPlants(prev, next []ForeshadowUpdate) []ForeshadowUpdate {
	declared := make(map[string]struct{}, len(next))
	for _, u := range next {
		if u.Action == "plant" {
			declared[u.ID] = struct{}{}
		}
	}
	var restored []ForeshadowUpdate
	for _, u := range prev {
		if u.Action != "plant" {
			continue
		}
		if _, ok := declared[u.ID]; ok {
			continue
		}
		declared[u.ID] = struct{}{}
		restored = append(restored, u)
	}
	if len(restored) == 0 {
		return next
	}
	// plants must precede the advance/resolve calls of the same chapter so replay creates the entry first.
	return append(restored, next...)
}

// RelationshipEntry is a character-relationship entry.
type RelationshipEntry struct {
	CharacterA string `json:"character_a"`
	CharacterB string `json:"character_b"`
	Relation   string `json:"relation"`
	Chapter    int    `json:"chapter"`
}

// ConsistencyIssue is a consistency problem.
type ConsistencyIssue struct {
	Type           string `json:"type"`     // The specific issue dimension the model reports from the rubric
	Severity       string `json:"severity"` // critical / error / warning
	Description    string `json:"description"`
	Evidence       string `json:"evidence,omitempty"` // Evidence: verbatim excerpt, concrete plot point or state data
	Suggestion     string `json:"suggestion,omitempty"`
	Chapters       []int  `json:"chapters,omitempty"` // Chapters the evidence actually falls in
	RequiresChange bool   `json:"requires_change"`    // Whether this should enter the rework queue now, judged semantically by the Editor
}

// DimensionScore is a single review dimension score.
type DimensionScore struct {
	Dimension string `json:"dimension"`         // Defined by the review rubric, extensible per task
	Score     int    `json:"score"`             // 0-100
	Verdict   string `json:"verdict,omitempty"` // Legacy review compatibility; at runtime thresholds no longer override the model judgement
	Comment   string `json:"comment,omitempty"` // Brief conclusion for this dimension
}

// ReviewEntry is one of the Editor's review entries.
type ReviewEntry struct {
	Chapter          int                `json:"chapter"`
	Scope            string             `json:"scope"` // chapter / global / arc
	Issues           []ConsistencyIssue `json:"issues"`
	Dimensions       []DimensionScore   `json:"dimensions,omitempty"`      // Per-dimension scores
	ContractStatus   string             `json:"contract_status,omitempty"` // met / partial / missed
	ContractMisses   []string           `json:"contract_misses,omitempty"` // Contract entries not met
	ContractNotes    string             `json:"contract_notes,omitempty"`  // Short note on contract fulfilment
	Verdict          string             `json:"verdict"`                   // accept / polish / rewrite
	Summary          string             `json:"summary"`
	AffectedChapters []int              `json:"affected_chapters,omitempty"` // Chapter numbers needing rewrite/polish
}

// CriticalCount returns the number of critical-severity issues.
func (r *ReviewEntry) CriticalCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "critical" {
			n++
		}
	}
	return n
}

// ErrorCount returns the number of error-severity issues.
func (r *ReviewEntry) ErrorCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			n++
		}
	}
	return n
}

// Dimension returns the score for the named dimension, or nil when absent.
func (r *ReviewEntry) Dimension(name string) *DimensionScore {
	if r == nil {
		return nil
	}
	for i := range r.Dimensions {
		if r.Dimensions[i].Dimension == name {
			return &r.Dimensions[i]
		}
	}
	return nil
}
