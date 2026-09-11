package diag

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/store"
)

// Runtime detection thresholds.
const (
	repeatCritical = 8 // Recent repeats reaching this count are escalated to critical
	streamIdleWarn = 3 // Cumulative stream_idle warning threshold
)

// RuntimeRuleFunc is the uniform signature of a runtime diagnostic rule (the counterpart of the creation side's RuleFunc).
// Its input is the redacted, aggregated RuntimeCapture and it produces report-only Findings — all AutoNone, diagnosing
// without producing Actions (observer discipline, see architecture.md §2.3).
type RuntimeRuleFunc func(rc *RuntimeCapture) []Finding

var runtimeRules = []RuntimeRuleFunc{
	repeatedErrors,
	stuckStep,
	streamIdleStorm,
}

// runtimeFindings runs every runtime rule.
func runtimeFindings(rc *RuntimeCapture) []Finding {
	var out []Finding
	for _, rule := range runtimeRules {
		out = append(out, rule(rc)...)
	}
	return out
}

// Diagnose is the full diagnostics entry point for /diag: creation diagnostics + runtime signals + runtime detection,
// returning the merged Report and the raw RuntimeCapture (for the export to reuse, avoiding a second capture).
// Runtime Findings are merged into Findings for display only and never modify Actions — staying purely observational.
func Diagnose(s *store.Store) (Report, RuntimeCapture) {
	rep := Analyze(s)
	rc := CaptureRuntime(s)
	rep.Findings = append(rep.Findings, runtimeFindings(&rc)...)
	sortFindings(rep.Findings)
	return rep, rc
}

// repeatedErrors only flags "errors / invalid arguments that recur near the end" as a Finding.
// It does not touch ordinary tool repetition — subagent/novel_context/read_chapter and the like are naturally
// high-frequency over a long run and a cumulative count is not a loop signal; genuine "repeating without progressing" is
// caught by stuckStep.
func repeatedErrors(rc *RuntimeCapture) []Finding {
	var out []Finding
	for _, r := range rc.Repeats {
		var rule, title, sugg string
		switch {
		case strings.Contains(r.Sig, " · err: "):
			rule = "RepeatedToolError"
			title = "Công cụ liên tục báo cùng một lỗi"
			sugg = "Gần đây cùng một công cụ liên tục trả về cùng một lỗi, phần nhiều do tham số của model không hợp lệ hoặc không khớp contract của công cụ; hãy xem phần kiểm tra công cụ của agentcore / quy ước tham số trong prompt (xem #34)."
		case strings.Contains(r.Sig, "(args invalid)"):
			rule = "ArgsInvalidLoop"
			title = "Tham số liên tục không phân tích được"
			sugg = "Tham số model gửi tới không phân tích được nhưng vẫn thử lại liên tục; hãy xem agentcore có ép kiểu nới lỏng cho dạng đó không (xem #34)."
		default:
			continue // Ordinary tool repetition does not produce a Finding.
		}
		sev := SevWarning
		if r.Count >= repeatCritical {
			sev = SevCritical
		}
		out = append(out, Finding{
			Rule:       rule,
			Category:   CatFlow,
			Severity:   sev,
			Confidence: ConfHigh,
			AutoLevel:  AutoNone,
			Target:     "runtime.flow",
			Title:      title,
			Evidence:   fmt.Sprintf("`%s` ×%d", r.Sig, r.Count),
			Suggestion: sugg,
		})
	}
	return out
}

// stuckStep detects checkpoints resting on the same step consecutively.
func stuckStep(rc *RuntimeCapture) []Finding {
	if rc.StuckStep == "" {
		return nil
	}
	sev := SevWarning
	if rc.StuckCount >= repeatCritical {
		sev = SevCritical
	}
	return []Finding{{
		Rule:       "StuckStep",
		Category:   CatFlow,
		Severity:   sev,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.flow",
		Title:      "checkpoint đình trệ ở cùng một step",
		Evidence:   fmt.Sprintf("Dừng liên tục ở `%s` ×%d", rc.StuckStep, rc.StuckCount),
		Suggestion: "Cùng một step bị ghi lặp mà không tiến triển; kết hợp với chữ ký lặp ở trên để xác định subagent nào đang kẹt.",
	}}
}

// streamIdleStorm detects frequent stream interruptions (#32).
func streamIdleStorm(rc *RuntimeCapture) []Finding {
	n := rc.LogKinds["stream_idle"]
	if n < streamIdleWarn {
		return nil
	}
	return []Finding{{
		Rule:       "StreamIdleStorm",
		Category:   CatFlow,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.provider",
		Title:      "Gián đoạn streaming thường xuyên (stream_idle)",
		Evidence:   fmt.Sprintf("stream_idle ×%d", n),
		Suggestion: "Thượng nguồn lâu không nhả token nên bị watchdog giết nhầm; với model suy nghĩ chậm hãy tăng streamIdleTimeout, hoặc kiểm tra độ ổn định kết nối tới provider (xem #32).",
	}}
}
