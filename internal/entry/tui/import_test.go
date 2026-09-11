package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/ainovel-cli/internal/host/imp"
)

// TestImportHistoryCoalescesRetryLines guards in-place updates of retry rows: consecutive events under
// one Key occupy a single row (the "attempt N" ticking in place) and start a new row once an ordinary
// progress row separates them, preserving time order.
func TestImportHistoryCoalescesRetryLines(t *testing.T) {
	s := newImportState(1, "book.txt", 100, 40, nil)
	base := len(s.history)
	retry := func(msg string) imp.Event {
		return imp.Event{Time: time.Now(), Stage: imp.StageSegmenting, Message: msg, Level: "warn", Key: "retry:segmenting"}
	}
	s.appendEvent(retry("1s 后重试（第 1 次）"), 80)
	s.appendEvent(retry("2s 后重试（第 2 次）"), 80)
	s.appendEvent(retry("4s 后重试（第 3 次）"), 80)
	if got := len(s.history) - base; got != 1 {
		t.Fatalf("同 Key 连续重试应合并为 1 行，得 %d", got)
	}
	if last := s.history[len(s.history)-1]; last.message != "4s 后重试（第 3 次）" {
		t.Fatalf("合并行应更新为最新消息，得 %q", last.message)
	}
	// After an ordinary progress row separates them, a new retry starts its own row.
	s.appendEvent(imp.Event{Time: time.Now(), Stage: imp.StageAnalyzing, Message: "分析第 1 章起的连续批次..."}, 80)
	s.appendEvent(retry("1s 后重试（第 1 次）"), 80)
	if got := len(s.history) - base; got != 3 {
		t.Fatalf("隔断后重试应另起一行，共 3 行，得 %d", got)
	}
}

// TestRenderImportLineWrapsWithoutClipping guards full visibility of error detail: the body wraps to
// the width remaining after the prefix with continuation lines aligned, and no row may exceed contentW —
// the viewport hard-clips over-wide rows, and the HTTP status/provider/model in an error are exactly the
// investigation evidence, so clipping them makes the error useless.
func TestRenderImportLineWrapsWithoutClipping(t *testing.T) {
	ln := importLine{
		at:      time.Now(),
		stage:   imp.StageSegmenting,
		message: "切分区间 L1..L171",
		err: errors.New("imp: 模型调用失败（请求参数非法，HTTP 400，openrouter，deepseek/deepseek-chat）：" +
			"Provider returned error: invalid request payload with a very long gateway message tail"),
	}
	const contentW = 80
	out := renderImportLine(ln, contentW, time.Now())
	// A wrap can break at any character; comparing with whitespace stripped verifies only that not one character is lost.
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' {
				return -1
			}
			return r
		}, s)
	}
	for _, want := range []string{"HTTP 400", "openrouter", "gateway message tail"} {
		if !strings.Contains(norm(out), norm(want)) {
			t.Fatalf("行内容缺少 %q：%q", want, out)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > contentW {
			t.Fatalf("第 %d 行宽 %d 超出 %d，会被 viewport 裁掉：%q", i, w, contentW, line)
		}
	}
	// Narrow terminal: the prefix (timestamp + icon + long stage name) can eat most of the row width, so the body must start its own line rather than being crammed over-wide against a floor.
	ln.stage = imp.StageAwaitingConfirmation
	const narrowW = 40
	for i, line := range strings.Split(renderImportLine(ln, narrowW, time.Now()), "\n") {
		if w := lipgloss.Width(line); w > narrowW {
			t.Fatalf("窄终端第 %d 行宽 %d 超出 %d：%q", i, w, narrowW, line)
		}
	}
}

// TestRenderImportLineMultilineBlock guards the layout of multi-line block messages (the segmentation
// confirmation preview): continuation lines carry a shallow 2-column indent rather than aligning to the
// prefix width — a 40+ column prefix would squeeze the whole chapter list into the panel's right half,
// leaving the left half empty.
func TestRenderImportLineMultilineBlock(t *testing.T) {
	ln := importLine{
		at:      time.Now(),
		stage:   imp.StageAwaitingConfirmation,
		current: 157, total: 157,
		message: "已切分 157 章，请核对：\n  第1章 引子\n  第2章 我故意的\n",
	}
	const contentW = 100
	out := strings.Split(renderImportLine(ln, contentW, time.Now()), "\n")
	if len(out) != 3 {
		t.Fatalf("应为前缀行 + 2 个正文行，得 %d 行：%q", len(out), out)
	}
	for i, line := range out[1:] {
		if w := lipgloss.Width(line); w > contentW {
			t.Fatalf("第 %d 行超宽 %d：%q", i+1, w, line)
		}
		if strings.HasPrefix(line, strings.Repeat(" ", 20)) {
			t.Fatalf("多行块续行不应按前缀宽对齐：%q", line)
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("多行块续行应浅缩进 2 列：%q", line)
		}
	}
}

