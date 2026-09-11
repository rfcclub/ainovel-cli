package diag

// Severity is a finding's severity.
type Severity string

const (
	SevCritical Severity = "critical" // Blocks progress or corrupts data
	SevWarning  Severity = "warning"  // May reduce quality or waste tokens
	SevInfo     Severity = "info"     // Optimisation opportunity
)

// Category groups findings by dimension.
type Category string

const (
	CatFlow     Category = "flow"     // Flow stalls, state anomalies, recovery problems
	CatQuality  Category = "quality"  // Review scores, contract fulfilment, consistency
	CatPlanning Category = "planning" // Outline gaps, foreshadow drift, stale compass
	CatContext  Category = "context"  // Character/timeline/relationship anomalies
)

// Confidence is the confidence of a rule's judgement.
type Confidence string

const (
	ConfHigh   Confidence = "high"   // Strong certainty, trustworthy
	ConfMedium Confidence = "medium" // Heuristic judgement, may misjudge
	ConfLow    Confidence = "low"    // Coarse signal, informational only
)

// AutoLevel indicates whether a Finding may become an automated action.
type AutoLevel string

const (
	AutoNone    AutoLevel = "none"    // Report only, never automatic
	AutoSuggest AutoLevel = "suggest" // Suggests an action but needs human confirmation
	AutoSafe    AutoLevel = "safe"    // Safe to run automatically
)

// Finding is one actionable diagnostic result.
type Finding struct {
	Rule       string     // Rule name, e.g. "StaleForeshadow"
	Category   Category   // Category
	Severity   Severity   // Severity
	Confidence Confidence // Judgement confidence
	AutoLevel  AutoLevel  // Automation level
	Target     string     // Suggested scope, e.g. "runtime.flow"
	Title      string     // One-line summary
	Evidence   string     // Concrete data evidence
	Suggestion string     // Improvement suggestion (points at prompt/flow/config)
}

// RuleFunc is the uniform signature of a diagnostic rule.
type RuleFunc func(snap *Snapshot) []Finding

// ActionKind is the type of a diagnostic action.
type ActionKind string

const (
	ActionEmitNotice      ActionKind = "emit_notice"       // Emit a system notice
	ActionEnqueueFollowUp ActionKind = "enqueue_follow_up" // Generate a follow-up handling suggestion
)

// Action is an executable action the Planner generates from a high-confidence Finding.
type Action struct {
	SourceRule  string     // Source rule name
	Kind        ActionKind // Action kind
	Severity    Severity   // Inherited from Finding
	Summary     string     // Short description
	Message     string     // Message passed to the control flow
	Fingerprint string     // Stable fingerprint of the source Finding, used for runtime dedupe
}

// Stats are the overview metrics displayed alongside the findings.
type Stats struct {
	CompletedChapters int
	TotalChapters     int
	TotalWords        int
	AvgWordsPerCh     int
	Phase             string
	Flow              string
	PlanningTier      string
	ReviewCount       int
	RewriteCount      int
	AvgReviewScore    float64
	ForeshadowOpen    int
	ForeshadowStale   int
}

// Report is the complete output of one diagnostic run.
type Report struct {
	Stats    Stats
	Findings []Finding
	Actions  []Action
}
