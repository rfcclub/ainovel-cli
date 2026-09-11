package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/utils"
)

const maxEvents = 500

// maxStreamRounds caps how many rounds the stream panel keeps. Each LLM call's end triggers a streamClear that opens
// a new round; a single-chapter writer runs about 3–5 rounds (agent header / thinking / draft / commit), so 32 rounds is
// roughly the streaming output of the last 6–10 chapters. A committed chapter's prose lives in store/drafts, and anything
// beyond the cap is dropped so that no token delta triggers an O(full text) re-render. The steady-state memory ceiling is
// about 512KB, well below the stutter threshold.
const maxStreamRounds = 32

type focusPane int

const (
	focusEvents focusPane = iota
	focusStream
	focusDetail
	focusState // Left status sidebar (scrollable)

	focusPaneCount // Total focus count, used for Tab cycling
)

type appMode int

const (
	modeNew     appMode = iota // Waiting for the user to enter the novel requirement
	modeRunning                // Writing (including stopping on error; input can resume)
	modeDone                   // Writing finished
)

// The spinner frame sequence shared by the top bar and streaming activity (bubbles.Spinner.MiniDot).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// A spinner frame sequence reserved for the event stream's "in progress" rows (bubbles.Spinner.Dot).
// Seven dots plus one gap rotate clockwise around a 3×3 grid, reading visually as a complete loading circle. A separate
// frame index and a faster tick leave the top bar and star animation undisturbed.
var toolSpinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// Model is the TUI's top-level state.
type Model struct {
	runtime        *host.Host
	cocreate       *cocreateState
	help           *helpState
	modelSwitch    *modelSwitchState
	modelConfig    *modelConfigState
	report         *reportState
	version        string
	importer       *importState
	importSeq      int
	simulator      *simulationState
	simSeq         int
	compItems      []commandPaletteItem
	compIdx        int
	compActive     bool
	commandToken   string // Currently registered command token; only that span is styled, not the arguments
	snapshot       host.UISnapshot
	events         []host.Event
	eventIndex     map[string]int   // event.ID -> index into m.events; updated in place when a call event arrives
	viewport       viewport.Model   // Event-stream viewport
	streamVP       viewport.Model   // Streaming-output viewport
	detailVP       viewport.Model   // Right-hand detail viewport
	stateVP        viewport.Model   // Left status sidebar viewport (scrollable)
	streamBuf      *strings.Builder // Accumulation buffer for streaming text
	streamRounds   []string
	textarea       textarea.Model
	width          int
	height         int
	autoScroll     bool
	streamScroll   bool      // Auto-follow the streaming panel
	streamDirty    bool      // streamRounds holds a delta not yet flushed
	flushPending   bool      // A streaming flush is scheduled, so each delta does not restart the timer
	lastKeyAt      time.Time // Time of the last non-Enter keypress; Enter is throttled so a pasted newline burst does not submit
	inputHistory   []string  // Submitted input history (deduped: adjacent repeats dropped)
	historyIdx     int       // Current browse index; == len(inputHistory) means "not browsing, editing the draft"
	historyDraft   string    // Draft saved before entering history browsing, restored when returning to the end
	focusPane      focusPane
	hoverPane      focusPane
	hoverActive    bool
	mode           appMode
	starting       bool // The UI reached the workbench and the Host runs startup initialisation
	startupMode    startupMode
	importHint     string // Hint shown when an unfinished import is detected at startup (shown on the welcome screen; cleared once an import starts)
	cocreateSeq    int
	reportSeq      int
	err            error
	spinnerIdx     int
	toolSpinnerIdx int  // Independent frame index for in-progress event-stream rows (150ms tick, does not affect the header/stars)
	toolTicking    bool // The tool animation timer is running; stops automatically when no event is active
	cursorIdx      int  // Streaming cursor frame index (advances with the main animation)
	streamRound    int  // Streaming output round counter
	quitPending    bool // Double Ctrl+C quit confirmation
	abortPending   bool // Manual pause waiting for Done to come back
	mouseOff       bool // When true, mouse reporting is disabled so the user can drag-select and copy natively; toggling again restores it
}

