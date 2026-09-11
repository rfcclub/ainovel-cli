package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const configDirName = ".ainovel"

// DefaultConfigPath returns the global configuration path ~/.ainovel/config.json.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName, "config.json")
}

// DefaultConfigDir returns the ~/.ainovel directory path, or an empty string when the home directory
// cannot be resolved. It is only used for reading/writing files that need not exist (such as the
// model cache) and never creates the directory.
func DefaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName)
}

// configDir returns the ~/.ainovel directory path, creating it when absent.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, configDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return dir, nil
}

// projectConfigPath returns the project-level configuration path ./.ainovel/config.json.
// The project dotdir mirrors the global ~/.ainovel/ and reuses the same configDirName; it resolves
// relative to the cwd.
func projectConfigPath() string {
	return filepath.Join(configDirName, "config.json")
}

// EffectiveConfigPath returns the configuration file TUI changes (/config, /model) should write back
// to: when the project has ./.ainovel/config.json, write that — consistent with the project layer
// overriding the global one on read, so "edit the copy in effect" and the change takes effect at
// once; otherwise write the global ~/.ainovel/config.json.
// It only edits an existing project configuration and never creates one from nothing (creating a
// project override is the user deliberately placing a file).
func EffectiveConfigPath() string {
	rel := projectConfigPath()
	if _, err := os.Stat(rel); err == nil {
		if abs, err := filepath.Abs(rel); err == nil {
			return abs
		}
		return rel
	}
	return DefaultConfigPath()
}

// LoadConfig loads and merges the configuration by priority:
//  1. ~/.ainovel/config.json (global)
//  2. ./.ainovel/config.json (project-level override)
func LoadConfig() (Config, error) {
	var cfg Config

	// 1. Global configuration. It is the lowest-priority base, and a bad file degrades to a warning
	//    rather than blocking — it can be overridden at project level, and a hard failure would lock out
	//    a user with "a broken global plus a valid project config".
	if p := DefaultConfigPath(); p != "" {
		global, found, err := loadOptionalJSON(p)
		switch {
		case err != nil:
			slog.Warn("phân tích cấu hình toàn cục thất bại, đã bỏ qua (có thể bị ghi đè ở tầng dự án)", "module", "config", "path", p, "err", err)
		case found:
			cfg = global
		}
	}

	// 2. Project-level override. A bad file fails loud: it is a configuration the user deliberately
	//    placed in this directory, and swallowing it silently makes "configured but not taking effect"
	//    impossible to diagnose (issue #37).
	project, found, err := loadOptionalJSON(projectConfigPath())
	if err != nil {
		return cfg, fmt.Errorf("phân tích cấu hình tầng dự án ./.ainovel/config.json thất bại (hãy kiểm tra cú pháp JSON): %w", err)
	}
	if found {
		cfg = mergeConfig(cfg, project)
	}

	return cfg, nil
}

// loadOptionalJSON reads an optional configuration file:
//   - File absent -> (zero, false, nil), leaving the caller to choose a default or an upper-layer value.
//   - File present but failing to parse -> an error (no longer swallowed silently — otherwise a user's
//     configuration "configured but not taking effect" cannot be diagnosed, which is the root cause of
//     issue #37).
func loadOptionalJSON(path string) (Config, bool, error) {
	cfg, err := loadJSONFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	return cfg, true, nil
}

// LoadConfigFile reads a single JSON configuration file and supports // line comments.
// It performs no merging and returns only that file's configuration. A missing file is an error.
func LoadConfigFile(path string) (Config, error) {
	return loadJSONFile(path)
}

