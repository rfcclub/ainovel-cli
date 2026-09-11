package rules

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RawSource is one raw source pending normalisation (the whole text of a rules file).
//
// Since YAML was dropped, a rules file is just a plain natural-language prompt;
// normalisation needs only the raw text and no longer parses front matter.
type RawSource struct {
	Label string     // Source label, recorded in Snapshot.Sources (e.g. global:my-style.md)
	Kind  SourceKind // Priority tier
	Text  string     // Raw file content
}

// RawFileSources enumerates the .md files under the rules directories in Global ->
// Project order and returns their raw text.
//
// It follows the same scanning convention as readDirFromDisk (top-level .md files,
// lexicographic order, hidden files skipped) but does not parse YAML, handing the whole
// text to the normaliser untouched. System defaults / the startup prompt / runtime
// requirements are supplied separately by the service.
func RawFileSources(opts LoadOptions) []RawSource {
	var out []RawSource
	out = append(out, rawDir(opts.HomeRulesDir, SourceGlobal)...)
	out = append(out, rawDir(opts.ProjectRulesDir, SourceProject)...)
	return out
}

func rawDir(dir string, kind SourceKind) []RawSource {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A missing directory is normal and skipped silently, but errors such as bad
		// permissions or a path that is actually a file must leave a trace — otherwise a
		// user writes rules that never take effect with zero feedback, which is extremely
		// expensive to diagnose (see known_rules_path_stale_readme).
		if !os.IsNotExist(err) {
			slog.Warn("đọc thư mục quy tắc thất bại, đã bỏ qua", "module", "rules", "dir", dir, "err", err)
		}
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var out []RawSource
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("đọc file quy tắc thất bại, đã bỏ qua", "module", "rules", "file", path, "err", err)
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		out = append(out, RawSource{
			Label: kind.String() + ":" + name,
			Kind:  kind,
			Text:  text,
		})
	}
	return out
}
