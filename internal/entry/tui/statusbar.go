package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/host"
)

// renderStatusBar renders the usage status bar at the very bottom of the screen, taking over the trailing blank line the input area already had (zero extra height):
//
//	◆ provider model(window,thinking) │ ↑input ↓output ⚡recent cache hits │ cost(/budget) saved X    ./book dir
//
// Its purpose is "see the cost at a glance": the model identity you are paying for, session cumulative tokens, cost and the
// budget-approaching warning. The data comes from the UISnapshot polled every 3s (each completed model call's usage is
// accrued immediately); per-role/per-model detail and cache diagnostics stay in the left panel and are not repeated here.
func renderStatusBar(snap host.UISnapshot, outputDir string, width int) string {
	dim := lipgloss.NewStyle().Foreground(colorDim)
	val := lipgloss.NewStyle().Foreground(colorMuted)

	var segs []string
	if snap.ModelName != "" {
		s := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("◆") + " "
		if snap.Provider != "" {
			s += dim.Render(snap.Provider) + " "
		}
		s += val.Render(snap.ModelName)
		if suffix := modelInfoSuffix(snap); suffix != "" {
			s += dim.Render("(" + suffix + ")")
		}
		segs = append(segs, s)
	}
	if snap.TotalInputTokens > 0 || snap.TotalOutputTokens > 0 {
		s := dim.Render("↑") + val.Render(formatTokensCompact(snap.TotalInputTokens)) +
			" " + dim.Render("↓") + val.Render(formatTokensCompact(snap.TotalOutputTokens))
		// The recent hit rate is shown only when the model genuinely supports prompt caching and samples exist, avoiding the "0% needs investigating" misreading.
		if snap.OverallCacheCapable && snap.OverallRecentSamples > 0 && snap.OverallRecentInput > 0 {
			rate := cacheHitRate(snap.OverallRecentCacheRead, snap.OverallRecentInput)
			s += " " + lipgloss.NewStyle().Foreground(cacheHitColor(rate)).Render("⚡"+formatPercent(rate))
		}
		segs = append(segs, s)
	}
	if snap.TotalCostUSD > 0 || snap.BudgetLimitUSD > 0 {
		cost := formatCostUSD(snap.TotalCostUSD)
		if cost == "" {
			cost = "$0"
		}
		style := val
		if snap.BudgetLimitUSD > 0 {
			// A budget being approached or exceeded uses the warning colour — the status bar is always visible and is where the budget most deserves to be seen.
			switch ratio := snap.TotalCostUSD / snap.BudgetLimitUSD; {
			case ratio >= 1:
				style = lipgloss.NewStyle().Foreground(colorError).Bold(true)
			case ratio >= 0.8:
				style = lipgloss.NewStyle().Foreground(colorReview)
			}
		}
		s := style.Render(cost)
		if snap.BudgetLimitUSD > 0 {
			s += dim.Render("/" + formatCostUSD(snap.BudgetLimitUSD))
		}
		if saved := formatCostUSD(snap.TotalSavedUSD); saved != "" {
			s += dim.Render(" (tiết kiệm " + saved + ")")
		}
		segs = append(segs, s)
	}

	left := strings.Join(segs, dim.Render(" │ "))
	var right string
	if outputDir != "" {
		right = dim.Render("./" + filepath.Base(outputDir))
	}
	if left == "" && right == "" {
		return dim.Render("SẴN SÀNG")
	}
	return joinInlineSides(left, right, width)
}

// modelInfoSuffix assembles the model parenthetical: context window + thinking level, such as "200K,med".
func modelInfoSuffix(snap host.UISnapshot) string {
	var parts []string
	if w := formatContextWindow(snap.ModelContextWindow); w != "" {
		parts = append(parts, w)
	}
	if t := formatThinkingLevel(snap.ThinkingLevel); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, ",")
}

func formatThinkingLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "":
		return "auto"
	case "medium":
		return "med"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}