// loadJSONFile reads a JSON configuration file and supports // line comments.
// A missing file is an error (the caller decides whether to ignore it).
func loadJSONFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cleaned := stripJSONComments(data)
	var cfg Config
	if err := json.Unmarshal(cleaned, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// mergeConfig merges overlay onto base. Non-zero fields override and maps merge by key.
func mergeConfig(base, overlay Config) Config {
	if overlay.Provider != "" {
		base.Provider = overlay.Provider
	}
	if overlay.ModelName != "" {
		base.ModelName = overlay.ModelName
	}
	if overlay.ReasoningEffort != "" {
		base.ReasoningEffort = overlay.ReasoningEffort
	}
	if overlay.Style != "" {
		base.Style = overlay.Style
	}
	if overlay.ContextWindow > 0 {
		base.ContextWindow = overlay.ContextWindow
	}

	// Providers: an overlay key overrides the same-named base key.
	if len(overlay.Providers) > 0 {
		if base.Providers == nil {
			base.Providers = make(map[string]ProviderConfig)
		}
		for k, v := range overlay.Providers {
			existing := base.Providers[k]
			if v.Type != "" {
				existing.Type = v.Type
			}
			if v.API != "" {
				existing.API = v.API
			}
			if v.APIKey != "" {
				existing.APIKey = v.APIKey
			}
			if v.BaseURL != "" {
				existing.BaseURL = v.BaseURL
			}
			if len(v.Models) > 0 {
				existing.Models = append([]ModelConfig(nil), v.Models...)
			}
			if len(v.ExtraBody) > 0 {
				existing.ExtraBody = cloneMap(v.ExtraBody)
			}
			if len(v.Extra) > 0 {
				existing.Extra = cloneMap(v.Extra)
			}
			base.Providers[k] = existing
		}
	}

	// Roles: an overlay key overrides the same-named base key.
	if len(overlay.Roles) > 0 {
		if base.Roles == nil {
			base.Roles = make(map[string]RoleConfig)
		}
		for k, v := range overlay.Roles {
			existing := base.Roles[k]
			if v.Provider != "" {
				existing.Provider = v.Provider
			}
			if v.Model != "" {
				existing.Model = v.Model
			}
			if len(v.Fallbacks) > 0 {
				existing.Fallbacks = append([]ModelRef(nil), v.Fallbacks...)
			}
			if v.ReasoningEffort != "" {
				existing.ReasoningEffort = v.ReasoningEffort
			}
			base.Roles[k] = existing
		}
	}

	// Budget / Notify: whole-block override (a project-level budget/alerting block is an independent
	// policy declaration, not stitched field by field onto the global one).
	if overlay.Budget != (BudgetConfig{}) {
		base.Budget = overlay.Budget
	}
	if overlay.Notify.Enabled != nil || overlay.Notify.Command != "" || len(overlay.Notify.Events) > 0 {
		base.Notify = overlay.Notify
	}

	return base
}

func cloneMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// CloneConfig deep-copies the maps/slices that get mutated at runtime, so a candidate configuration
// cannot pollute the current one.
func CloneConfig(cfg Config) Config {
	clone := cfg
	clone.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		pc.Models = append([]ModelConfig(nil), pc.Models...)
		pc.Extra = cloneMap(pc.Extra)
		pc.ExtraBody = cloneMap(pc.ExtraBody)
		clone.Providers[name] = pc
	}
	clone.Roles = make(map[string]RoleConfig, len(cfg.Roles))
	for role, rc := range cfg.Roles {
		rc.Fallbacks = append([]ModelRef(nil), rc.Fallbacks...)
		clone.Roles[role] = rc
	}
	clone.Notify.Events = append([]string(nil), cfg.Notify.Events...)
	return clone
}

// SaveProviderConfig patch-updates one provider's credentials and model list in the target
// configuration layer. It touches only the providers section and never the top-level provider/model
// choice — "which one is in use" belongs to /model. It creates a minimal configuration when the
// target is absent and refuses to overwrite a corrupted one.
func SaveProviderConfig(path string, provider string, pc ProviderConfig) error {
	target, found, err := loadOptionalJSON(path)
	if err != nil {
		return err
	}
	if !found {
		target = Config{}
	}
	if target.Providers == nil {
		target.Providers = make(map[string]ProviderConfig)
	}
	target.Providers[provider] = pc
	return SaveConfig(path, target)
}

// stripJSONComments removes // line comments from JSON, tracking quote state so string content is
// never deleted by mistake.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		b := data[i]

		if escaped {
			out = append(out, b)
			escaped = false
			continue
		}

		if inString {
			out = append(out, b)
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		// Not inside a string.
		if b == '"' {
			inString = true
			out = append(out, b)
			continue
		}

		// Detect a // comment.
		if b == '/' && i+1 < len(data) && data[i+1] == '/' {
			// Skip to the end of the line.
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
			continue
		}

		out = append(out, b)
	}

	return out
}

// WriteStartupError appends a fatal startup error to ~/.ainovel/last-error.log and returns that path
// (best-effort; an empty string on failure). When launched by double-clicking, the console window
// closes the moment the process exits and the error flashes by; writing it to disk is the only way
// such a user can look it up afterwards.
func WriteStartupError(msg string) string {
	dir := DefaultConfigDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, "last-error.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "[%s] %s\n", time.Now().Format(time.RFC3339), msg); err != nil {
		return ""
	}
	return path
}

// SaveConfig writes the configuration to the given path (JSON, pretty-printed).
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
