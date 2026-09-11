package store

import (
	"os"
	"slices"
	"sort"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CastStore manages the supporting-cast roster (meta/cast_ledger.json).
//
// The supporting-cast roster records "named minor characters that have appeared", orthogonal to characters.json (the core
// character files):
//   - characters.json: the protagonist and key supporting characters the Architect designed explicitly, not modified
//     during writing
//   - cast_ledger.json: accumulated automatically by the commit_chapter tool, covering every named non-core supporting
//     character
//
// MergeAppearances is idempotent: a repeated commit of the same chapter does not double-count AppearanceCount.
type CastStore struct{ io *IO }

func NewCastStore(io *IO) *CastStore { return &CastStore{io: io} }

const castLedgerPath = "meta/cast_ledger.json"

// Load reads the supporting-cast roster, returning an empty slice when the file does not exist.
func (s *CastStore) Load() ([]domain.CastEntry, error) {
	var entries []domain.CastEntry
	if err := s.io.ReadJSON(castLedgerPath, &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// Save writes the supporting-cast roster wholesale (atomic write).
func (s *CastStore) Save(entries []domain.CastEntry) error {
	return s.io.WriteJSON(castLedgerPath, entries)
}

// MergeAppearances merges this chapter's appearance records into the roster.
//
// Arguments:
//   - chapter: this chapter's number
//   - characters: the array of names appearing in this chapter (from commit_chapter.Characters)
//   - intros: new-character blurbs the Writer declared explicitly (first appearance, or completing BriefRole)
//   - knownCore: the set of core character names already in characters.json (those skip the ledger write)
//
// Behaviour:
//   - name in knownCore: skipped (the core character file is its only recording entry point)
//   - name already in the ledger with chapter already in AppearanceChapters: skipped entirely (idempotent)
//   - name already in the ledger but chapter is new: update LastSeenChapter + append chapter + count++
//   - name not in the ledger: add an entry
//   - a BriefRole from intros is adopted only while the ledger entry's BriefRole is still empty, avoiding overwriting an earlier blurb
func (s *CastStore) MergeAppearances(
	chapter int,
	characters []string,
	intros []domain.CastIntro,
	knownCore map[string]bool,
) error {
	if chapter <= 0 || len(characters) == 0 {
		return nil
	}
	return s.io.WithWriteLock(func() error {
		var entries []domain.CastEntry
		if err := s.io.ReadJSONUnlocked(castLedgerPath, &entries); err != nil && !os.IsNotExist(err) {
			return err
		}

		introMap := make(map[string]string, len(intros))
		for _, in := range intros {
			if in.Name != "" {
				introMap[in.Name] = in.BriefRole
			}
		}

		index := make(map[string]int, len(entries))
		for i, e := range entries {
			index[e.Name] = i
			for _, alias := range e.Aliases {
				index[alias] = i
			}
		}

		seen := make(map[string]bool, len(characters))
		for _, name := range characters {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if knownCore[name] {
				continue
			}
			if i, ok := index[name]; ok {
				entry := &entries[i]
				if !slices.Contains(entry.AppearanceChapters, chapter) {
					entry.AppearanceChapters = append(entry.AppearanceChapters, chapter)
					entry.AppearanceCount = len(entry.AppearanceChapters)
					if chapter > entry.LastSeenChapter {
						entry.LastSeenChapter = chapter
					}
					if chapter < entry.FirstSeenChapter || entry.FirstSeenChapter == 0 {
						entry.FirstSeenChapter = chapter
					}
				}
				if entry.BriefRole == "" {
					if br, ok := introMap[name]; ok && br != "" {
						entry.BriefRole = br
					}
				}
				continue
			}
			entries = append(entries, domain.CastEntry{
				Name:               name,
				BriefRole:          introMap[name],
				FirstSeenChapter:   chapter,
				LastSeenChapter:    chapter,
				AppearanceCount:    1,
				AppearanceChapters: []int{chapter},
			})
		}
		return s.io.WriteJSONUnlocked(castLedgerPath, entries)
	})
}

// RecentActive returns the N most recently active supporting-cast entries (by LastSeenChapter descending).
// It serves novel_context in recalling the "recently appearing supporting cast" the Writer may need when writing the next
// chapter.
//
// Entries already promoted to characters.json (Promoted=true) are skipped, avoiding duplicate recall alongside the core
// files.
func (s *CastStore) RecentActive(limit int) ([]domain.CastEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	active := entries[:0:0]
	for _, e := range entries {
		if e.Promoted {
			continue
		}
		active = append(active, e)
	}
	if len(active) == 0 {
		return nil, nil
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].LastSeenChapter != active[j].LastSeenChapter {
			return active[i].LastSeenChapter > active[j].LastSeenChapter
		}
		return active[i].AppearanceCount > active[j].AppearanceCount
	})
	if len(active) > limit {
		active = active[:limit]
	}
	return active, nil
}