// TestWrapTextResetsAtNewlines guards multi-line message wrapping: the line-width counter must reset at
// '\n', or once any line wraps every following line is misjudged over-wide and gets a spurious wrap plus
// indent, tearing the whole confirmation preview apart.
func TestWrapTextResetsAtNewlines(t *testing.T) {
	in := strings.Repeat("宽", 30) + "\n短行一\n短行二"
	out := wrapText(in, 20)
	for i, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > 20 {
			t.Fatalf("第 %d 行宽 %d 超出 20：%q", i, w, l)
		}
	}
	if !strings.Contains(out, "\n短行一\n短行二") {
		t.Fatalf("原有短行不得被打散：%q", out)
	}
}

// TestImportEscResumeGate guards where Esc lands on the import panel: after an import started from the
// welcome page wraps up successfully, closing the panel must run one catch-up recovery (bootstrap's
// Resume only runs at startup), or the user is left on a welcome page with no continuation entry point;
// in an error terminal state or the workbench scenario only the panel closes; a running Esc still cancels
// rather than closing.
func TestImportEscResumeGate(t *testing.T) {
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	// tea.Batch returns a BatchMsg after execution (subcommands are not run), which distinguishes "focus + recovery" from focus alone.
	isBatch := func(cmd tea.Cmd) bool {
		_, ok := cmd().(tea.BatchMsg)
		return ok
	}
	newM := func(mode appMode, st *importState) Model {
		return Model{mode: mode, importer: st, textarea: textarea.New()}
	}

	m := newM(modeNew, &importState{done: true, stage: imp.StageDone})
	next, cmd := m.handleImportKey(esc)
	if next.(Model).importer != nil {
		t.Fatal("终态 Esc 应关闭面板")
	}
	if !isBatch(cmd) {
		t.Fatal("欢迎页导入成功关面板应附带恢复命令")
	}

	m = newM(modeNew, &importState{done: true, stage: imp.StageError, err: errors.New("boom")})
	if _, cmd := m.handleImportKey(esc); isBatch(cmd) {
		t.Fatal("出错终态不应触发恢复（书可能根本没导入成功）")
	}

	m = newM(modeRunning, &importState{done: true, stage: imp.StageDone})
	if _, cmd := m.handleImportKey(esc); isBatch(cmd) {
		t.Fatal("工作台自有门禁，不应重复触发恢复")
	}

	canceled := false
	m = newM(modeNew, &importState{cancel: func() { canceled = true }})
	next, _ = m.handleImportKey(esc)
	if !canceled || next.(Model).importer == nil {
		t.Fatal("运行中 Esc 应取消导入且保留面板等 runner 收尾")
	}
}

// TestRetryCountdown guards the countdown rendering contract (shared by the event panel and the import
// panel): empty when no deadline is set or it has passed (the request is in flight); the remaining time
// rounds up to seconds, decrements per second and never shows 0s.
func TestRetryCountdown(t *testing.T) {
	now := time.Now()
	if got := retryCountdown(time.Time{}, now); got != "" {
		t.Fatalf("零值截止应返回空，得 %q", got)
	}
	if got := retryCountdown(now.Add(-time.Second), now); got != "" {
		t.Fatalf("已到点应返回空，得 %q", got)
	}
	if got := retryCountdown(now.Add(7500*time.Millisecond), now); got != "Thử lại sau 8s" {
		t.Fatalf("7.5s 应上取整为 8s，得 %q", got)
	}
	if got := retryCountdown(now.Add(300*time.Millisecond), now); got != "Thử lại sau 1s" {
		t.Fatalf("不足 1s 应显示 1s，得 %q", got)
	}
}

// TestParseImportArgsGuide guards --guide parsing: natural-language guidance may contain spaces (every
// following token joins it), may combine with other options (placed last), and empty content errors.
func TestParseImportArgsGuide(t *testing.T) {
	opts, err := parseImportArgs([]string{"--guide=幕间·X", "也是", "独立章节"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Guidance != "幕间·X 也是 独立章节" {
		t.Fatalf("含空格指导应整体并入，得 %q", opts.Guidance)
	}
	opts, err = parseImportArgs([]string{"book.txt", "--yes", "--guide=序章并入第一章"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.AutoConfirm || opts.SourcePath != "book.txt" || opts.Guidance != "序章并入第一章" {
		t.Fatalf("与其它选项组合解析不符：%+v", opts)
	}
	if _, err := parseImportArgs([]string{"--guide="}); err == nil {
		t.Fatal("空 --guide 应报错")
	}
}
