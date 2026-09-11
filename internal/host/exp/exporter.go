package exp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Run performs one export. It returns synchronously and does little IO (local file reads and
// writes).
//
// Failure semantics:
//   - Invalid deps/opts -> a configuration error, returned at once.
//   - No completed chapters at all -> an error (so the caller is explicit).
//   - A chapter's chapters/{ch}.md missing inside the range -> an error (Progress and the file
//     system disagreeing is a fact-layer bug and should be visible to the user).
//   - The output path already existing without Overwrite -> an error.
//
// Skipped covers the case of "legal but not yet complete inside the range" (the user passes
// to=100 while only 80 are written).
func Run(ctx context.Context, deps Deps, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("exp: deps.Store is nil")
	}

	if opts.Format == "" {
		f, err := inferFormat(opts.OutPath)
		if err != nil {
			return nil, err
		}
		opts.Format = f
	}
	if opts.Format != FormatTXT && opts.Format != FormatEPUB {
		return nil, fmt.Errorf("exp: định dạng chưa được hỗ trợ %q", opts.Format)
	}

	progress, err := deps.Store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("nạp progress thất bại: %w", err)
	}
	if progress == nil || len(progress.CompletedChapters) == 0 {
		return nil, fmt.Errorf("chưa có chương nào hoàn thành, không có nội dung để xuất")
	}
	book, err := deps.Store.Book.Load()
	if err != nil {
		return nil, fmt.Errorf("nạp thông tin tác phẩm thất bại: %w", err)
	}
	if book == nil {
		return nil, fmt.Errorf("thông tin tác phẩm không tồn tại, không thể xuất")
	}

	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	maxCh := 0
	for _, c := range progress.CompletedChapters {
		completed[c] = struct{}{}
		if c > maxCh {
			maxCh = c
		}
	}

	from := opts.From
	if from <= 0 {
		from = 1
	}
	to := opts.To
	if to <= 0 {
		to = maxCh
	}
	if from > to {
		return nil, fmt.Errorf("khoảng chương không hợp lệ: from=%d > to=%d", from, to)
	}

	var chapters, skipped []int
	for ch := from; ch <= to; ch++ {
		if _, ok := completed[ch]; ok {
			chapters = append(chapters, ch)
		} else {
			skipped = append(skipped, ch)
		}
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("khoảng %d..%d không có chương nào hoàn thành", from, to)
	}

	bodies := make(map[int]string, len(chapters))
	for _, ch := range chapters {
		text, err := deps.Store.Drafts.LoadChapterText(ch)
		if err != nil {
			return nil, fmt.Errorf("đọc chương %d thất bại: %w", ch, err)
		}
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("progress đánh dấu chương %d đã hoàn thành, nhưng chapters/%02d.md thiếu hoặc rỗng", ch, ch)
		}
		bodies[ch] = text
	}

	outline, _ := deps.Store.Outline.LoadOutline()
	var volumes []domain.VolumeOutline
	if progress.Layered {
		volumes, _ = deps.Store.Outline.LoadLayeredOutline()
	}

	outPath := opts.OutPath
	if outPath == "" {
		outPath = filepath.Join(deps.Store.Dir(), sanitizeFileName(book.Title)+"."+string(opts.Format))
	}

	if !opts.Overwrite {
		if _, err := os.Stat(outPath); err == nil {
			return nil, fmt.Errorf("file đã tồn tại: %s (thêm --overwrite để ghi đè)", outPath)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("kiểm tra đường dẫn đầu ra thất bại: %w", err)
		}
	}

	titleIdx := buildTitleIndex(outline)
	for _, ch := range chapters {
		summary, err := deps.Store.Summaries.LoadSummary(ch)
		if err != nil {
			return nil, fmt.Errorf("đọc tóm tắt chương %d thất bại: %w", ch, err)
		}
		if summary != nil && strings.TrimSpace(summary.Title) != "" {
			titleIdx[ch] = summary.Title
		}
	}
	var locations map[int]chapterLocation
	if len(volumes) > 0 {
		locations = buildLocations(volumes)
	}

	var data []byte
	switch opts.Format {
	case FormatTXT:
		data = []byte(renderTXT(book.Title, chapters, titleIdx, locations, bodies))
	case FormatEPUB:
		buf, err := renderEPUB(*book, chapters, titleIdx, locations, bodies, epubLang())
		if err != nil {
			return nil, fmt.Errorf("kết xuất EPUB thất bại: %w", err)
		}
		data = buf
	}

	if err := atomicWrite(outPath, data); err != nil {
		return nil, fmt.Errorf("ghi thất bại: %w", err)
	}

	return &Result{
		Path:     outPath,
		Chapters: len(chapters),
		Bytes:    len(data),
		Skipped:  skipped,
	}, nil
}

// inferFormat infers the format from the output path suffix. An empty path falls back to TXT;
// an unknown suffix errors (avoiding a silent mistake).
func inferFormat(path string) (Format, error) {
	if path == "" {
		return FormatTXT, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case "", ".txt":
		return FormatTXT, nil
	case ".epub":
		return FormatEPUB, nil
	default:
		return "", fmt.Errorf("không thể suy ra định dạng từ phần mở rộng %q (hỗ trợ .txt / .epub)", filepath.Ext(path))
	}
}

// atomicWrite has the same shape as store/io.go's WriteFile: tmp + sync + rename.
// store.IO is not reused because the output path may lie outside store.Dir().
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// sanitizeFileName replaces characters that most file systems reject or that read ambiguously.
// It does no aggressive transcoding, blocking only path separators and control characters.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "novel"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		"\x00", "_",
	)
	return replacer.Replace(name)
}
