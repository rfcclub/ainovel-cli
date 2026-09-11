package tui

import (
	"fmt"
	"math"

	"github.com/charmbracelet/lipgloss"
)

// --- Helper functions ---

func renderField(label, value string) string {
	if value == "" {
		value = "-"
	}
	return fieldLabelStyle.Render(label) + fieldValueStyle.Render(value) + "\n"
}

func renderHighlightField(label, value string) string {
	return fieldLabelStyle.Render(label) + highlightValueStyle.Render(value) + "\n"
}

// contextPercentColor returns a health-gradient color based on context usage.
// Mirrors Claude Code's calculateTokenWarningState concept:
//   - < 70%: green (healthy headroom)
//   - 70-85%: yellow (approaching compression threshold)
//   - > 85%: red (compression imminent or active)
func contextPercentColor(percent float64) lipgloss.AdaptiveColor {
	switch {
	case percent >= 85:
		return colorError
	case percent >= 70:
		return colorReview
	default:
		return colorSuccess
	}
}

// formatContextWindow formats a token count into a compact window marker: "128K" / "200K" / "1M" / "2M".
// A technical 1M such as Gemini's 1048576 (2^20) shows as "1M" rather than "1.0M".
// n<=0 returns an empty string, on which the caller should decide whether to display it at all.
func formatContextWindow(n int) string {
	if n <= 0 {
		return ""
	}
	if n >= 1_000_000 {
		m := float64(n) / 1_000_000
		rounded := math.Round(m)
		if rounded > 0 && math.Abs(m-rounded)/rounded < 0.05 {
			return fmt.Sprintf("%dM", int(rounded))
		}
		return fmt.Sprintf("%.1fM", m)
	}
	if n >= 1000 {
		return fmt.Sprintf("%dK", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// formatCostUSD formats a dollar cost. Under $0.01 uses 4 decimals, otherwise 2. Zero returns empty.
func formatCostUSD(usd float64) string {
	if usd <= 0 {
		return ""
	}
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

func formatNumber(n int) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	result := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// truncate truncates by visual width (Chinese characters count as 2 columns), ending with "..." when over-wide.
// It cannot truncate by rune count: a purely Chinese line would overflow by nearly twice the column width and be hard-clipped
// at the edge by the outer viewport, ellipsis included, leaving the user with "text cut at the edge, no wrapping".
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	if max < 4 {
		return truncateWidth(s, max)
	}
	return truncateWidth(s, max-3) + "..."
}
