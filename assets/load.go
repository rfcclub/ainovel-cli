package assets

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/tools"
)

//go:embed prompts
var promptsFS embed.FS

//go:embed references
var referencesFS embed.FS

//go:embed styles
var stylesFS embed.FS

//go:embed voice.md
var voiceFS embed.FS

// Prompts biểu thị tập hợp các prompt được nhúng.
type Prompts struct {
	ArchitectShort   string
	ArchitectLong    string
	Writer           string // Giao thức khuôn mẫu, chứa placeholder {{VOICE}}; bản cuối ráp qua BuildWriterPrompt
	Editor           string
	ImportSegment    string // Phân tách ngữ nghĩa: nhận diện ranh giới chương/tập/phần phụ
	ImportAnalyze    string // Trích xuất sự thật từng chương
	ImportSynthesize string // Tổng hợp phân tầng và phân chia tập/cung toàn sách (BookSynthesis)
	ImportRange      string // Tóm tắt khoảng liên tục giai đoạn Map (RangeDigest)
	SimulationSource string
	SimulationMerge  string
	RevisionAnalyze  string

	// Arbiter tài phán (LLM-as-function, không bọc simulation guidance)
	ArbiterPlanStart    string
	ArbiterIntervention string
	ArbiterFailure      string
}

// Bundle represents the static resources needed at runtime.
type Bundle struct {
	References tools.References
	Prompts    Prompts
	Styles     map[string]string
	Voice      string // Voice standard, assembled through three override layers
	Language   string // Always "vi": the codebase carries a Vietnamese-only pipeline
	// ContentRating is the declared adult-content level ("general" / "mature" /
	// "explicit"); applied to role prompts through WithRating.
	ContentRating string
}

// WithRating applies the bundle's content rating to a role prompt. Every role that
// produces or plans prose goes through here so the rating is stated once, in the
// system prompt, instead of being re-derived per call site.
func (b Bundle) WithRating(prompt string) string {
	return WithContentRating(prompt, b.ContentRating)
}

// LoadOptions declares the override sources for the voice layer, plus the content
// rating the writer must respect.
type LoadOptions struct {
	BookStyleDir string // <outputDir>/style
	HomeStyleDir string // ~/.ainovel/style
	// ContentRating is the declared adult-content level: "general" (default),
	// "mature" or "explicit". Empty means general. It is appended to the writer
	// prompt so the model is told the level instead of guessing.
	ContentRating string
}

// DefaultLoadOptions khởi tạo nguồn ghi đè dựa trên thư mục sách.
func DefaultLoadOptions(outputDir string) LoadOptions {
	var opts LoadOptions
	if outputDir != "" {
		opts.BookStyleDir = filepath.Join(outputDir, "style")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		opts.HomeStyleDir = filepath.Join(home, ".ainovel", "style")
	}
	return opts
}

// Load trả về tài nguyên tương ứng với style và ngôn ngữ truyện (mặc định vi).
func Load(style string, opts LoadOptions) Bundle {
	return LoadWithLanguage("vi", style, opts)
}

// LoadWithLanguage loads resources for the given content language and style.
//
// The pipeline is Vietnamese-only: language is accepted for call-site stability
// but every resource resolves to the Vietnamese set. Use Load when the caller has
// no language value at hand.
func LoadWithLanguage(_, style string, opts LoadOptions) Bundle {
	return Bundle{
		References:    loadReferences(style, opts),
		Prompts:       loadPrompts(),
		Styles:        loadStyles(opts),
		Voice:         resolveAppendable(loadVoice(), "voice.md", opts),
		Language:      "vi",
		ContentRating: bootstrap.NormalizeContentRating(opts.ContentRating),
	}
}

const voicePlaceholder = "{{VOICE}}"

// BuildWriterPrompt is the single assembly gate for the Writer prompt.
func BuildWriterPrompt(writerPrompt, voice, style string) string {
	out := strings.Replace(writerPrompt, voicePlaceholder, strings.TrimSpace(voice), 1)
	if style != "" {
		out += "\n\n" + style
	}
	return out
}

// WithContentRating appends the content-rating directive to a role prompt. The rating
// is a declaration by the (adult) operator about their own work; the system only records
// it so the model does not invent its own ceiling. An empty or general rating appends
// nothing — the default posture stays the conservative one.
func WithContentRating(prompt, rating string) string {
	if section := ContentRatingSection(rating); section != "" {
		return prompt + "\n\n" + section
	}
	return prompt
}

