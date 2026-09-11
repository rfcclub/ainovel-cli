package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/host"
)

const resetForeground = "\x1b[39m"

// highlightCommandToken colours only the confirmed command token, preserving the textarea's own cursor, reverse video and
// newline ANSI sequences. Arguments start at the first whitespace character and always use the prose colour.
func highlightCommandToken(inputView, inputValue, commandToken string) string {
	if commandToken == "" {
		return inputView
	}
	fields := strings.Fields(inputValue)
	if len(fields) == 0 || fields[0] != commandToken {
		return inputView
	}
	plain := ansi.Strip(inputView)
	start := strings.Index(plain, commandToken)
	if start < 0 {
		return inputView
	}
	return highlightANSIByteRange(inputView, start, start+len(commandToken))
}

// highlightANSIByteRange overlays a foreground colour on a byte range after ANSI is stripped. If the textarea's own SGR
// appears inside the range (a reverse-video cursor, say), the emphasis colour is re-issued after it; the range's end resets
// only the foreground, leaving the cursor's other terminal attributes intact.
func highlightANSIByteRange(value string, start, end int) string {
	if start < 0 || end <= start {
		return value
	}
	marker := lipgloss.NewStyle().Foreground(colorAccent).Render("x")
	markerAt := strings.IndexByte(marker, 'x')
	if markerAt <= 0 {
		return value
	}
	accent := marker[:markerAt]

	var out strings.Builder
	out.Grow(len(value) + len(accent)*2 + len(resetForeground))
	plainPos := 0
	active := false
	var state byte
	for len(value) > 0 {
		sequence, _, size, nextState := ansi.DecodeSequence(value, state, nil)
		state = nextState
		plain := ansi.Strip(sequence)
		if plain == "" {
			out.WriteString(sequence)
			if active {
				out.WriteString(accent)
			}
			value = value[size:]
			continue
		}
		if !active && plainPos == start {
			out.WriteString(accent)
			active = true
		}
		out.WriteString(sequence)
		value = value[size:]
		plainPos += len(plain)
		if active && plainPos >= end {
			out.WriteString(resetForeground)
			active = false
		}
	}
	if active {
		out.WriteString(resetForeground)
	}
	return out.String()
}

// renderInputBox renders the bottom input area: the input box, the shortcut hint row and the usage status bar at the very bottom.
// The input box handles input and hints alone and does not carry the startup mode bar.
func renderInputBox(inputView, hints string, snap host.UISnapshot, outputDir string, width int) string {
	innerW := width - 4 // border + padding
	if innerW < 12 {
		innerW = 12
	}

	// Input row: prompt symbol + input box
	prompt := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("❯ ")
	inputLine := prompt + inputView

	// Hint row: the shortcuts take the whole line — run information such as model/cost moved to the bottom status bar instead of crowding the right and truncating each other.
	line2 := fitInlineLine(hints, innerW)

	// Input area (a single box, avoiding the look of two input boxes)
	inputStyle := lipgloss.NewStyle().
		Width(width).
		Border(baseBorder, true, false, true, false).
		BorderForeground(colorDim).
		Padding(0, 1)
	inputBlock := inputStyle.Render(inputLine)

	// Hint row (borderless, hugging just below the bottom rule)
	hintStyle := lipgloss.NewStyle().
		Width(width).
		Padding(0, 2)
	hintBlock := hintStyle.Render(line2)

	// The status bar takes over the trailing blank line the input area already had: the block keeps its height and layoutHeights needs no adjustment.
	statusBlock := hintStyle.Render(renderStatusBar(snap, outputDir, innerW))

	return inputBlock + "\n" + hintBlock + "\n" + statusBlock
}

func joinInlineSides(left, right string, width int) string {
	if width <= 0 {
		return left + right
	}
	if strings.TrimSpace(right) == "" {
		return fitInlineLine(left, width)
	}

	right = fitInlineLine(right, width)
	rightW := ansi.StringWidth(right)
	if rightW >= width {
		return right
	}

	leftMax := width - rightW - 1
	if leftMax < 0 {
		leftMax = 0
	}
	left = fitInlineLine(left, leftMax)
	gap := width - ansi.StringWidth(left) - rightW
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func fitInlineLine(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(text) <= width {
		return text
	}
	return ansi.Truncate(text, width, "...")
}
