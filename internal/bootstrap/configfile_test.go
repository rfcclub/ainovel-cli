package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/errs"
)

const validGlobal = `{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "providers": { "openrouter": { "api_key": "sk-test-123456" } }
}`

// writeGlobal writes global config under an isolated HOME and returns that HOME.
func writeGlobal(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// On Windows os.UserHomeDir reads USERPROFILE; without setting it, the real local ~/.ainovel would be read.
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".ainovel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o644); err != nil {
			t.Fatalf("write global: %v", err)
		}
	}
	return home
}

// writeProjectConfig writes project-level config under ./.ainovel/ in the current working directory.
// The caller must t.Chdir to the target directory first.
func writeProjectConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(".ainovel", 0o755); err != nil {
		t.Fatalf("mkdir .ainovel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(".ainovel", "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write project: %v", err)
	}
}

// Root cause 3: a project-level ./.ainovel/config.json that exists but is bad JSON must error rather than being silently swallowed in favour of the global config.
func TestLoadConfig_CorruptProjectFailsLoud(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	// A hand-copied example with a trailing comma — the most common kind of bad JSON.
	writeProjectConfig(t, `{ "model": "x", }`)

	if _, err := LoadConfig(); err == nil {
		t.Fatal("坏的 ./.ainovel/config.json 应当报错，却被静默忽略了")
	}
}

// The global config is the lowest-priority base: a bad file must not block a higher-priority
// project-level override (a regression guard — the previous version also made the global config
// fail loud, leaving users with "bad global + valid project config" blocked by an irrelevant file).
func TestLoadConfig_CorruptGlobalDoesNotBlockProjectOverride(t *testing.T) {
	writeGlobal(t, `{ not json`)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, validGlobal)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("坏全局不应阻断有效项目级配置，得到: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("应使用项目级配置的值，得到 provider=%q", cfg.Provider)
	}
}

// Edit in place: when the project directory has ./.ainovel/config.json, EffectiveConfigPath points at
// it (an absolute path) and otherwise falls back to the global config — both /config and /model use
// that to decide where to write.
func TestEffectiveConfigPathPrefersProject(t *testing.T) {
	writeGlobal(t, validGlobal)

	t.Chdir(t.TempDir()) // 无项目配置
	if got := EffectiveConfigPath(); got != DefaultConfigPath() {
		t.Fatalf("无项目配置应回落全局，got %q want %q", got, DefaultConfigPath())
	}

	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, validGlobal)
	wantAbs, err := filepath.Abs(filepath.Join(".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if got := EffectiveConfigPath(); got != wantAbs {
		t.Fatalf("有项目配置应写项目，got %q want %q", got, wantAbs)
	}
}

// A missing file is a normal case (portable use / first run) and must not error.
func TestLoadConfig_MissingFilesNoError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // ~/.ainovel/config.json 不存在
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir()) // 也没有 ./.ainovel/config.json

	if _, err := LoadConfig(); err != nil {
		t.Fatalf("缺失配置文件不应报错，得到: %v", err)
	}
}

