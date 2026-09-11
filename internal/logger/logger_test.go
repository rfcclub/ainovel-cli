package logger

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSetupFileWritesDefaultLog(t *testing.T) {
	previous := slog.Default()

	dir := t.TempDir()
	cleanup, err := SetupFile(dir, "test.log", false,
		slog.String("version", "v1.2.3"),
		slog.String("commit", "abc123"),
		slog.String("built", "2026-08-03"),
	)
	if err != nil {
		t.Fatalf("SetupFile: %v", err)
	}
	slog.Info("logger-test-message")
	cleanup()
	if slog.Default() != previous {
		t.Fatal("cleanup phải khôi phục logger mặc định trước đó")
	}

	data, err := os.ReadFile(filepath.Join(dir, "logs", "test.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "logger-test-message") {
		t.Fatalf("log missing message: %q", data)
	}
	if !regexp.MustCompile(`time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2})`).Match(data) {
		t.Fatalf("thời gian trong log phải gồm ngày, mili giây và múi giờ: %q", data)
	}
	if !strings.Contains(string(data), "msg=\"bắt đầu phiên log\"") || !strings.Contains(string(data), "session=") {
		t.Fatalf("log phải chứa ranh giới phiên và thuộc tính session có thể liên kết: %q", data)
	}
	for _, want := range []string{"version=v1.2.3", "commit=abc123", "built=2026-08-03"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("log phải chứa định danh bản dựng %q: %q", want, data)
		}
	}
}

func TestSetupFileReturnsOpenError(t *testing.T) {
	previous := slog.Default()
	var fallback bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&fallback, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := SetupFile(blocker, "test.log", false)
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("khi không tạo được thư mục log thì phải trả lỗi")
	}
	if cleanup != nil {
		t.Fatal("khi thất bại không được trả hàm dọn dẹp")
	}
	slog.Info("fallback-remains-visible")
	if !strings.Contains(fallback.String(), "fallback-remains-visible") {
		t.Fatal("sau khi khởi tạo log ghi file thất bại phải giữ nguyên logger mặc định cũ")
	}
}
