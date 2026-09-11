package imp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// workspaceSchemaVersion is the overall schema version of the import workspace.
// On a mismatch it explicitly requires continuing with a matching version or re-importing, never guessing at a migration (RFC §6.1).
const workspaceSchemaVersion = 1

// Digest computes a content digest, following the repo's existing "sha256:"+hex convention (see store/checkpoints.go).
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Artifact is the uniform identity of every semantic artifact in the workspace: schema version + input digest
// + payload.
// It is reusable only when the same InputDigest can be rebuilt from the current real semantic inputs (RFC
// §6.3 / invariant 1).
// No dependency graph is implemented: LoadState walks the fixed linear pipeline comparing InputDigest to decide
// reuse and invalidation, and NextAction derives the next step from that.
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}

// Manifest corresponds to one normalised source snapshot and is workspace identity rather than a derived
// artifact (RFC §6.1).
// It stores no absolute source path, avoiding leaking machine directories and removing the recovery problems
// that moving a file would cause.
type Manifest struct {
	Version          int    `json:"version"`
	SourceName       string `json:"source_name"`
	RawSHA256        string `json:"raw_sha256"`
	NormalizedSHA256 string `json:"normalized_sha256"`
	Encoding         string `json:"encoding"`
	SizeBytes        int64  `json:"size_bytes"`
	CreatedAt        string `json:"created_at"`
}

// Intent stores the explicit user authorisation from when the import started; it must still be honoured after recovery, is never guessed from artifacts, and is never silently rewritten by the Runner (RFC §6.1).
type Intent struct {
	Version             int    `json:"version"`
	AutoConfirm         bool   `json:"auto_confirm,omitempty"`
	StoryResolution     string `json:"story_resolution,omitempty"` // open / closed
	ContinueAfterImport bool   `json:"continue_after_import,omitempty"`
}

// Standard workspace artifact relative paths.
const (
	fileManifest     = "manifest.json"
	fileIntent       = "intent.json"
	fileSource       = "source.txt"
	fileGuidance     = "guidance.txt"
	fileSegmentation = "segmentation.json"
	fileConfirmation = "confirmation.json"
	fileSynthesis    = "synthesis.json"
	fileStoryResolve = "story-resolution.json"
	dirAnalyses      = "analyses"
	dirRangeDigests  = "range-digests"
	dirSegmentChunks = "segment-chunks"
	dirFailures      = "failures"
)

// Workspace is the atomic artifact read/write handle for the <book root>/meta/import/ directory.
type Workspace struct {
	dir string
}

// OpenWorkspace returns a handle pointing at meta/import/ under the book root; the directory is not guaranteed to exist, so use Active().
func OpenWorkspace(bookDir string) *Workspace {
	return &Workspace{dir: filepath.Join(bookDir, "meta", "import")}
}

// Dir returns the workspace's absolute path (for diagnostics and failure-artifact locations).
func (w *Workspace) Dir() string { return w.dir }

func (w *Workspace) path(rel string) string { return filepath.Join(w.dir, rel) }

// Active reports whether a published, active workspace exists. A missing meta/import/ does not count as active,
// and a half-initialised directory exists as meta/import.init-* so it cannot be misread as active (RFC §6.1).
func (w *Workspace) Active() bool {
	fi, err := os.Stat(w.dir)
	return err == nil && fi.IsDir()
}

func (w *Workspace) has(rel string) bool {
	_, err := os.Stat(w.path(rel))
	return err == nil
}

// writeAtomic writes rel (relative to the workspace) atomically via temp file + fsync + rename.
func (w *Workspace) writeAtomic(rel string, data []byte) error {
	full := w.path(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), filepath.Base(full)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return err
	}
	syncDir(filepath.Dir(full))
	return nil
}

// syncDir best-effort fsyncs the directory entry so a just-completed rename survives a power loss.
// Platforms such as Windows may not support directory Sync and their errors are ignored — process-crash safety
// does not depend on it; it only covers the power-loss case (RFC §12.3).
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func (w *Workspace) writeJSON(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return w.writeAtomic(rel, append(data, '\n'))
}

