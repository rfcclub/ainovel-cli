package domain

// FoundationAuditIssue is a cross-file consistency problem the Architect reports for the
// persisted foundation settings.
type FoundationAuditIssue struct {
	Artifact    string `json:"artifact"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// FoundationAudit records one model audit against a specific version of the foundation settings.
type FoundationAudit struct {
	Fingerprint string                 `json:"fingerprint"`
	Ready       bool                   `json:"ready"`
	Summary     string                 `json:"summary"`
	Issues      []FoundationAuditIssue `json:"issues"`
}