// NewModel creates the TUI Model.
func NewModel(rt *host.Host, version string) Model {
	ta := textarea.New()
	ta.Placeholder = placeholderForNewMode(startupModeQuick)
	ta.CharLimit = 5000
	ta.SetHeight(1)
	// MaxHeight=6 lets over-long input wrap by width into several lines (a visual cap of 6 lines).
	ta.MaxHeight = 6
	ta.ShowLineNumbers = false
	ta.Focus()

	// Enter inserts no newline by default (handleEnterKey submits); an explicit newline is rebound to ctrl+j (unix \n) and
	// alt+enter (the GUI habit). The terminal protocol layer cannot tell Shift+Enter from Enter, so Shift+Enter is not
	// supported.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")

	vp := viewport.New(80, 20)
	vp.SetContent("")

	svp := viewport.New(80, 10)
	svp.SetContent("")

	dvp := viewport.New(40, 20)
	dvp.SetContent("")

	stvp := viewport.New(32, 20)
	stvp.SetContent("")

	// An unfinished import is checked once at startup (LoadState recomputes artifact digests and does not go into snapshot
	// polling); an unfinished book would otherwise only be discovered when the creation gate refuses the user (RFC
	// §18.2).
	importHint := ""
	if rt != nil {
		importHint = rt.ImportResumeHint()
	}

	return Model{
		runtime:      rt,
		version:      strings.TrimSpace(version),
		autoScroll:   true,
		streamScroll: true,
		mode:         modeNew,
		startupMode:  startupModeQuick,
		importHint:   importHint,
		textarea:     ta,
		viewport:     vp,
		streamVP:     svp,
		detailVP:     dvp,
		stateVP:      stvp,
		streamBuf:    &strings.Builder{},
		eventIndex:   make(map[string]int),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenEvents(m.runtime),
		listenDone(m.runtime),
		listenStream(m.runtime),
		tickSnapshot(m.runtime),
		bootstrapRuntime(m.runtime),
		tickSpinner(),
	)
}

func (m *Model) paneAtMouse(x, y int) (focusPane, bool) {
	if m.width == 0 || m.height == 0 {
		return focusEvents, false
	}

	topH, _, bodyH := m.layoutHeights()
	if bodyH < 1 {
		return focusEvents, false
	}

	bodyStartY := topH
	bodyEndY := topH + bodyH
	if y < bodyStartY || y >= bodyEndY {
		return focusEvents, false
	}

	leftW := m.sidebarWidth()
	rightW := m.detailWidth()
	centerStartX := leftW
	rightStartX := m.width - rightW

	if x >= rightStartX {
		return focusDetail, true
	}
	if x < centerStartX {
		return focusState, true
	}

	eventH, _ := m.splitHeights(bodyH)
	if y-bodyStartY < eventH {
		return focusEvents, true
	}
	return focusStream, true
}

func (m *Model) paneHighlighted(pane focusPane) bool {
	if m.focusPane == pane {
		return true
	}
	return m.hoverActive && m.hoverPane == pane
}

// hasRunningEvent reports whether an unfinished call event exists (its spinner still turning).
// toolSpinnerTick uses it to decide whether a re-render is worth it: with no running event the spinner frame does not
// affect the output and the whole refreshEventViewport would be provably wasted work.
func (m *Model) hasRunningEvent() bool {
	for i := range m.events {
		if m.events[i].Running() {
			return true
		}
	}
	return false
}

// flushStreamIfDirty renders the accumulated streamRounds into the viewport and marks them flushed.
// It reports whether a flush actually happened, letting the caller decide about GotoBottom.
func (m *Model) flushStreamIfDirty() bool {
	if !m.streamDirty {
		return false
	}
	m.refreshStreamViewport()
	m.streamDirty = false
	return true
}

