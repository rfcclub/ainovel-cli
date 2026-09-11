package domain

// StateChange records a character/entity state change.
type StateChange struct {
	Chapter  int    `json:"chapter"`
	Entity   string `json:"entity"`              // Character or entity name
	Field    string `json:"field"`               // Changed attribute: realm/location/status/power/relation etc.
	OldValue string `json:"old_value,omitempty"` // Before the change (may be empty on first appearance)
	NewValue string `json:"new_value"`           // After the change
	Reason   string `json:"reason,omitempty"`    // Reason for the change
}
