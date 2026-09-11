package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const checkpointsFile = "meta/checkpoints.jsonl"

// CheckpointStore manages the append and query of step-level checkpoints.
// Disk format: meta/checkpoints.jsonl, append-only; queries go through the in-memory mirror.
// Invariant: cache mirrors checkpoints.jsonl and is maintained at the single points Append/Reset.
// Concurrency: cache is protected by io.mu — writes take Lock, reads take RLock.
type CheckpointStore struct {
	io      *IO
	seqGen  atomic.Int64
	cache   []domain.Checkpoint
	loadErr error
}

// NewCheckpointStore creates the checkpoint store, loading existing checkpoints into cache in one pass from disk.
func NewCheckpointStore(io *IO) *CheckpointStore {
	cs := &CheckpointStore{io: io}
	cs.loadFromDisk()
	return cs
}

// loadFromDisk reads the disk jsonl into cache in one pass and restores seqGen.
func (cs *CheckpointStore) loadFromDisk() {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()

	cs.cache, cs.loadErr = readCheckpointsFile(cs.io.path(checkpointsFile))
	var maxSeq int64
	for _, cp := range cs.cache {
		if cp.Seq > maxSeq {
			maxSeq = cp.Seq
		}
	}
	cs.seqGen.Store(maxSeq)
}

// Append appends one checkpoint.
// Idempotent: an existing Scope + Step + Digest is skipped and the existing record returned directly.
func (cs *CheckpointStore) Append(scope domain.Scope, step, artifact, digest string) (*domain.Checkpoint, error) {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	if cs.loadErr != nil {
		return nil, fmt.Errorf("khởi tạo checkpoint store thất bại: %w", cs.loadErr)
	}

	if digest != "" {
		for i := len(cs.cache) - 1; i >= 0; i-- {
			cp := cs.cache[i]
			if cp.Scope.Matches(scope) && cp.Step == step && cp.Digest == digest {
				return &cp, nil
			}
		}
	}

	// seq advances only after a successful write, so a failed write cannot leave a permanent gap.
	// io.mu's write lock is already held, so nothing preempts between the Load and the Store.
	seq := cs.seqGen.Load() + 1
	cp := domain.Checkpoint{
		Seq:        seq,
		Scope:      scope,
		Step:       step,
		Artifact:   artifact,
		Digest:     digest,
		OccurredAt: time.Now(),
	}

	data, err := json.Marshal(cp)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := cs.io.AppendLineUnlocked(checkpointsFile, data); err != nil {
		return nil, err
	}
	cs.seqGen.Store(seq)
	cs.cache = append(cs.cache, cp)
	return &cp, nil
}

// AppendArtifact computes the artifact content fingerprint and then appends a checkpoint.
func (cs *CheckpointStore) AppendArtifact(scope domain.Scope, step, artifact string) (*domain.Checkpoint, error) {
	if artifact == "" {
		return cs.Append(scope, step, "", "")
	}
	data, err := cs.io.ReadFile(artifact)
	if err != nil {
		return nil, fmt.Errorf("digest artifact %s: %w", artifact, err)
	}
	sum := sha256.Sum256(data)
	return cs.Append(scope, step, artifact, "sha256:"+hex.EncodeToString(sum[:]))
}

// AppendArtifacts generates a combined fingerprint for several official artifacts of the same step.
// Artifact keeps the first main artifact's path; a change in any related artifact produces a new checkpoint.
func (cs *CheckpointStore) AppendArtifacts(scope domain.Scope, step string, artifacts ...string) (*domain.Checkpoint, error) {
	if len(artifacts) == 0 {
		return cs.Append(scope, step, "", "")
	}
	h := sha256.New()
	for _, artifact := range artifacts {
		data, err := cs.io.ReadFile(artifact)
		if err != nil {
			return nil, fmt.Errorf("digest artifact %s: %w", artifact, err)
		}
		_, _ = h.Write([]byte(artifact))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return cs.Append(scope, step, artifacts[0], "sha256:"+hex.EncodeToString(h.Sum(nil)))
}

// Latest returns the latest checkpoint for the given scope.
func (cs *CheckpointStore) Latest(scope domain.Scope) *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	for i := len(cs.cache) - 1; i >= 0; i-- {
		if cs.cache[i].Scope.Matches(scope) {
			cp := cs.cache[i]
			return &cp
		}
	}
	return nil
}

// LatestByStep returns the latest checkpoint for the given scope + step.
func (cs *CheckpointStore) LatestByStep(scope domain.Scope, step string) *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	for i := len(cs.cache) - 1; i >= 0; i-- {
		cp := cs.cache[i]
		if cp.Scope.Matches(scope) && cp.Step == step {
			return &cp
		}
	}
	return nil
}

// LatestGlobal returns the globally latest checkpoint (ignoring scope).
func (cs *CheckpointStore) LatestGlobal() *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	if len(cs.cache) == 0 {
		return nil
	}
	cp := cs.cache[len(cs.cache)-1]
	return &cp
}

// All returns a copy of the whole checkpoint list (ascending seq).
func (cs *CheckpointStore) All() []domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	if len(cs.cache) == 0 {
		return nil
	}
	out := make([]domain.Checkpoint, len(cs.cache))
	copy(out, cs.cache)
	return out
}

// Reset clears the checkpoint file and cache. It is used only when creating a novel.
// The file is deleted before memory is cleared: on a failed delete cache and seqGen are kept, so memory and disk cannot
// fall out of step.
func (cs *CheckpointStore) Reset() error {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	if err := cs.io.RemoveFileUnlocked(checkpointsFile); err != nil {
		return err
	}
	cs.seqGen.Store(0)
	cs.cache = nil
	cs.loadErr = nil
	return nil
}

// InitError returns the error from loading the checkpoint mirror at construction. Store.Init must check it first, so a
// corrupt jsonl is not interpreted as "no checkpoints".
func (cs *CheckpointStore) InitError() error {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	return cs.loadErr
}

// readCheckpointsFile parses jsonl strictly; a truncated tail is also a persistence error the user needs to see.
func readCheckpointsFile(path string) ([]domain.Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var result []domain.Checkpoint
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var cp domain.Checkpoint
		if err := json.Unmarshal(raw, &cp); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", checkpointsFile, lineNo, err)
		}
		result = append(result, cp)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", checkpointsFile, err)
	}
	return result, nil
}