// refreshEventViewport re-renders the event stream content and sets the viewport.
func (m *Model) refreshEventViewport() {
	centerW := m.eventFlowWidth()
	content := renderEventContent(m.events, centerW, m.toolSpinnerIdx)
	snap := m.snapshot
	if m.starting {
		snap.IsRunning = true
	}
	if activity := renderEventActivity(snap, m.spinnerIdx, centerW); activity != "" {
		if strings.TrimSpace(content) != "" {
			content += "\n" + activity
		} else {
			content = activity
		}
	}
	m.viewport.SetContent(content)
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

func (m *Model) refreshStreamViewport() {
	cursor := ""
	if m.snapshot.IsRunning {
		cursor = renderStreamCursor(m.cursorIdx)
	}
	m.streamVP.SetContent(renderStreamContent(m.streamRounds, m.streamVP.Width, cursor))
}

func (m *Model) refreshDetailViewport() {
	rightW := m.detailWidth()
	if rightW <= 4 {
		return
	}
	m.detailVP.SetContent(renderDetailContent(m.snapshot, rightW-4))
}

// refreshStateViewport flushes the left state sidebar's content into its viewport.
// The sidebar content derives purely from the snapshot, so it must be refreshed on any snapshot or size change.
func (m *Model) refreshStateViewport() {
	leftW := m.sidebarWidth()
	if leftW <= 4 {
		return
	}
	m.stateVP.SetContent(renderStateContent(m.snapshot, leftW-4))
}

// updateViewportSize updates the viewport sizes from the current window dimensions.
func (m *Model) updateViewportSize() {
	centerW := m.eventFlowWidth()
	rightW := m.detailWidth()
	bodyH := m.bodyHeight()
	eventH, streamH := m.splitHeights(bodyH)
	m.viewport.Width = centerW - 2
	m.viewport.Height = eventH - 1 // -1 accounts for the event panel header row
	m.streamVP.Width = centerW - 2
	m.streamVP.Height = streamH - 1 // -1 accounts for the stream panel header row
	m.detailVP.Width = rightW - 2
	m.detailVP.Height = bodyH
	leftW := m.sidebarWidth()
	m.stateVP.Width = max(1, leftW-2)
	m.stateVP.Height = max(1, bodyH-1) // -1 leaves room at the top so the content starts on the bottom row
	// After the height or content shrinks, the freely scrolling left and right panes may sit at an out-of-range offset
	// (bubbles' SetContent only guards against passing the last line) and the viewport pads the bottom with blank lines.
	// SetYOffset clamps itself.
	m.stateVP.SetYOffset(m.stateVP.YOffset)
	m.detailVP.SetYOffset(m.detailVP.YOffset)
}

// splitHeights computes the height allocation between the event stream and the streaming output.
func (m *Model) splitHeights(bodyH int) (eventH, streamH int) {
	eventH = bodyH * 40 / 100
	if eventH < 3 {
		eventH = 3
	}
	streamH = bodyH - eventH - 1 // -1 accounts for the separator line
	if streamH < 3 {
		streamH = 3
	}
	return
}

func (m *Model) inputWidth() int {
	if m.width == 0 {
		return 60
	}
	return m.width - 6 // border + padding + the "❯ " prompt
}

func (m *Model) currentInputWidth() int {
	if m.cocreate != nil {
		return coCreateInputWidth(m.width, m.height)
	}
	return m.inputWidth()
}

// refitTextareaHeight estimates the visual line count from the current content and calls SetHeight dynamically.
// Visual lines = the sum of each logical line (split on \n) wrapped to the width. Together with MaxHeight=6 this gives
// "over-long content or an explicit newline expands to multiple lines, up to 6".
func (m *Model) refitTextareaHeight() {
	w := m.textarea.Width()
	if w <= 0 {
		return
	}
	// In cocreation mode the input stays a fixed single line: multi-line textarea content is scrolled by the textarea
	// itself around the cursor. Otherwise the inputBox height would follow the content, shrinking the left conversation
	// pane and drifting the input vertically, wrecking layout stability.
	if m.cocreate != nil {
		m.textarea.SetHeight(1)
		return
	}
	text := m.textarea.Value()
	if text == "" {
		m.textarea.SetHeight(1)
		return
	}
	// Two columns are deducted for overhead (the textarea's internal prompt symbol + cursor); erring one line long is acceptable.
	contentW := w - 2
	if contentW < 1 {
		contentW = 1
	}
	total := 0
	for line := range strings.SplitSeq(text, "\n") {
		lw := lipgloss.Width(line)
		if lw == 0 {
			total++
			continue
		}
		total += (lw + contentW - 1) / contentW
	}
	if total < 1 {
		total = 1
	}
	m.textarea.SetHeight(total) // SetHeight clamps against MaxHeight internally
}

// resizeTextarea sets the width and the content-based height together.
// It replaces scattered SetWidth(currentInputWidth()) calls and keeps the height following any width change.
func (m *Model) resizeTextarea() {
	m.textarea.SetWidth(m.currentInputWidth())
	m.refitTextareaHeight()
}

// maxInputHistory caps the history length, preventing memory growth in a long session.
const maxInputHistory = 200

// pushInputHistory appends successfully submitted content to the history, deduplicating adjacent entries, and resets the browse index in step.
func (m *Model) pushInputHistory(text string) {
	if text == "" {
		return
	}
	if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
		m.inputHistory = append(m.inputHistory, text)
		if len(m.inputHistory) > maxInputHistory {
			m.inputHistory = m.inputHistory[len(m.inputHistory)-maxInputHistory:]
		}
	}
	m.historyIdx = len(m.inputHistory)
	m.historyDraft = ""
}