// ContentRatingSection returns the Vietnamese directive block for a rating, or "" for
// the general level (nothing to add when no adult content is requested).
func ContentRatingSection(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case bootstrap.ContentRatingMature:
		return contentRatingMatureVI
	case bootstrap.ContentRatingExplicit:
		return contentRatingExplicitVI
	default:
		return ""
	}
}

const contentRatingMatureVI = `## Mức nội dung: người lớn (mature)

Người dùng đã khai báo tác phẩm này hướng tới độc giả trưởng thành. Các chủ đề nặng — bạo lực, tình dục, nghiện, chấn thương tâm lý, tội ác — được phép xuất hiện và được viết thẳng thắn, không né tránh, không cắt cảnh giả tạo khi tình tiết cần.

Ranh giới:
- Miêu tả phục vụ nhân vật và cốt truyện: cảm giác, hệ quả, cái giá phải trả — không miêu tả trơ trẽn chỉ để gây kích thích.
- Cảnh thân mật đi tới mức rõ ràng về ham muốn và hành vi, nhưng không cần đặc tả chi tiết giải phẫu từng bước.
- Không hạ thấp nhân vật thành công cụ; đồng thuận và hệ quả cảm xúc vẫn phải có chỗ đứng.
- Vẫn tuyệt đối không viết nội dung tình dục liên quan trẻ vị thành niên, và không biến bạo lực thành hướng dẫn có thể làm theo ngoài đời.`

const contentRatingExplicitVI = `## Mức nội dung: miêu tả thẳng (explicit)

Người dùng đã khai báo tác phẩm này dành cho độc giả trưởng thành và cho phép miêu tả thẳng. Khi tình tiết dẫn tới cảnh thân mật hoặc cảnh bạo lực, hãy viết trọn vẹn: nhịp điệu, giác quan, ham muốn, lời nói, hệ quả — không dùng cắt cảnh, không dùng ẩn dụ thay thế, không tự kiểm duyệt giữa chừng.

Ranh giới còn lại (không thương lượng):
- Đây là tác phẩm hư cấu giữa các nhân vật người lớn. Tuyệt đối không có nội dung tình dục liên quan trẻ vị thành niên, dù là gợi ý, hồi tưởng hay ẩn dụ.
- Không miêu tả bạo lực như một bản hướng dẫn có thể làm theo ngoài đời (công thức chế tạo, liều lượng, quy trình gây hại cụ thể).
- Đồng thuận phải tồn tại trong truyện; cưỡng bức chỉ được viết như một bi kịch có hệ quả, không phải cảnh hưởng thụ.
- Giữ đúng giọng văn và nhân vật: miêu tả thẳng không có nghĩa là thô lỗ, lặp từ hay mất kiểm soát nhịp điệu.`


// OverrideVoice thay thế đoạn văn phong đã ráp (phục vụ thử nghiệm A/B).
func (b *Bundle) OverrideVoice(raw string) {
	b.Voice = raw
}

func resolveAppendable(builtin, name string, opts LoadOptions) string {
	out := builtin
	if s := readOverride(opts.HomeStyleDir, name); s != "" {
		out += "\n\n## Người dùng ghi đè văn phong toàn cục (User Global Style Override)\n\n" + s
	}
	if s := readOverride(opts.BookStyleDir, name); s != "" {
		out += "\n\n## Ghi đè văn phong cuốn sách này (Book Style Override)\n\n" + s
	}
	return out
}

