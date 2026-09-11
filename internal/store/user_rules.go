package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/rules"
)

// UserRulesStore manages this book's normalised user-rules snapshot (meta/user_rules.json).
//
// The runtime's single source of truth: both novel_context injection and commit_chapter's check read only this one copy
// instead of repeatedly reading the rules files (avoiding drift and divergence between two readers). The snapshot is
// normalised and generated when the book starts, on import and on refresh.
type UserRulesStore struct{ io *IO }

func NewUserRulesStore(io *IO) *UserRulesStore { return &UserRulesStore{io: io} }

// Load reads meta/user_rules.json, returning nil when it does not exist (the caller lazily generates it on that basis).
func (s *UserRulesStore) Load() (*rules.Snapshot, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	var snap rules.Snapshot
	if err := s.io.ReadJSONUnlocked("meta/user_rules.json", &snap); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &snap, nil
}

// Save persists the snapshot.
func (s *UserRulesStore) Save(snap *rules.Snapshot) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.io.WriteJSONUnlocked("meta/user_rules.json", snap)
}