// tryHistoryUp moves one entry further back in history and reports whether the key was handled.
// On first entering history browsing the current textarea content is stashed as a draft, restored when the end is reached
// again. The caller must decide for itself whether to bypass this in a multi-line case (letting the textarea handle
// in-line cursor movement).
func (m *Model) tryHistoryUp() bool {
	if len(m.inputHistory) == 0 || m.historyIdx <= 0 {
		return false
	}
	if m.historyIdx == len(m.inputHistory) {
		m.historyDraft = m.textarea.Value()
	}
	m.historyIdx--
	m.textarea.SetValue(m.inputHistory[m.historyIdx])
	m.textarea.CursorEnd()
	m.syncCommandInputHighlight()
	m.refitTextareaHeight()
	return true
}

// tryHistoryDown moves one entry further forward in history, restoring the draft at the end.
func (m *Model) tryHistoryDown() bool {
	if m.historyIdx >= len(m.inputHistory) {
		return false
	}
	m.historyIdx++
	if m.historyIdx == len(m.inputHistory) {
		m.textarea.SetValue(m.historyDraft)
		m.historyDraft = ""
	} else {
		m.textarea.SetValue(m.inputHistory[m.historyIdx])
	}
	m.textarea.CursorEnd()
	m.syncCommandInputHighlight()
	m.refitTextareaHeight()
	return true
}

// textareaIsMultiline reports whether the current textarea content contains an explicit newline; it decides whether ↑↓ walks history or moves within the line.
func (m *Model) textareaIsMultiline() bool {
	return strings.Contains(m.textarea.Value(), "\n")
}

// inputHints generates the bottom hint text from the current state.
// copySuffix is appended uniformly at the end, so the user sees how to copy a selection in any non-urgent state; with the
// mouse disabled it shows a prominent red hint reminding them that another key press restores mouse interaction.
func (m *Model) inputHints() string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	if m.quitPending {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("Nhấn Ctrl+C một lần nữa để thoát")
	}
	limitHint := m.inputLimitHint()
	suffix := limitHint + " · Ctrl+R Chế độ copy"
	if m.mode == modeNew {
		suffix = limitHint
	}
	if m.mouseOff && m.mode != modeNew {
		return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render("✂ Chế độ bôi đen copy: Kéo chuột chọn văn bản để sao chép · Ctrl+R Quay lại")
	}
	if m.cocreate != nil {
		scrollHint := " · Tab Cuộn: Hội thoại"
		if m.cocreate.focusPrompt {
			scrollHint = " · Tab Cuộn: Chỉ thị"
		}
		switch {
		case m.cocreate.awaiting:
			return dimStyle.Render("Chờ AI phản hồi · Esc Thoát đồng sáng tác" + scrollHint + suffix)
		case m.cocreate.canStart():
			startLabel := "Ctrl+S Bắt đầu sáng tác"
			if m.cocreate.stage {
				startLabel = "Ctrl+S Áp dụng & Tiếp tục"
			}
			return dimStyle.Render("Enter Gửi · " + startLabel + " · Esc Thoát" + scrollHint + suffix)
		default:
			return dimStyle.Render("Enter Gửi · Esc Thoát đồng sáng tác" + scrollHint + suffix)
		}
	}
	if m.mode == modeNew {
		if m.startupMode == startupModeQuick {
			return dimStyle.Render("Tab Đổi chế độ · Gõ / để xem lệnh · Enter Bắt đầu viết ngay · Esc Xóa" + suffix)
		}
		return dimStyle.Render("Tab Đổi chế độ · Gõ / để xem lệnh · Enter Bắt đầu đồng sáng tác · Esc Xóa" + suffix)
	}
	switch m.snapshot.RuntimeState {
	case "pausing":
		return dimStyle.Render("Đang tạm dừng sáng tác · Vui lòng chờ vòng hiện tại kết thúc" + suffix)
	case "paused":
		return dimStyle.Render("Gõ / để xem lệnh · Enter Tiếp tục sáng tác · Esc Xóa" + suffix)
	}
	return dimStyle.Render("Gõ / để xem lệnh · Click/Tab Đổi panel · ↑↓ Cuộn · End Về cuối · Ctrl+L Xóa màn hình · Esc Tạm dừng · Enter Gửi" + suffix)
}

