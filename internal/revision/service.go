package revision

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type StyleIndex interface {
	ChapterCommitted(chapter int, text string)
}

var errPreparedStale = errors.New("phân tích tu chỉnh đã hết hiệu lực")

type Result struct {
	Changed  []int
	Applied  []int
	Analyses []domain.RevisionAnalysis
}

type Service struct {
	store      *store.Store
	model      agentcore.ChatModel
	prompt     string
	styleIndex StyleIndex
}

func NewService(st *store.Store, model agentcore.ChatModel, prompt string, styleIndex StyleIndex) *Service {
	return &Service{store: st, model: model, prompt: prompt, styleIndex: styleIndex}
}

func (s *Service) Sync(ctx context.Context) (*Result, error) {
	pending, err := s.store.Revisions.LoadPending()
	if err != nil {
		return nil, fmt.Errorf("đọc bản ghi khôi phục tu chỉnh: %w", err)
	}
	if pending != nil {
		return s.applyPending(*pending)
	}

	changes, err := Scan(s.store)
	if err != nil {
		return nil, err
	}
	result := &Result{Changed: ChangedChapters(changes)}
	if len(changes) == 0 {
		return result, nil
	}

	items := make([]domain.PendingRevisionItem, 0, len(changes))
	proposedSummaries := make(map[int]domain.ChapterSummary, len(changes))
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		previous, err := s.store.ChapterRecords.Load(change.Chapter)
		if err != nil || previous == nil {
			if err == nil {
				err = fmt.Errorf("bản ghi tiếp nhận không tồn tại")
			}
			return nil, fmt.Errorf("đọc đường cơ sở chương %d: %w", change.Chapter, err)
		}
		downstream, err := s.downstreamSummaries(change.Chapter, proposedSummaries)
		if err != nil {
			return nil, err
		}
		analysis, err := Analyze(ctx, s.model, s.prompt, change, *previous, downstream)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		record := domain.ChapterRecord{
			Version: domain.ChapterRecordVersion, Chapter: change.Chapter, Revision: previous.Revision + 1,
			Origin: domain.ChapterOriginUser, Content: change.After, ContentSHA256: change.CurrentSHA256,
			Facts: analysis.Facts, StyleDelta: domain.MergeStyleDelta(previous.StyleDelta, analysis.StyleDelta), AcceptedAt: now,
		}
		items = append(items, domain.PendingRevisionItem{
			Chapter: change.Chapter, BaseSHA256: change.BaseSHA256, CurrentSHA256: change.CurrentSHA256,
			Record: record, Analysis: analysis,
		})
		proposedSummaries[change.Chapter] = domain.ChapterSummary{
			Chapter: change.Chapter, Title: analysis.Facts.Title, Summary: analysis.Facts.Summary,
			Characters: analysis.Facts.Characters, KeyEvents: analysis.Facts.KeyEvents,
		}
	}
	slices.SortFunc(items, func(a, b domain.PendingRevisionItem) int { return a.Chapter - b.Chapter })
	for _, item := range items {
		result.Analyses = append(result.Analyses, item.Analysis)
	}
	if err := s.validatePendingRecords(items); err != nil {
		return nil, fmt.Errorf("kiểm tra trạng thái chương sau tu chỉnh: %w", err)
	}
	now := time.Now()
	pending = &domain.PendingRevision{Stage: domain.RevisionStagePrepared, Items: items, StartedAt: now, UpdatedAt: now}
	if err := s.store.Revisions.SavePending(*pending); err != nil {
		return nil, fmt.Errorf("lưu bản ghi khôi phục tu chỉnh: %w", err)
	}
	applied, err := s.applyPending(*pending)
	if err != nil {
		return nil, err
	}
	applied.Changed = result.Changed
	applied.Analyses = result.Analyses
	return applied, nil
}