func readOverride(dir, name string) string {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

var styleNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func loadVoice() string {
	return mustRead(voiceFS, "voice.md")
}

func loadReferences(style string, opts LoadOptions) tools.References {
	if style == "" {
		style = "default"
	}
	readRef := func(rel string) string {
		return mustRead(referencesFS, "references/"+rel)
	}

	refs := tools.References{
		ChapterGuide:      readRef("chapter-guide.md"),
		HookTechniques:    readRef("hook-techniques.md"),
		QualityChecklist:  readRef("quality-checklist.md"),
		OutlineTemplate:   readRef("outline-template.md"),
		CharacterTemplate: readRef("character-template.md"),
		ChapterTemplate:   readRef("chapter-template.md"),
		Consistency:       readRef("consistency.md"),
		ContentExpansion:  readRef("content-expansion.md"),
		DialogueWriting:   readRef("dialogue-writing.md"),
		LongformPlanning:  readRef("longform-planning.md"),
		Differentiation:   readRef("differentiation.md"),
		AntiAITone:        resolveAppendable(readRef("anti-ai-tone.md"), "anti-ai-tone.md", opts),
	}
	if style != "" && style != "default" {
		genreDir := "references/genres/" + style + "/"
		refs.StyleReference = readRef("genres/" + style + "/style-references.md")
		refs.ArcTemplates = readRef("genres/" + style + "/arc-templates.md")
		_ = genreDir
		relPath := filepath.Join("genres", style, "style-references.md")
		for _, dir := range []string{opts.HomeStyleDir, opts.BookStyleDir} {
			if s := readOverride(dir, relPath); s != "" {
				refs.StyleReference = s
			}
		}
	}
	return refs
}

func loadPrompts() Prompts {
	readPrompt := func(filename string) string {
		return mustRead(promptsFS, "prompts/"+filename)
	}

	return Prompts{
		ArchitectShort:   WithSimulationGuidance(readPrompt("architect-short.md"), "architect"),
		ArchitectLong:    WithSimulationGuidance(readPrompt("architect-long.md"), "architect"),
		Writer:           WithSimulationGuidance(readPrompt("writer.md"), "writer"),
		Editor:           WithSimulationGuidance(readPrompt("editor.md"), "editor"),
		ImportSegment:    readPrompt("import-segment.md"),
		ImportAnalyze:    readPrompt("import-analyze.md"),
		ImportSynthesize: readPrompt("import-synthesize.md"),
		ImportRange:      readPrompt("import-range.md"),
		SimulationSource: readPrompt("simulation-source.md"),
		SimulationMerge:  readPrompt("simulation-merge.md"),
		RevisionAnalyze:  readPrompt("revision-analyze.md"),

		ArbiterPlanStart:    readPrompt("arbiter-plan-start.md"),
		ArbiterIntervention: readPrompt("arbiter-intervention.md"),
		ArbiterFailure:      readPrompt("arbiter-failure.md"),
	}
}

// WithSimulationGuidance appends the per-role simulation guidance to a prompt.
func WithSimulationGuidance(prompt, role string) string {
	return prompt + "\n\n" + strings.ReplaceAll(simulationGuidanceVI, "{{role}}", role)
}

// OverridePrompt ghi đè prompt của vai trò cụ thể.
func (b *Bundle) OverridePrompt(file, raw string) error {
	role, ok := promptRole[file]
	if !ok {
		return fmt.Errorf("không hỗ trợ ghi đè file prompt: %s (chỉ có thể ghi đè prompt vai trò cốt lõi)", file)
	}
	wrapped := WithSimulationGuidance(raw, role)
	switch file {
	case "architect-short.md":
		b.Prompts.ArchitectShort = wrapped
	case "architect-long.md":
		b.Prompts.ArchitectLong = wrapped
	case "writer.md":
		b.Prompts.Writer = wrapped
	case "editor.md":
		b.Prompts.Editor = wrapped
	}
	return nil
}

var promptRole = map[string]string{
	"architect-short.md": "architect",
	"architect-long.md":  "architect",
	"writer.md":          "writer",
	"editor.md":          "editor",
}

const simulationGuidanceVI = `## Hồ sơ mô phỏng văn phong (Simulation Profile)

Khi trong planning_memory hoặc working_memory của novel_context xuất hiện simulation_profile, bắt buộc phải xem đó là ràng buộc định hướng mô phỏng của tác phẩm hiện tại. {{role}} cần đọc kỹ các trường style, lexicon, plot_design, hook_design, pacing_density, reader_engagement và role_guidance.

Nguyên tắc sử dụng: Học hỏi cấu trúc, nhịp điệu, móc câu, cách giải phóng thông tin và thủ pháp cuốn hút độc giả; tuyệt đối không sao chép câu văn nguyên văn, tên nhân vật, địa danh, thiết lập độc quyền hay phân đoạn cố định. Nếu simulation_profile xung đột với yêu cầu rõ ràng của người dùng, ưu tiên tuân thủ yêu cầu của người dùng.`

func loadStyles(opts LoadOptions) map[string]string {
	styles := make(map[string]string)
	const prefix = "styles"
	entries, err := stylesFS.ReadDir(prefix)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			data, err := stylesFS.ReadFile(prefix + "/" + e.Name())
			if err != nil {
				continue
			}
			styles[name] = string(data)
		}
	}
	for _, dir := range []string{opts.HomeStyleDir, opts.BookStyleDir} {
		overlayStyles(styles, dir)
	}
	return styles
}

func overlayStyles(styles map[string]string, dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(filepath.Join(dir, "styles"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if !styleNameRe.MatchString(name) {
			slog.Warn("Bỏ qua tên file style không hợp lệ", "module", "assets", "dir", dir, "file", e.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "styles", e.Name()))
		if err != nil {
			continue
		}
		styles[name] = string(data)
	}
}

func mustRead(fs embed.FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embed read %s: %v", path, err))
	}
	return string(data)
}