func (m *Model) inputLimitHint() string {
	limit := m.textarea.CharLimit
	if limit <= 0 {
		return ""
	}
	used := m.textarea.Length()
	if used < limit*4/5 {
		return ""
	}
	return fmt.Sprintf(" · Đã nhập %d/%d", used, limit)
}

func (m *Model) eventFlowWidth() int {
	if m.width == 0 {
		return 80
	}
	leftW := m.sidebarWidth()
	rightW := m.detailWidth()
	return m.width - leftW - rightW
}

func (m *Model) sidebarWidth() int {
	if m.width == 0 {
		return 32
	}
	return m.width * 23 / 100
}

func (m *Model) detailWidth() int {
	if m.width == 0 {
		return 40
	}
	return m.width * 27 / 100
}

func (m *Model) bodyHeight() int {
	_, _, bodyH := m.layoutHeights()
	return bodyH
}

func (m *Model) currentSpinnerFrame() string {
	if !m.snapshot.IsRunning && !m.starting {
		return ""
	}
	return spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
}

func (m *Model) outputDir() string {
	if m.runtime == nil {
		return ""
	}
	return m.runtime.Dir()
}

func defaultSteerPlaceholder() string {
	return "Nhập can thiệp cốt truyện, ví dụ: đẩy tuyến tình cảm lên chương 4"
}

func (m *Model) syncRuntimePlaceholder() {
	if m.mode != modeRunning || m.cocreate != nil {
		return
	}
	if m.starting {
		m.textarea.Placeholder = "Đang khởi tạo sáng tác..."
		return
	}
	switch m.snapshot.RuntimeState {
	case "completed":
		m.textarea.Placeholder = donePlaceholder
	case "pausing":
		m.textarea.Placeholder = "Đang tạm dừng sáng tác..."
	case "paused":
		if m.snapshot.AdvanceMode == "review" && m.snapshot.Phase == "writing" {
			m.textarea.Placeholder = "Chờ nghiệm thu: Nhập ý kiến chỉnh sửa, hoặc /next để duyệt chương tiếp"
		} else {
			m.textarea.Placeholder = "Sáng tác đã tạm dừng, nhập nội dung bất kỳ để tiếp tục"
		}
	default:
		if !m.snapshot.IsRunning {
			if m.snapshot.AdvanceMode == "review" && m.snapshot.Phase == "writing" {
				m.textarea.Placeholder = "Chờ nghiệm thu: Nhập ý kiến chỉnh sửa, hoặc /next để duyệt chương tiếp"
			} else {
				m.textarea.Placeholder = "Sáng tác bị gián đoạn, nhập nội dung bất kỳ để tiếp tục"
			}
		} else {
			m.textarea.Placeholder = defaultSteerPlaceholder()
		}
	}
}

func (m *Model) renderBottomBar() string {
	inputView := highlightCommandToken(m.textarea.View(), m.textarea.Value(), m.commandToken)
	inputBox := renderInputBox(
		inputView,
		m.inputHints(),
		m.snapshot,
		m.outputDir(),
		m.width,
	)
	if m.mode != modeNew || m.cocreate != nil {
		return inputBox
	}
	return renderStartupModeBar(m.width, m.startupMode) + "\n" + inputBox
}

