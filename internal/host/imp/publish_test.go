package imp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// spyCommitter records the number of Execute calls for publication idempotency/recovery tests.
type spyCommitter struct{ calls int }

func (s *spyCommitter) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	s.calls++
	return json.RawMessage(`{}`), nil
}

func TestCheckFoundationConflictsNormalizesBookMetadata(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "测试书", Synopsis: "测试简介"}); err != nil {
		t.Fatal(err)
	}
	f := &Foundation{Book: domain.BookMetadata{Title: " 测试书 ", Synopsis: " 测试简介 "}}
	if err := checkFoundationConflicts(st, f); err != nil {
		t.Fatalf("规范化后相同的作品信息不应冲突: %v", err)
	}
}

// TestPublishChapterHandlesStalePendingCommit guards recovery from the publication crash window: a crash
// between MarkChapterComplete and ClearPendingCommit leaves a pending_commit pointing at this chapter.
// Skipping a completed chapter outright would bypass the commit tool's cleanup branch, the next
// chapter's Execute would refuse with ErrToolConflict, and the import would die at the same point on
// every rerun — on a residue hit it must still go through the tool's idempotent path once.
func TestPublishChapterHandlesStalePendingCommit(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 100, "mystery", "quest"); err != nil {
		t.Fatal(err)
	}
	f := ImportedChapterFacts{Chapter: 1, Summary: "s", CoreEvent: "c", HookType: "mystery", DominantStrand: "quest"}

	// No residue: a completed chapter is skipped at zero cost without triggering a commit.
	spy := &spyCommitter{}
	if err := publishChapter(context.Background(), st, spy, 1, "正文", f); err != nil {
		t.Fatalf("已完成章应幂等跳过：%v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("无残留不应调用 commit，得 %d 次", spy.calls)
	}

	// The residue points at this chapter: it must go through the commit idempotent path once to complete the cleanup.
	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1}); err != nil {
		t.Fatal(err)
	}
	if err := publishChapter(context.Background(), st, spy, 1, "正文", f); err != nil {
		t.Fatalf("残留清理路径不应失败：%v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("命中残留应恰好调用 commit 一次，得 %d 次", spy.calls)
	}
}