// The normal path: global + project-level merged and in effect.
func TestLoadConfig_ValidMergeWorks(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "model": "google/gemini-2.5-pro",
  "reasoning_effort": "high",
  "roles": {
    "writer": {
      "provider": "openrouter",
      "model": "google/gemini-2.5-flash",
      "reasoning_effort": "low"
    }
  }
}`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("有效配置不应报错: %v", err)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("provider 应保留全局值 openrouter，得到 %q", cfg.Provider)
	}
	if cfg.ModelName != "google/gemini-2.5-pro" {
		t.Errorf("model 应被项目级覆盖，得到 %q", cfg.ModelName)
	}
	if cfg.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort 应被项目级覆盖，得到 %q", cfg.ReasoningEffort)
	}
	if got := cfg.Roles["writer"].ReasoningEffort; got != "low" {
		t.Errorf("roles.writer.reasoning_effort 应被项目级覆盖，得到 %q", got)
	}
}

func TestMergeConfig_ProviderExtraFields(t *testing.T) {
	base := Config{
		Provider:  "openrouter",
		ModelName: "google/gemini-2.5-flash",
		Providers: map[string]ProviderConfig{
			"openrouter": {
				API:    "chat",
				APIKey: "sk-test-123456",
				ExtraBody: map[string]any{
					"temperature": 0.8,
				},
				Extra: map[string]any{
					"user_agent": "base-client/1.0",
				},
			},
		},
	}
	overlay := Config{
		Providers: map[string]ProviderConfig{
			"openrouter": {
				API:     "responses",
				BaseURL: "https://proxy.example.com/v1",
				ExtraBody: map[string]any{
					"min_p": 0.05,
				},
				Extra: map[string]any{
					"user_agent": "override-client/1.0",
					"headers": map[string]any{
						"X-Custom-Client": "ainovel",
					},
				},
			},
		},
	}

	cfg := mergeConfig(base, overlay)
	pc := cfg.Providers["openrouter"]
	if pc.APIKey != "sk-test-123456" {
		t.Fatalf("APIKey = %q, want inherited key", pc.APIKey)
	}
	if pc.API != "responses" {
		t.Fatalf("API = %q, want responses", pc.API)
	}
	if pc.BaseURL != "https://proxy.example.com/v1" {
		t.Fatalf("BaseURL = %q, want overlay URL", pc.BaseURL)
	}
	if _, ok := pc.ExtraBody["temperature"]; ok {
		t.Fatalf("ExtraBody should be replaced by overlay, got %#v", pc.ExtraBody)
	}
	if got := pc.ExtraBody["min_p"]; got != 0.05 {
		t.Fatalf("ExtraBody[min_p] = %#v, want 0.05", got)
	}
	if got := pc.Extra["user_agent"]; got != "override-client/1.0" {
		t.Fatalf("Extra[user_agent] = %#v, want override-client/1.0", got)
	}
	headers, ok := pc.Extra["headers"].(map[string]any)
	if !ok {
		t.Fatalf("Extra[headers] missing or invalid: %#v", pc.Extra["headers"])
	}
	if got := headers["X-Custom-Client"]; got != "ainovel" {
		t.Fatalf("Extra.headers[X-Custom-Client] = %#v, want ainovel", got)
	}
}

// Root cause 2 (the core reproduction of issue #37): a project-level override names a provider without
// declaring its credentials, so ValidateBase must report a config error (rather than letting it through
// and crashing deeper down).
func TestValidateBase_ProviderOverrideWithoutCredentials(t *testing.T) {
	cfg := Config{
		Provider:  "mimo",
		ModelName: "mimo-v2.5-pro",
		Providers: map[string]ProviderConfig{
			"openrouter": {APIKey: "sk-test-123456"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("provider 缺凭证应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

func TestValidateBaseRejectsInvalidProviderAPI(t *testing.T) {
	cfg := Config{
		Provider:  "openai",
		ModelName: "gpt-5.1",
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-test-123456", API: "legacy"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("provider api 非法应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

func TestValidateBaseRejectsProviderAPIOnNonOpenAIProvider(t *testing.T) {
	cfg := Config{
		Provider:  "anthropic",
		ModelName: "claude-sonnet-4",
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKey: "sk-test-123456", API: "responses"},
		},
	}
	cfg.FillDefaults()
	err := cfg.ValidateBase()
	if err == nil {
		t.Fatal("非 OpenAI provider 配置 api 应报错")
	}
	if !errors.Is(err, errs.ErrConfig) {
		t.Errorf("应包装 errs.ErrConfig，得到: %v", err)
	}
}

// The example config must be self-consistent: valid JSON once comments are stripped, no dangling
// top-level provider pointer, and it makes the "pointer" mental model clear — it is the template users
// copy, so breaking it burns them.
func TestExampleConfigIsValidAndSelfConsistent(t *testing.T) {
	if exampleConfig == "" {
		t.Fatal("go:embed 未生效，exampleConfig 为空")
	}
	rootExample, err := os.ReadFile(filepath.Join("..", "..", "config.example.jsonc"))
	if err != nil {
		t.Fatalf("读取根目录 config.example.jsonc: %v", err)
	}
	if string(rootExample) != exampleConfig {
		t.Fatal("根目录 config.example.jsonc 与 internal/bootstrap/config.example.jsonc 不一致")
	}
	var cfg Config
	if err := json.Unmarshal(stripJSONComments([]byte(exampleConfig)), &cfg); err != nil {
		t.Fatalf("内置示例去注释后不是合法 JSON（用户照抄即坑）: %v", err)
	}
	if cfg.Provider == "" || cfg.ModelName == "" {
		t.Fatal("示例应给出默认 provider/model")
	}
	if _, ok := cfg.Providers[cfg.Provider]; !ok {
		t.Errorf("provider tầng trên cùng %q trong ví dụ không trỏ tới mục nào trong providers — chính mẫu con trỏ bị treo lơ lửng", cfg.Provider)
	}
	if !contains(exampleConfig, "con trỏ") {
		t.Error("ví dụ phải nói rõ \"provider là một con trỏ\" — đừng để cái bẫy nhận thức của #37 quay lại")
	}
}

func TestWriteStartupError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := WriteStartupError("boom: provider not configured")
	if path == "" {
		t.Fatal("应返回落盘路径")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 last-error.log: %v", err)
	}
	if want := "boom: provider not configured"; !contains(string(data), want) {
		t.Errorf("日志应包含 %q，实际: %s", want, data)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