func (m *Model) layoutHeights() (topH, inputH, bodyH int) {
	if m.width == 0 || m.height == 0 {
		return 1, 4, 20
	}
	topH = lipgloss.Height(renderTopBar(m.snapshot, m.width, m.currentSpinnerFrame(), m.version))
	inputH = lipgloss.Height(m.renderBottomBar())
	bodyH = m.height - topH - inputH
	if bodyH < 3 {
		bodyH = 3
	}
	return
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Đang tải..."
	}
	if m.width < 100 {
		return lipgloss.NewStyle().
			Width(m.width).Height(m.height).
			AlignHorizontal(lipgloss.Center).
			AlignVertical(lipgloss.Center).
			Render("Độ rộng terminal quá nhỏ, vui lòng mở rộng tối thiểu 100 cột")
	}
	if m.cocreate != nil {
		return renderCoCreateModal(m.width, m.height, m.cocreate, errorText(m.err), m.textarea.View(), m.spinnerIdx, m.quitPending)
	}
	if m.help != nil {
		return renderHelpModal(m.width, m.height, m.help)
	}
	if m.report != nil {
		return renderReportModal(m.width, m.height, m.report)
	}
	if m.importer != nil {
		// An import does not depend on the Engine's run state, so the animation frame comes straight from spinnerIdx (currentSpinnerFrame returns empty when the engine is stopped).
		return renderImportModal(m.width, m.height, m.importer, m.spinnerIdx)
	}
	if m.simulator != nil {
		return renderSimulationModal(m.width, m.height, m.simulator)
	}

	topBar := renderTopBar(m.snapshot, m.width, m.currentSpinnerFrame(), m.version)
	inputBox := m.renderBottomBar()
	_, inputH, bodyH := m.layoutHeights()

	var body string
	if m.mode == modeNew {
		errMsg := ""
		if m.err != nil {
			errMsg = m.err.Error()
		}
		body = renderWelcome(m.width, bodyH, errMsg, m.startupMode, m.importHint)
	} else {
		leftW := m.sidebarWidth()
		rightW := m.detailWidth()
		centerW := m.width - leftW - rightW
		eventH, streamH := m.splitHeights(bodyH)

		if m.viewport.Width != centerW-2 || m.viewport.Height != eventH-1 {
			m.viewport.Width = centerW - 2
			m.viewport.Height = eventH - 1 // -1 accounts for the event panel header row
		}
		if m.streamVP.Width != centerW-2 || m.streamVP.Height != streamH-1 {
			m.streamVP.Width = centerW - 2
			m.streamVP.Height = streamH - 1 // -1 accounts for the stream panel header row
		}

		eventFlow := renderEventFlowViewport(m.viewport, centerW, eventH, m.paneHighlighted(focusEvents))
		streamPanel := renderStreamPanel(m.streamVP, centerW, streamH, m.paneHighlighted(focusStream), m.snapshot.IsRunning || m.starting, m.spinnerIdx)
		center := lipgloss.JoinVertical(lipgloss.Left, eventFlow, streamPanel)

		left := renderStatePanel(m.stateVP, leftW, bodyH, m.paneHighlighted(focusState))
		right := renderDetailPanel(m.detailVP, rightW, bodyH, m.paneHighlighted(focusDetail))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, center, right)
	}

	view := lipgloss.JoinVertical(lipgloss.Left, topBar, body, inputBox)

	// Modal overlay stacking: floats above the body's bottom without affecting layout
	if m.modelSwitch != nil {
		commandBar := renderModelSwitchBar(m.width, m.modelSwitch)
		view = overlayAboveInput(view, commandBar, inputH)
	} else if m.modelConfig != nil {
		view = overlayAboveInput(view, renderModelConfigModal(m.width, m.modelConfig), inputH)
	} else if m.compActive {
		commandBar := renderCommandPalette(m.width, m.compItems, m.compIdx)
		view = overlayAboveInput(view, commandBar, inputH)
	}
	return view
}

// sendCoCreate starts one cocreation request, handling reqID, the textarea and the placeholder uniformly.
func (m *Model) sendCoCreate() tea.Cmd {
	m.cocreateSeq++
	m.cocreate.reqID = m.cocreateSeq
	m.cocreate.awaiting = true
	m.resizeTextarea()
	m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
	m.textarea.Blur()
	return runCoCreate(m.runtime, m.cocreate)
}