func (w *Workspace) readJSON(rel string, v any) error {
	data, err := os.ReadFile(w.path(rel))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// LoadManifest reads the workspace source-snapshot identity.
func (w *Workspace) LoadManifest() (*Manifest, error) {
	var m Manifest
	if err := w.readJSON(fileManifest, &m); err != nil {
		return nil, err
	}
	if m.Version != workspaceSchemaVersion {
		return nil, fmt.Errorf("phiên bản schema manifest %d != %d, hãy tiếp tục bằng phiên bản khớp hoặc nhập lại", m.Version, workspaceSchemaVersion)
	}
	return &m, nil
}

// LoadIntent reads the user's start authorisation.
func (w *Workspace) LoadIntent() (*Intent, error) {
	var in Intent
	if err := w.readJSON(fileIntent, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// LoadSource reads the normalised source snapshot text.
func (w *Workspace) LoadSource() ([]byte, error) {
	return os.ReadFile(w.path(fileSource))
}

// LoadGuidance reads the user's segmentation guidance (RFC §18.3); missing means no guidance.
// Guidance is, like source.txt, a semantic input to segmentation rather than a derived artifact, updated by an
// explicit --guide; a content change makes segmentation and everything downstream miss naturally.
func (w *Workspace) LoadGuidance() (string, error) {
	data, err := os.ReadFile(w.path(fileGuidance))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readBytes reads an artifact's raw bytes for downstream InputDigest binding.
func (w *Workspace) readBytes(rel string) ([]byte, error) {
	return os.ReadFile(w.path(rel))
}

// writeArtifact writes a semantic artifact with uniform identity.
func writeArtifact[T any](w *Workspace, rel, inputDigest string, payload T) error {
	return w.writeJSON(rel, Artifact[T]{
		SchemaVersion: workspaceSchemaVersion,
		InputDigest:   inputDigest,
		Payload:       payload,
	})
}

// readArtifact reads a semantic artifact and validates the schema version; whether the InputDigest matches is for the caller to decide against current inputs.
func readArtifact[T any](w *Workspace, rel string) (*Artifact[T], error) {
	var a Artifact[T]
	if err := w.readJSON(rel, &a); err != nil {
		return nil, err
	}
	if a.SchemaVersion != workspaceSchemaVersion {
		return nil, fmt.Errorf("%s có phiên bản schema %d != %d, hãy tiếp tục bằng phiên bản khớp hoặc nhập lại", rel, a.SchemaVersion, workspaceSchemaVersion)
	}
	return &a, nil
}

// clearDir deletes one intermediate cache directory inside the workspace. Errors must be handed to the caller:
// swallowing one would make the "cleared" wording a lie — the next rerun would reuse the bad cache anyway
// (Windows antivirus / handle locks are a real scenario, Debug-First).
func (w *Workspace) clearDir(rel string) error {
	return os.RemoveAll(w.path(rel))
}

// FailureMeta is the diagnostic metadata of the most recent failure (RFC §14.2).
type FailureMeta struct {
	Stage         string `json:"stage"`
	Detail        string `json:"detail"`
	StopReason    string `json:"stop_reason,omitempty"`
	PrefixSalvage string `json:"prefix_salvage,omitempty"` // available:N / unavailable
}

// writeFailure best-effort saves the most recent failure's metadata and the untrimmed raw model response into
// failures/ (RFC §14.2).
// The raw response may contain prose, so it lands only in the user's own book directory and never in ordinary
// logs or redacted diagnostic exports.
func (w *Workspace) writeFailure(meta FailureMeta, rawResponse string) {
	_ = w.writeJSON(filepath.Join(dirFailures, "last.json"), meta)
	_ = w.writeAtomic(filepath.Join(dirFailures, "last-response.txt"), []byte(rawResponse))
}

// createWorkspace writes manifest/intent/source completely into a temp directory, validates them, then publishes
// atomically as meta/import/ by renaming the directory.
// This keeps the initial triple from reaching NextAction half-initialised and makes stage=initializing
// unnecessary (RFC §6.1).
func createWorkspace(bookDir string, m Manifest, in Intent, normalized []byte) (*Workspace, error) {
	base := filepath.Join(bookDir, "meta")
	final := filepath.Join(base, "import")
	if fi, err := os.Stat(final); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("workspace nhập liệu đã tồn tại: %s (chạy /import không tham số để khôi phục từ đó)", final)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(base, "import.init-*")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmp)
		}
	}()

	tw := &Workspace{dir: tmp}
	if err := tw.writeAtomic(fileSource, normalized); err != nil {
		return nil, err
	}
	if err := tw.writeJSON(fileManifest, m); err != nil {
		return nil, err
	}
	if err := tw.writeJSON(fileIntent, in); err != nil {
		return nil, err
	}
	// Before publishing, the triple is validated as readable and the source snapshot as matching the manifest, ruling out a half-written workspace.
	got, err := tw.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("kiểm tra manifest ban đầu: %w", err)
	}
	src, err := tw.LoadSource()
	if err != nil {
		return nil, fmt.Errorf("kiểm tra ảnh chụp nguồn ban đầu: %w", err)
	}
	if d := Digest(src); d != got.NormalizedSHA256 {
		return nil, fmt.Errorf("tóm tắt ảnh chụp nguồn ban đầu không khớp: %s != %s", d, got.NormalizedSHA256)
	}
	if _, err := tw.LoadIntent(); err != nil {
		return nil, fmt.Errorf("kiểm tra intent ban đầu: %w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	syncDir(base)
	committed = true
	return &Workspace{dir: final}, nil
}
