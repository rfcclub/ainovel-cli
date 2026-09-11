package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
)

// sessionRecord is the lightweight parse shape of one meta/sessions/*.jsonl record — only the fields
// accumulated usage needs. Large fields such as Content are skipped to save startup IO.
//
// Model attribution degrades in three steps:
//  1. Usage.Provider/Model — the real responding model passed through by agentcore/litellm (preferred)
//  2. Meta(_meta)          — when the upstream does not pass it through, the "effective at the time"
//     model added on the write side by ModelLookup
//  3. neither              — replay falls back to effectiveModel, inferred from the current ModelSet
//     (accuracy suffers)
type sessionRecord struct {
	Role  agentcore.Role     `json:"role"`
	Usage *agentcore.Usage   `json:"usage,omitempty"`
	Meta  *sessionRecordMeta `json:"_meta,omitempty"`
}

type sessionRecordMeta struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// ReplaySessions scans meta/sessions/agents/*.jsonl and re-accumulates each assistant message's usage
// into the tracker. It returns the number of entries refilled.
//
// Call constraint: the refill runs once, only when meta/usage.json is missing.
// Day-to-day persistence goes through SaveNow / autoSaveLoop.
//
// Accuracy depends on the three-step degradation documented on sessionRecord — step 3 (both Usage and
// _meta missing) only triggers on older logs or upstream faults.
func (t *UsageTracker) ReplaySessions(rootDir string) (int, error) {
	if t == nil {
		return 0, nil
	}
	sessionsDir := filepath.Join(rootDir, "meta", "sessions")
	info, err := os.Stat(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, nil
	}

	total := 0
	agentsDir := filepath.Join(sessionsDir, "agents")
	walkErr := filepath.WalkDir(agentsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		agentName := parseAgentNameFromFile(name)
		if agentName == "" {
			return nil
		}
		n, fileErr := t.replayFile(path, agentName)
		if fileErr != nil {
			slog.Warn("replay agent session failed", "module", "usage", "file", name, "err", fileErr)
			return nil
		}
		total += n
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return total, walkErr
	}
	return total, nil
}

// replayFile scans a single jsonl file and feeds every assistant message carrying Usage to accumulate.
// agentName is parsed by the caller from the Worker session filename.
func (t *UsageTracker) replayFile(path, agentName string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	role := agentRoleName(agentName)
	count := 0
	scanner := bufio.NewScanner(f)
	// A single line can be very long (assistant message plus tool args flattened together), so the limit is relaxed to 4MB.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Role != agentcore.RoleAssistant || rec.Usage == nil {
			continue
		}
		provider, modelName := usageActualModel(rec.Usage)
		if rec.Meta != nil {
			if provider == "" {
				provider = rec.Meta.Provider
			}
			if modelName == "" {
				modelName = rec.Meta.Model
			}
		}
		t.accumulate(role, provider, modelName, *rec.Usage)
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("scan %s: %w", path, err)
	}
	return count, nil
}

// parseAgentNameFromFile extracts the agent name (the part before "-") from "writer-ch01.jsonl" /
// "architect_short-001.jsonl". For the naming convention see store/session.go::subAgentPath: the
// agentName contains no dash and the suffix is ch<n> or an incrementing sequence number.
func parseAgentNameFromFile(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	if i := strings.Index(base, "-"); i > 0 {
		return base[:i]
	}
	return ""
}