func (m Model) handleCoCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cocreate == nil {
		return m, nil
	}
	state := m.cocreate

	// Keyboard ↑↓/PgUp/PgDn/Home/End scroll; Tab toggles scroll focus between the left conversation pane and the right
	// creation-brief pane (the left pane by default, where the user reads back the body). The welcome page has mouse
	// reporting disabled to preserve native copy, so an overflowing right pane relies on Tab to move focus and then
	// keyboard scrolling. Left pane: scrolling up turns follow off, scrolling to the bottom turns it back on (streaming
	// follow).
	switch msg.Type {
	case tea.KeyTab:
		state.focusPrompt = !state.focusPrompt
		return m, nil
	case tea.KeyUp, tea.KeyPgUp:
		if state.focusPrompt {
			var cmd tea.Cmd
			state.promptVP, cmd = state.promptVP.Update(msg)
			return m, cmd
		}
		state.convFollow = false
		var cmd tea.Cmd
		state.convVP, cmd = state.convVP.Update(msg)
		return m, cmd
	case tea.KeyDown, tea.KeyPgDown:
		if state.focusPrompt {
			var cmd tea.Cmd
			state.promptVP, cmd = state.promptVP.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		state.convVP, cmd = state.convVP.Update(msg)
		if state.convVP.AtBottom() {
			state.convFollow = true
		}
		return m, cmd
	case tea.KeyHome:
		if state.focusPrompt {
			state.promptVP.GotoTop()
			return m, nil
		}
		state.convFollow = false
		state.convVP.GotoTop()
		return m, nil
	case tea.KeyEnd:
		if state.focusPrompt {
			state.promptVP.GotoBottom()
			return m, nil
		}
		state.convFollow = true
		state.convVP.GotoBottom()
		return m, nil
	case tea.KeyEsc:
		return m.exitCoCreate()
	}

	// While awaiting an AI reply, editing keys (character input / backspace / cursor / Ctrl+U / newline) pass through — the
	// user can type the next sentence ahead while the AI thinks. The submission block is pushed down inside each case so
	// Enter throttling runs before the awaiting block, letting a pasted \n fragment still become a space.

	switch msg.Type {
	case tea.KeyCtrlS:
		if state.awaiting {
			return m, nil
		}
		if !state.canStart() {
			return m, nil
		}
		// Stage cocreation: inject the "next direction brief" and resume creation, returning to the workbench.
		if state.stage {
			draft := state.draftPrompt()
			m.cocreate = nil
			m.err = nil
			m.resizeTextarea()
			m.textarea.Placeholder = defaultSteerPlaceholder()
			return m, tea.Batch(resumeFromCoCreate(m.runtime, draft), m.textarea.Focus())
		}
		// Cold-start cocreation: begin creation with the organised creation brief.
		prompt, err := state.buildPrompt()
		if err != nil {
			m.err = err
			return m, nil
		}
		cmd := m.enterStarting(prompt)
		return m, tea.Batch(startRuntime(m.runtime, prompt), cmd)
	case tea.KeyEnter:
		// Alt+Enter → explicit newline, handed to textarea.Update (KeyMap.InsertNewline is already bound to this key)
		if msg.Alt {
			break
		}
		// Too short an interval since the last character key → treated as a \n fragment from a paste: a space is inserted instead of submitting.
		// This must be decided before the awaiting block — otherwise a pasted \n fragment would be blocked while awaiting,
		// swallowing "abc\ndef" into "abcdef" and diverging from the base path's semantics.
		if !m.lastKeyAt.IsZero() && time.Since(m.lastKeyAt) < 50*time.Millisecond {
			var cmd tea.Cmd
			state.resetSuggestionInput()
			m.textarea, cmd = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
			m.refitTextareaHeight()
			return m, cmd
		}
		// A genuine submit intent: blocked while awaiting (requests must not be sent concurrently)
		if state.awaiting {
			return m, nil
		}
		text := utils.CleanInputLine(m.textarea.Value())
		if text == "" {
			return m, nil
		}
		m.err = nil
		state.appendUser(text)
		m.textarea.Reset()
		m.refitTextareaHeight()
		cmd := m.sendCoCreate()
		return m, cmd
	case tea.KeyCtrlU:
		state.resetSuggestionInput()
		m.textarea.Reset()
		m.refitTextareaHeight()
		return m, nil
	}

	// Number keys 1/2/3 compose suggestions successively: the first fills the box, later ones append after a semicolon and
	// a repeated choice is ignored. Any manual edit exits the quick-compose state, after which digits keep their ordinary
	// input semantics.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !state.awaiting {
		if r := msg.Runes[0]; r >= '1' && r <= '3' {
			if value, handled := state.appendSuggestion(int(r-'1'), m.textarea.Value()); handled {
				m.textarea.SetValue(value)
				m.textarea.CursorEnd()
				m.refitTextareaHeight()
				return m, nil
			}
		}
	}

	// Ordinary input is forwarded to the textarea
	if msg.Type == tea.KeyRunes && (containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes)) {
		return m, nil
	}
	var ok bool
	if msg, ok = cleanHumanKeyRunes(msg); !ok {
		return m, nil
	}
	state.resetSuggestionInput()
	if msg.Type == tea.KeyRunes {
		m.lastKeyAt = time.Now()
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.refitTextareaHeight()
	return m, cmd
}

