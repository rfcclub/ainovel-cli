package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAllowsFilter(t *testing.T) {
	if New("", nil).allows(KindDeadlock) != true {
		t.Error("mặc định events phải cho qua tất cả")
	}
	n := New("", []string{KindRunEnd, KindBudget})
	if !n.allows(KindRunEnd) || !n.allows(KindBudget) {
		t.Error("kind có trong danh sách phải được cho qua")
	}
	if n.allows(KindDeadlock) {
		t.Error("kind không có trong danh sách phải bị chặn")
	}
	var nilN *Notifier
	if nilN.allows(KindRunEnd) {
		t.Error("Notifier nil phải chặn mọi thứ")
	}
	nilN.Send(Notification{Kind: KindRunEnd}) // Must not panic.
}

func TestKindsAreUniqueAndKnown(t *testing.T) {
	seen := map[string]bool{}
	for _, kind := range Kinds() {
		if kind == "" || seen[kind] {
			t.Fatalf("tên sự kiện thông báo phải khác rỗng và duy nhất: %q", kind)
		}
		seen[kind] = true
		if !IsKnownKind(kind) {
			t.Fatalf("Kinds và IsKnownKind không nhất quán: %q", kind)
		}
	}
	if IsKnownKind("repeat") {
		t.Fatal("sự kiện repeat cũ không được tiếp tục xuất hiện trong contract mới")
	}
}

func TestCommandChannelEnvAndStdin(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	jsonFile := filepath.Join(dir, "stdin.json")

	command := `echo "$NOTIFY_KIND|$NOTIFY_LEVEL|$NOTIFY_TITLE|$NOTIFY_BODY" > ` + shellQuote(envFile) + ` && cat > ` + shellQuote(jsonFile)
	if runtime.GOOS == "windows" {
		// Explicit UTF-8 (no BOM) so Chinese title/body survive PowerShell's default code page.
		command = `$utf8 = New-Object System.Text.UTF8Encoding $false; ` +
			`$line = "$env:NOTIFY_KIND|$env:NOTIFY_LEVEL|$env:NOTIFY_TITLE|$env:NOTIFY_BODY"; ` +
			`[System.IO.File]::WriteAllText(` + powerShellQuote(envFile) + `, $line, $utf8); ` +
			`$reader = New-Object System.IO.StreamReader([Console]::OpenStandardInput(), $utf8); ` +
			`$payload = $reader.ReadToEnd(); ` +
			`[System.IO.File]::WriteAllText(` + powerShellQuote(jsonFile) + `, $payload, $utf8)`
	}
	n := New(command, nil)
	nt := Notification{Kind: KindBudget, Level: "warn", Title: "ainovel: ngân sách", Body: "đã chi $8.00"}
	if err := n.deliverError(nt); err != nil {
		t.Fatalf("thực thi command thất bại: %v", err)
	}

	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("command chưa được thực thi: %v", err)
	}
	if got := strings.TrimSpace(string(env)); got != "budget|warn|ainovel: ngân sách|đã chi $8.00" {
		t.Errorf("truyền qua biến môi trường không khớp: %q", got)
	}

	raw, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("stdin chưa được truyền: %v", err)
	}
	var decoded Notification
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("stdin không phải JSON hợp lệ: %v", err)
	}
	if decoded != nt {
		t.Errorf("JSON trên stdin không khớp: %+v", decoded)
	}
}

func TestCommandChannelTimeoutKill(t *testing.T) {
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	n := New(command, nil)
	n.timeout = 200 * time.Millisecond

	start := time.Now()
	err := n.deliverError(Notification{Kind: KindRunEnd})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lệnh quá thời gian phải trả context deadline exceeded, nhận %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("quá thời gian nhưng không bị kill cứng, bị chặn %v", elapsed)
	}
}

func TestFindPowerShellPrefersPwsh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("việc chọn PowerShell chỉ áp dụng trên Windows")
	}
	want, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Skip("pwsh.exe chưa cài, chỉ kiểm tra nhánh tương thích Windows PowerShell")
	}
	got, err := findPowerShell()
	if err != nil {
		t.Fatalf("findPowerShell: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("phải ưu tiên pwsh.exe, nhận %q, mong đợi %q", got, want)
	}
}

func TestWindowsNotificationScriptUsesEnvironmentWithoutInterpolation(t *testing.T) {
	for _, want := range []string{"$env:NOTIFY_TITLE", "$env:NOTIFY_BODY", "$env:NOTIFY_LEVEL", "ShowBalloonTip"} {
		if !strings.Contains(windowsNotificationScript, want) {
			t.Fatalf("Windows notification script missing %q", want)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func powerShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
