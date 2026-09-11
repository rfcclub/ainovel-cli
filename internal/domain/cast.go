package domain

// CastEntry is one supporting-character record in the cast ledger.
//
// It is decoupled from Character (characters.json, the core profiles the Architect maintains):
//   - CastEntry is accumulated automatically by the commit_chapter tool, recording "named
//     secondary characters that have appeared".
//   - Character is designed explicitly by the Architect, recording the personality arc,
//     traits and tier of the protagonist and key supporting cast.
//
// On a name collision, Character wins (core characters never enter cast_ledger), avoiding
// duplication.
type CastEntry struct {
	Name string `json:"name"`
	// Aliases currently has no write path; it is reserved for a future "user steer merges
	// aliases" tool (e.g. declaring "Lý chưởng quầy" and "Lão Lý" to be the same person).
	// MergeAppearances already supports alias lookup.
	Aliases          []string `json:"aliases,omitempty"`
	BriefRole        string   `json:"brief_role,omitempty"` // One-line role (filled by the Writer on first appearance, completable later; never overwritten)
	FirstSeenChapter int      `json:"first_seen_chapter"`
	LastSeenChapter  int      `json:"last_seen_chapter"`
	// AppearanceCount derives from len(AppearanceChapters) and stays in sync during merges.
	// The explicit field is kept so the UI/JSON can read it directly without recomputing.
	AppearanceCount    int   `json:"appearance_count"`
	AppearanceChapters []int `json:"appearance_chapters"`
	// Promoted marks this entry as promoted into characters.json. RecentActive skips such
	// entries so they are not recalled twice alongside the core profiles. The promotion path
	// is not implemented yet; the field is a reserved hook.
	Promoted bool `json:"promoted,omitempty"`
}

// CastIntro is the Writer's introduction declaration for a newly appearing character at
// commit_chapter time. It is only adopted when the name appears for the first time or when
// its BriefRole in the ledger is still empty.
type CastIntro struct {
	Name      string `json:"name"`
	BriefRole string `json:"brief_role"`
}