// exitCoCreate leaves cocreation mode, cancels the in-flight LLM request and restores the input box state.
func (m Model) exitCoCreate() (tea.Model, tea.Cmd) {
	if m.cocreate.cancel != nil {
		m.cocreate.cancel()
	}
	stage := m.cocreate.stage
	initial := m.cocreate.initialInput()
	m.cocreate = nil
	m.resizeTextarea()
	// Stage-cocreation cancellation: clear the occupancy flag, stay paused and return to the workbench input state (no synthesised opening is filled back in).
	if stage {
		m.textarea.SetValue("")
		m.textarea.Placeholder = defaultSteerPlaceholder()
		return m, tea.Batch(cancelCoCreate(m.runtime), fetchSnapshot(m.runtime), m.textarea.Focus())
	}
	m.textarea.SetValue(initial)
	m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
	return m, m.textarea.Focus()
}

// overlayAboveInput floats the overlay over the bottom of the base view (above the inputBox) without changing the overall
// layout height. It covers only the overlay card's own width, leaving the underlying content visible on the right.
func overlayAboveInput(base, overlay string, inputLineCount int) string {
	baseLines := strings.Split(base, "\n")
	overLines := strings.Split(strings.TrimRight(overlay, "\n"), "\n")

	endY := len(baseLines) - inputLineCount
	startY := endY - len(overLines)
	if startY < 0 {
		startY = 0
	}

	for i, ol := range overLines {
		y := startY + i
		if y >= 0 && y < endY {
			olW := lipgloss.Width(ol)
			// Trim olW visible characters from the left of the baseline line, then splice the overlay with the remaining right-hand content
			right := ansi.TruncateLeft(baseLines[y], olW, "")
			baseLines[y] = ol + right
		}
	}
	return strings.Join(baseLines, "\n")
}

// isCSILeak detects whether KeyRunes is a leaked fragment of a CSI escape sequence.
// When a terminal sends an arrow key as \x1b[A, fast key presses can split the sequence: \x1b is parsed as Escape while
// "[" or "[A" leaks into the textarea as KeyRunes.
func isCSILeak(runes []rune) bool {
	if len(runes) == 0 || runes[0] != '[' {
		return false
	}
	for _, r := range runes[1:] {
		if (r >= '0' && r <= '9') || r == ';' ||
			(r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
			continue
		}
		return false
	}
	return true
}

// containsSGRFragment detects whether text contains a leaked SGR mouse fragment (the "<number;number;" pattern).
func containsSGRFragment(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		if j >= len(s) || s[j] < '0' || s[j] > '9' {
			continue
		}
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && s[j] == ';' {
			return true
		}
	}
	return false
}

func cleanHumanKeyRunes(msg tea.KeyMsg) (tea.KeyMsg, bool) {
	if msg.Type != tea.KeyRunes {
		return msg, true
	}
	cleaned := utils.CleanInputRunes(msg.Runes)
	if cleaned == "" {
		return msg, false
	}
	msg.Runes = []rune(cleaned)
	return msg, true
}
