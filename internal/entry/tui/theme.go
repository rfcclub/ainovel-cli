package tui

import "github.com/charmbracelet/lipgloss"

// Theme palette — warm, bookish
// AdaptiveColor: Light = the value on a light background, Dark = the value on a dark one
//
// Design principle: the Light tier stays fixed (the light background already looks right); the Dark tier is uniformly
// brightened about 25% in lightness against Light with a slightly raised saturation, giving a dark background enough
// contrast (colorDim's old #6b6355 was nearly invisible on #1c1c1c black, making dividers and secondary text vanish).
//
// colorAccent2 on a dark background changed from #7a9e7e to the teal #5fb8a3, separating it from colorSuccess's "healthy
// green" — the two used to be identical, blurring the architect agent's colour tag with the delight of a high hit rate.
// bodyTextColor is the foreground strategy for "neutral prose":
//   - dark terminal → NoColor, inheriting the terminal's default foreground, so we do not force #e8e0d0 off-white into a
//     clash with the user's own warm- or cool-background theme (measured: the dark background's default colour reads
//     better).
//   - light terminal → colorText's Light tier (dark brown #3d3529), keeping the brand's warm tone; plain black on a light
//     background contrasts too harshly, and the tuned dark brown reads softer there.
//
// AdaptiveColor requires a value for both ends and has no "no colour" tier, so the background is judged once at startup
// and every "neutral prose" use — overview values, chapter prose, command descriptions — references bodyTextColor
// uniformly.
var bodyTextColor lipgloss.TerminalColor = func() lipgloss.TerminalColor {
	if lipgloss.HasDarkBackground() {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color("#3d3529")
}()

var (
	colorText    = lipgloss.AdaptiveColor{Light: "#3d3529", Dark: "#e8e0d0"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#8a7e6b", Dark: "#8a8175"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#7a7060", Dark: "#b8b09c"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#e5b449"}
	colorAccent2 = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#5fb8a3"}
	colorRunning = lipgloss.AdaptiveColor{Light: "#6f8641", Dark: "#b5d075"}
	colorSuccess = lipgloss.AdaptiveColor{Light: "#3d7a42", Dark: "#7ec488"}
	colorError   = lipgloss.AdaptiveColor{Light: "#b5433a", Dark: "#e07060"}
	colorReview  = lipgloss.AdaptiveColor{Light: "#b07530", Dark: "#e09b5a"}
	colorContext = lipgloss.AdaptiveColor{Light: "#6b5a9e", Dark: "#a890d8"}
	colorTool    = lipgloss.AdaptiveColor{Light: "#3a7a8a", Dark: "#7ec5d8"}
)

// Status-label colour mapping
var statusColors = map[string]lipgloss.AdaptiveColor{
	"READY":    colorDim,
	"PAUSING":  colorAccent,
	"PAUSED":   colorAccent,
	"RUNNING":  colorRunning,
	"REVIEW":   colorReview,
	"REWRITE":  colorReview,
	"COMPLETE": colorSuccess,
	"ERROR":    colorError,
}

// Status display: icon + label. Consistent with the overall warm theme and avoiding an abrupt solid colour block.
// RUNNING's icon is left empty and filled dynamically by the spinner frame, folding the sense of motion into the status
// indicator itself.
var statusDisplay = map[string]struct {
	icon  string
	label string
}{
	"READY":    {"○", "sẵn sàng"},
	"RUNNING":  {"", "đang chạy"},
	"REVIEW":   {"◆", "thẩm duyệt"},
	"REWRITE":  {"◆", "làm lại"},
	"COMPLETE": {"●", "hoàn thành"},
	"PAUSED":   {"⏸", "tạm dừng"},
	"PAUSING":  {"⏸", "đang tạm dừng"},
	"ERROR":    {"✕", "lỗi"},
}

// Event-category colour mapping
var categoryColors = map[string]lipgloss.AdaptiveColor{
	"DISPATCH": colorAccent,
	"DECISION": colorContext,
	"TOOL":     colorTool,
	"SYSTEM":   colorAccent,
	"USER":     colorAccent2,
	"REVIEW":   colorReview,
	"CHECK":    colorSuccess,
	"ERROR":    colorError,
	"AGENT":    colorMuted,
	"CONTEXT":  colorContext,
	"COMPACT":  colorContext,
}

// Base styles
var (
	baseBorder = lipgloss.RoundedBorder()

	topBarStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(0, 1)

	statusIconStyle = lipgloss.NewStyle().
			Bold(true)

	statusLabelStyle = lipgloss.NewStyle().
				Foreground(colorText)

	panelTitleStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	fieldLabelStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Width(10)

	// fieldValueStyle / cardContentStyle use bodyTextColor — "neutral prose content" such as overview values (run state,
	// completed chapter count, word count), outline entries, character lists and chapter summaries follows the terminal's
	// default foreground on a dark background (avoiding forced off-white clashing with the theme) and the dark brown on a
	// light one, keeping the warm tone. Strongly semantic elements (headings, highlighted values, statuses, errors, hit-rate
	// colouring) still use theme colours such as colorAccent / colorError.
	fieldValueStyle = lipgloss.NewStyle().Foreground(bodyTextColor)

	highlightValueStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	contextUsageMetaStyle = lipgloss.NewStyle().
				Foreground(colorDim)

	cardTitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	cardContentStyle = lipgloss.NewStyle().Foreground(bodyTextColor)
)
