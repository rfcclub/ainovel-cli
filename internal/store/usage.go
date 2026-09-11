package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// UsageStore persists accumulated token / cost usage to meta/usage.json.
// Writes go through IO's atomic write (tmp + rename), and the Save path overwrites the whole state each time.
type UsageStore struct{ io *IO }

func NewUsageStore(io *IO) *UsageStore { return &UsageStore{io: io} }

// Load reads usage.json, returning (nil, nil) when the file is absent or the schema version does not match; the caller
// decides whether to run a one-shot session replay refill.
func (s *UsageStore) Load() (*domain.UsageState, error) {
	var state domain.UsageState
	if err := s.io.ReadJSON("meta/usage.json", &state); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if state.Schema != domain.UsageSchemaVersion {
		return nil, nil
	}
	return &state, nil
}

// Save overwrites the whole state on disk. The caller owns debouncing / throttling.
func (s *UsageStore) Save(state domain.UsageState) error {
	state.Schema = domain.UsageSchemaVersion
	return s.io.WriteJSON("meta/usage.json", state)
}