func (s *Service) applyPending(pending domain.PendingRevision) (*Result, error) {
	switch pending.Stage {
	case domain.RevisionStagePrepared:
		if err := s.applyPreparedRecords(pending.Items); err != nil {
			if errors.Is(err, errPreparedStale) {
				if clearErr := s.store.Revisions.ClearPending(); clearErr != nil {
					return nil, fmt.Errorf("%v; dọn phân tích tu chỉnh hết hiệu lực thất bại: %w", err, clearErr)
				}
			}
			return nil, err
		}
		pending.Stage = domain.RevisionStageRecordsApplied
		pending.UpdatedAt = time.Now()
		if err := s.store.Revisions.SavePending(pending); err != nil {
			return nil, err
		}
		fallthrough
	case domain.RevisionStageRecordsApplied:
		progress, err := s.store.Progress.Load()
		if err != nil || progress == nil {
			if err == nil {
				err = fmt.Errorf("progress chưa được khởi tạo")
			}
			return nil, err
		}
		records, err := s.store.ChapterRecords.LoadCompleted(progress.CompletedChapters)
		if err != nil {
			return nil, err
		}
		if err := NewProjector(s.store).Apply(records); err != nil {
			return nil, fmt.Errorf("dựng lại trạng thái dẫn xuất của chương: %w", err)
		}
		if len(pending.Items) > 0 {
			if err := s.store.InvalidateChapterAggregates(pending.Items[0].Chapter); err != nil {
				return nil, fmt.Errorf("vô hiệu hóa trạng thái dẫn xuất cấp cao sau tu chỉnh: %w", err)
			}
		}
		for _, item := range pending.Items {
			if slices.Contains(progress.PendingRewrites, item.Chapter) {
				if err := s.store.Progress.CompleteRewrite(item.Chapter); err != nil {
					return nil, fmt.Errorf("hoàn tất làm lại thủ công chương %d: %w", item.Chapter, err)
				}
			}
		}
		for _, item := range pending.Items {
			analysis := item.Analysis
			if analysis.StoryChanged || analysis.OutlineImpact != nil || len(analysis.DownstreamIssues) > 0 {
				feedback := store.ChapterFeedback{
					Chapter: item.Chapter, StoryChanged: analysis.StoryChanged,
					ChangeSummary: analysis.ChangeSummary, DownstreamIssues: analysis.DownstreamIssues,
				}
				if analysis.OutlineImpact != nil {
					feedback.Deviation = analysis.OutlineImpact.Deviation
					feedback.Suggestion = analysis.OutlineImpact.Suggestion
				}
				if err := s.store.Outline.AppendOutlineFeedback(feedback); err != nil {
					return nil, fmt.Errorf("lưu ảnh hưởng đại cương của chương %d: %w", item.Chapter, err)
				}
			}
		}
		pending.Stage = domain.RevisionStageProjectionsApplied
		pending.UpdatedAt = time.Now()
		if err := s.store.Revisions.SavePending(pending); err != nil {
			return nil, err
		}
		fallthrough
	case domain.RevisionStageProjectionsApplied:
		for _, item := range pending.Items {
			if _, err := s.store.Checkpoints.AppendArtifact(domain.ChapterScope(item.Chapter), "revision_sync", store.ChapterRecordPath(item.Chapter)); err != nil {
				return nil, fmt.Errorf("ghi checkpoint tu chỉnh chương %d: %w", item.Chapter, err)
			}
		}
	default:
		return nil, fmt.Errorf("giai đoạn khôi phục tu chỉnh không xác định %q", pending.Stage)
	}
	if err := s.store.Revisions.ClearPending(); err != nil {
		return nil, fmt.Errorf("dọn bản ghi khôi phục tu chỉnh: %w", err)
	}
	result := &Result{}
	for _, item := range pending.Items {
		result.Applied = append(result.Applied, item.Chapter)
		result.Analyses = append(result.Analyses, item.Analysis)
		if s.styleIndex != nil {
			s.styleIndex.ChapterCommitted(item.Chapter, item.Record.Content)
		}
	}
	return result, nil
}

func (s *Service) applyPreparedRecords(items []domain.PendingRevisionItem) error {
	for _, item := range items {
		content, err := s.store.Drafts.LoadChapterText(item.Chapter)
		if err != nil {
			return fmt.Errorf("đọc chính văn workspace của chương %d: %w", item.Chapter, err)
		}
		if domain.ChapterContentSHA256(content) != item.CurrentSHA256 {
			return fmt.Errorf("%w: chương %d lại thay đổi sau khi phân tích, hãy chạy lại /sync", errPreparedStale, item.Chapter)
		}
		current, err := s.store.ChapterRecords.Load(item.Chapter)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("bản ghi tiếp nhận chương %d không tồn tại", item.Chapter)
		}
		switch {
		case sameChapterRecord(*current, item.Record):
			continue
		case current.ContentSHA256 == item.BaseSHA256 && current.Revision+1 == item.Record.Revision:
			if err := s.store.ChapterRecords.Save(item.Record); err != nil {
				return fmt.Errorf("tiếp nhận tu chỉnh chương %d: %w", item.Chapter, err)
			}
		default:
			return fmt.Errorf("%w: bản ghi tiếp nhận chương %d thay đổi sau khi phân tích, hãy chạy lại /sync", errPreparedStale, item.Chapter)
		}
	}
	return nil
}

func sameChapterRecord(a, b domain.ChapterRecord) bool {
	return a.Version == b.Version && a.Chapter == b.Chapter && a.Revision == b.Revision &&
		a.Origin == b.Origin && a.Content == b.Content && a.ContentSHA256 == b.ContentSHA256 &&
		reflect.DeepEqual(a.Facts, b.Facts) && reflect.DeepEqual(a.StyleDelta, b.StyleDelta) &&
		a.AcceptedAt.Equal(b.AcceptedAt)
}

func (s *Service) validatePendingRecords(items []domain.PendingRevisionItem) error {
	progress, err := s.store.Progress.Load()
	if err != nil || progress == nil {
		if err == nil {
			err = fmt.Errorf("progress chưa được khởi tạo")
		}
		return err
	}
	records, err := s.store.ChapterRecords.LoadCompleted(progress.CompletedChapters)
	if err != nil {
		return err
	}
	replacements := make(map[int]domain.ChapterRecord, len(items))
	for _, item := range items {
		replacements[item.Chapter] = item.Record
	}
	for i, record := range records {
		if replacement, ok := replacements[record.Chapter]; ok {
			records[i] = replacement
		}
	}
	return ValidateRecords(records)
}

func (s *Service) downstreamSummaries(chapter int, proposed map[int]domain.ChapterSummary) ([]domain.ChapterSummary, error) {
	progress, err := s.store.Progress.Load()
	if err != nil {
		return nil, err
	}
	if progress == nil {
		return nil, fmt.Errorf("progress chưa được khởi tạo")
	}
	chapters := slices.Clone(progress.CompletedChapters)
	slices.Sort(chapters)
	var summaries []domain.ChapterSummary
	for _, current := range chapters {
		if current <= chapter {
			continue
		}
		if summary, ok := proposed[current]; ok {
			summaries = append(summaries, summary)
			continue
		}
		summary, err := s.store.Summaries.LoadSummary(current)
		if err != nil {
			return nil, fmt.Errorf("đọc tóm tắt chương %d: %w", current, err)
		}
		if summary == nil {
			return nil, fmt.Errorf("chương %d thiếu tóm tắt, không thể phân tích đầy đủ ảnh hưởng về sau", current)
		}
		summaries = append(summaries, *summary)
	}
	return summaries, nil
}
