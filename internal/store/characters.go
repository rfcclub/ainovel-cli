package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CharacterStore manages character files and state snapshots.
type CharacterStore struct {
	io      *IO
	outline *OutlineStore // Read-only dependency used when walking snapshots
}

func NewCharacterStore(io *IO, outline *OutlineStore) *CharacterStore {
	return &CharacterStore{io: io, outline: outline}
}

// Save writes characters.json and characters.md together (atomic write).
func (s *CharacterStore) Save(chars []domain.Character) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("characters.json", chars); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("characters.md", renderCharacters(chars, s.io.labels()))
	})
}

// Load reads the character files from characters.json.
func (s *CharacterStore) Load() ([]domain.Character, error) {
	var chars []domain.Character
	if err := s.io.ReadJSON("characters.json", &chars); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return chars, nil
}

// SaveSnapshots saves character state snapshots to meta/snapshots/v{vol}a{arc}.json.
func (s *CharacterStore) SaveSnapshots(volume, arc int, snapshots []domain.CharacterSnapshot) error {
	return s.io.WriteJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), snapshots)
}

// LoadSnapshots reads the character snapshots for the given volume-arc.
func (s *CharacterStore) LoadSnapshots(volume, arc int) ([]domain.CharacterSnapshot, error) {
	var snapshots []domain.CharacterSnapshot
	if err := s.io.ReadJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), &snapshots); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return snapshots, nil
}

// LoadLatestSnapshots loads the most recent character snapshot (searching volume-arcs in descending order).
func (s *CharacterStore) LoadLatestSnapshots() ([]domain.CharacterSnapshot, error) {
	// A missing outline → (nil, nil) is already absorbed inside LoadLayeredOutline; corruption must be raised, or the
	// character snapshots would vanish silently along with the corrupt layered outline and continuity would be lost
	// without anyone knowing.
	volumes, err := s.outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	if len(volumes) == 0 {
		return nil, nil
	}
	for vi := len(volumes) - 1; vi >= 0; vi-- {
		v := volumes[vi]
		for ai := len(v.Arcs) - 1; ai >= 0; ai-- {
			snaps, err := s.LoadSnapshots(v.Index, v.Arcs[ai].Index)
			if err != nil {
				return nil, err
			}
			if len(snaps) > 0 {
				return snaps, nil
			}
		}
	}
	return nil, nil
}

func renderCharacters(chars []domain.Character, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.charProfiles)
	for _, c := range chars {
		fmt.Fprintf(&b, "## %s%s%s%s\n\n", c.Name, l.openParen, c.Role, l.closeParen)
		fmt.Fprintf(&b, "%s\n\n", c.Description)
		if c.Arc != "" {
			fmt.Fprintf(&b, "**%s**%s%s\n\n", l.charArc, l.colon, c.Arc)
		}
		if len(c.Traits) > 0 {
			fmt.Fprintf(&b, "**%s**%s%s\n\n", l.traits, l.colon, strings.Join(c.Traits, l.listSep))
		}
	}
	return b.String()
}
