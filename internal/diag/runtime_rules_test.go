package diag

import "testing"

// TestRuntimeFindings_Classify proves repeated signatures classify by shape, thresholds escalate and
// de-escalate correctly, and every runtime Finding is AutoNone (observer discipline: diagnose without
// producing an Action).
func TestRuntimeFindings_Classify(t *testing.T) {
	rc := RuntimeCapture{
		Repeats: []RepeatStat{
			{Sig: "writer-ch07 · err: InputValidationError", Count: 14}, // 错误循环 critical
			{Sig: "writer-ch07 · novel_context", Count: 45},             // 正常高频工具 → 不产 Finding
			{Sig: "writer · save_plan (args invalid)", Count: 4},        // 参数无效 warning
		},
		StuckStep:  "writing.commit_ch07",
		StuckCount: 9, // 卡住 critical
		LogKinds:   map[string]int{"stream_idle": 4},
		LogErrors:  270, // 长跑累计，不应单独产 Finding
	}

	fs := runtimeFindings(&rc)
	sev := map[string]Severity{}
	for _, f := range fs {
		sev[f.Rule] = f.Severity
		if f.AutoLevel != AutoNone {
			t.Errorf("%s 应为 AutoNone（观察者纪律），got %s", f.Rule, f.AutoLevel)
		}
	}

	want := map[string]Severity{
		"RepeatedToolError": SevCritical,
		"ArgsInvalidLoop":   SevWarning,
		"StuckStep":         SevCritical,
		"StreamIdleStorm":   SevWarning,
	}
	for rule, w := range want {
		if sev[rule] != w {
			t.Errorf("%s: got %q want %q", rule, sev[rule], w)
		}
	}
	// A normally high-frequency tool or a cumulative log error should not produce a Finding (avoiding false alarms on long runs).
	if _, ok := sev["RepeatedToolCall"]; ok {
		t.Error("普通工具重复不应产 Finding")
	}
	if _, ok := sev["LogErrorBurst"]; ok {
		t.Error("日志 error 累计不应单独产 Finding")
	}
}

// TestRuntimeFindings_Quiet proves no anomalous signal yields no runtime Finding at all (zero false positives).
func TestRuntimeFindings_Quiet(t *testing.T) {
	rc := RuntimeCapture{
		LogKinds:  map[string]int{"stream_idle": 1}, // 低于阈值
		LogErrors: 2,
	}
	if fs := runtimeFindings(&rc); len(fs) != 0 {
		t.Errorf("安静态不应产 Finding，got %d: %+v", len(fs), fs)
	}
}
