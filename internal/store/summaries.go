package store

import (
	"fmt"
	"os"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SummaryStore manages chapter, arc and volume summaries.
type SummaryStore struct {
	io         *IO
	outline    *OutlineStore // Read-only dependency used to get arc/volume counts
	titleMu    sync.RWMutex
	titleCache map[int]string
}

func NewSummaryStore(io *IO, outline *OutlineStore) *SummaryStore {
	return &SummaryStore{io: io, outline: outline, titleCache: make(map[int]string)}
}

// SaveSummary saves a chapter summary to summaries/{ch}.json.
func (s *SummaryStore) SaveSummary(sum domain.ChapterSummary) error {
	if err := s.io.WriteJSON(fmt.Sprintf("summaries/%02d.json", sum.Chapter), sum); err != nil {
		return err
	}
	s.titleMu.Lock()
	s.titleCache[sum.Chapter] = sum.Title
	s.titleMu.Unlock()
	return nil
}

// LoadSummary reads the summary for the given chapter.
func (s *SummaryStore) LoadSummary(chapter int) (*domain.ChapterSummary, error) {
	var sum domain.ChapterSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/%02d.json", chapter), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadSummaryTitle reads a chapter title and caches it in-process. A title changes only on SaveSummary, so the same
// chapter summary need not be decoded repeatedly.
func (s *SummaryStore) LoadSummaryTitle(chapter int) (string, error) {
	s.titleMu.RLock()
	title, ok := s.titleCache[chapter]
	s.titleMu.RUnlock()
	if ok {
		return title, nil
	}
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	if title, ok := s.titleCache[chapter]; ok {
		return title, nil
	}
	sum, err := s.LoadSummary(chapter)
	if err != nil || sum == nil {
		return "", err
	}
	s.titleCache[chapter] = sum.Title
	return sum.Title, nil
}

// LoadRecentSummaries loads the summaries of the count chapters immediately before chapter current.
func (s *SummaryStore) LoadRecentSummaries(current, count int) ([]domain.ChapterSummary, error) {
	var result []domain.ChapterSummary
	start := max(current-count, 1)
	for ch := start; ch < current; ch++ {
		sum, err := s.LoadSummary(ch)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveArcSummary saves an arc-level summary.
func (s *SummaryStore) SaveArcSummary(sum domain.ArcSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", sum.Volume, sum.Arc), sum)
}

// HasArcSummary checks whether the given arc already has a summary saved.
func (s *SummaryStore) HasArcSummary(volume, arc int) (bool, error) {
	sum, err := s.LoadArcSummary(volume, arc)
	if err != nil {
		return false, err
	}
	return sum != nil, nil
}

// HasVolumeSummary checks whether the given volume already has a summary saved.
func (s *SummaryStore) HasVolumeSummary(volume int) (bool, error) {
	sum, err := s.LoadVolumeSummary(volume)
	if err != nil {
		return false, err
	}
	return sum != nil, nil
}

// LoadArcSummary reads the summary of the given arc.
func (s *SummaryStore) LoadArcSummary(volume, arc int) (*domain.ArcSummary, error) {
	var sum domain.ArcSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", volume, arc), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadArcSummaries loads every existing arc summary in one volume.
func (s *SummaryStore) LoadArcSummaries(volume int) ([]domain.ArcSummary, error) {
	maxArc := s.arcCountForVolume(volume)
	var result []domain.ArcSummary
	for arc := 1; arc <= maxArc; arc++ {
		sum, err := s.LoadArcSummary(volume, arc)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveVolumeSummary saves a volume-level summary.
func (s *SummaryStore) SaveVolumeSummary(sum domain.VolumeSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/vol-v%02d.json", sum.Volume), sum)
}

// LoadVolumeSummary reads the summary of the given volume.
func (s *SummaryStore) LoadVolumeSummary(volume int) (*domain.VolumeSummary, error) {
	var sum domain.VolumeSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/vol-v%02d.json", volume), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadAllVolumeSummaries loads every existing volume summary.
func (s *SummaryStore) LoadAllVolumeSummaries() ([]domain.VolumeSummary, error) {
	maxVol := s.volumeCount()
	var result []domain.VolumeSummary
	for vol := 1; vol <= maxVol; vol++ {
		sum, err := s.LoadVolumeSummary(vol)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// FindCharacterAppearances batch-finds the last appearing chapter number of several characters.
func (s *SummaryStore) FindCharacterAppearances(names []string, endChapter, recentWindow int) (map[string]int, error) {
	result := make(map[string]int, len(names))
	remaining := make(map[string]struct{}, len(names))
	for _, n := range names {
		remaining[n] = struct{}{}
	}
	for ch := endChapter - recentWindow; ch >= 1; ch-- {
		if len(remaining) == 0 {
			break
		}
		sum, err := s.LoadSummary(ch)
		if err != nil {
			return nil, err
		}
		if sum == nil {
			continue
		}
		for _, c := range sum.Characters {
			if _, need := remaining[c]; need {
				result[c] = ch
				delete(remaining, c)
			}
		}
	}
	return result, nil
}

func (s *SummaryStore) volumeCount() int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil && len(volumes) > 0 {
		return len(volumes)
	}
	return 20
}

func (s *SummaryStore) arcCountForVolume(volume int) int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil {
		for _, v := range volumes {
			if v.Index == volume {
				return len(v.Arcs)
			}
		}
	}
	return 20
}
