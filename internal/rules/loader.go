package rules

import (
	"os"
	"path/filepath"
)

// LoadOptions enumerates the directories rules files come from, for RawFileSources to
// scan and normalise.
//
// A missing directory is not an error; the scan skips it silently.
type LoadOptions struct {
	// HomeRulesDir is the ~/.ainovel/rules/ directory; every top-level .md under it is scanned (merged in filename order). Empty means skip.
	HomeRulesDir string

	// ProjectRulesDir is the ./.ainovel/rules/ directory (mirrors the global one; every top-level .md under it is scanned too). Empty means skip.
	ProjectRulesDir string
}

// ainovelDirName is the dotdir name ainovel shares across the user and project levels.
// It makes ~/.ainovel/rules/ and ./.ainovel/rules/ symmetric.
const ainovelDirName = ".ainovel"

// DefaultProjectRulesDir builds the absolute path of ./.ainovel/rules/ from the given
// project directory. The caller passes the project root so the loader never depends on
// the cwd internally; it mirrors DefaultHomeRulesDir.
func DefaultProjectRulesDir(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ainovelDirName, "rules")
}

// DefaultHomeRulesDir builds the absolute path of ~/.ainovel/rules/.
// It returns an empty string when the home directory cannot be resolved, letting the
// caller skip that source.
func DefaultHomeRulesDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ainovelDirName, "rules")
}

// homeRulesReadme is the guide written to ~/.ainovel/rules/README.txt on first run.
// The .txt suffix is deliberate rather than .md — the scan only accepts .md, so this
// guide is never normalised as a rule.
const homeRulesReadme = `Đây là nơi đặt sở thích viết dùng chung, áp dụng cho mọi cuốn sách.

Tạo một file .md mới (ví dụ my-style.md) và viết yêu cầu bằng lời thường —
không cần định dạng gì, không cần YAML:

    # Nhân vật
    - Nhân vật chính Lâm Trần đừng viết kiểu thánh mẫu, ngoài lạnh trong nóng là được
    # Văn phong
    - Dùng cảm giác cơ thể (khớp ngón tay trắng bệch) thay cho nhãn cảm xúc (căng thẳng)
    - Đối thoại đừng quá sách vở, mỗi chương khoảng 3000 chữ
    - Đừng xuất hiện những câu AI kiểu "ở một mức độ nào đó"

Viết xong không cần quan tâm định dạng: hệ thống sẽ dùng model chuẩn hóa những yêu cầu
ngôn ngữ tự nhiên này thành ràng buộc có cấu trúc (khoảng số chữ, từ cấm, ngưỡng từ nhàm...),
tự động tuân thủ khi viết và tự kiểm tra khi commit.

Nhiều file .md được hợp nhất theo thứ tự từ điển của tên file; file ẩn bắt đầu bằng dấu chấm
và file không phải .md đều bị bỏ qua (nên README.txt này không bị coi là quy tắc).

Các câu sáo AI thường gặp và từ nhàm đã có sẵn baseline cơ học, dùng ngay được, không viết cũng không sao.

Thứ tự ưu tiên nạp (cao → thấp): ./.ainovel/rules/*.md (sách này) > ~/.ainovel/rules/*.md (ở đây) > mặc định tích hợp
`

// EnsureHomeRulesDir best-effort creates ~/.ainovel/rules/ and writes the README.txt
// guide, so users discover this global-preference extension point and know how to use it.
// Nice-to-have, not a critical path: a failed home resolution or write is swallowed
// silently and never blocks startup.
func EnsureHomeRulesDir() {
	if dir := DefaultHomeRulesDir(); dir != "" {
		_ = ensureRulesDirAt(dir)
	}
}

// ensureRulesDirAt creates the directory and writes the current README.txt template; it is
// the testable core of EnsureHomeRulesDir.
// README.txt is a system-generated guide (user preferences live in *.md, which is what
// gets scanned), so it is overwritten with the latest template every time — no old content
// is preserved, and therefore no version-compatibility logic is needed.
func ensureRulesDirAt(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(homeRulesReadme), 0o644)
}

// DefaultOptions builds the usual LoadOptions from the current working directory.
//
// It suits a single call at Host startup, letting the user-rules service reuse the same
// source configuration. When resolving the cwd fails, ProjectRulesDir stays empty and the
// scan skips that source.
//
// Path semantics: ProjectRulesDir binds to the **current working directory (cwd)**, not to
// outputDir. A user who cds into a different directory to start a different book gets a
// ./.ainovel/rules/ that follows the cwd naturally; to share across books, put the rules
// in the global ~/.ainovel/rules/ directory (every .md under it is loaded).
func DefaultOptions() LoadOptions {
	cwd, _ := os.Getwd()
	return LoadOptions{
		HomeRulesDir:    DefaultHomeRulesDir(),
		ProjectRulesDir: DefaultProjectRulesDir(cwd),
	}
}
