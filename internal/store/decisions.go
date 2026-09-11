package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// DecisionStore audits runtime LLM semantic adjudications (meta/decisions.jsonl, append-only).
//
// Position (docs/engine-arbiter.md §4.3): the data source for audit and offline replay — recording "what facts were
// seen and what was adjudicated", for eval regression and A/B comparison of future Arbiters. It is **not** event
// sourcing and **not** a recovery data source (recovery depends only on the fact layer: Progress/Checkpoint/RunMeta and
// the like).
type DecisionStore struct{ io *IO }

func NewDecisionStore(io *IO) *DecisionStore { return &DecisionStore{io: io} }

const (
	decisionSchemaVersion = 1
	decisionsFile         = "meta/decisions.jsonl"
	// maxDecisionInputBytes is the per-entry input cap; over it the input is truncated and flagged, so a long paste cannot burst the audit file.
	maxDecisionInputBytes = 8 << 10
)

// DecisionRecord is the audit record of one semantic adjudication. facts holds structured facts and references only and
// never copies prose. input stays inside the record (offline replay needs it); redaction happens at the diag export
// boundary, not at persistence time.
type DecisionRecord struct {
	SchemaVersion  int             `json:"schema_version"`
	ID             string          `json:"id"`
	At             string          `json:"at"`
	Kind           string          `json:"kind"`    // intervention | plan_start | volume_end | ...
	Decider        string          `json:"decider"` // arbiter | architect (volume-end review)
	CheckpointSeq  int64           `json:"checkpoint_seq,omitempty"`
	Input          string          `json:"input,omitempty"`
	InputTruncated bool            `json:"input_truncated,omitempty"`
	Facts          json.RawMessage `json:"facts,omitempty"`
	Decision       json.RawMessage `json:"decision,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Error          string          `json:"error,omitempty"` // Error text when a decision fails — a failure is an audit fact too; without it, debugging is guesswork
	Model          string          `json:"model,omitempty"`
	DurationMs     int64           `json:"duration_ms,omitempty"`
}

// Append persists one adjudication record; SchemaVersion/At/ID are filled in by this method and an over-limit input is
// truncated. It returns the completed record (the ID lets the caller correlate it, as in PlanStartRecord.DecisionID).
func (s *DecisionStore) Append(rec DecisionRecord) (DecisionRecord, error) {
	rec.SchemaVersion = decisionSchemaVersion
	if rec.At == "" {
		rec.At = time.Now().Format(time.RFC3339)
	}
	if rec.ID == "" {
		rec.ID = newDecisionID()
	}
	if len(rec.Input) > maxDecisionInputBytes {
		rec.Input = rec.Input[:maxDecisionInputBytes]
		rec.InputTruncated = true
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return rec, fmt.Errorf("marshal decision: %w", err)
	}
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	// The previous append may have crashed before the newline was written. The provably-uncommitted tail is deleted first,
	// so new JSON is never concatenated onto a broken line; a complete newline-terminated record is never modified
	// automatically.
	if _, err := s.committedDataUnlocked(); err != nil {
		return rec, fmt.Errorf("repair decision tail: %w", err)
	}
	if err := s.io.AppendLineUnlocked(decisionsFile, append(data, '\n')); err != nil {
		return rec, err
	}
	return rec, nil
}

// Recent returns the last n records (old to new); a missing file returns empty.
//
// A committed corrupt line must return an explicit error — the Arbiter cannot keep adjudicating on a fact pack missing
// part of its history. A tail fragment interrupted by a crash (last byte not '\n') is truncated by
// committedDataUnlocked with an explicit warning; this is not speculative repair, since this file's protocol states that
// only newline-terminated records count as committed.
func (s *DecisionStore) Recent(n int) ([]DecisionRecord, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	data, err := s.committedDataUnlocked()
	if err != nil {
		return nil, err
	}
	all, err := parseDecisionRecords(data)
	if err != nil {
		return nil, err
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// committedDataUnlocked returns complete newline-terminated records and truncates the leftover bytes after the last
// newline from disk. The caller must hold io.mu's write lock. Truncation is idempotent; on failure the original file is
// kept and the error is raised explicitly.
func (s *DecisionStore) committedDataUnlocked() ([]byte, error) {
	data, err := s.io.ReadFileUnlocked(decisionsFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data, nil
	}
	keep := bytes.LastIndexByte(data, '\n') + 1
	if err := os.Truncate(s.io.path(decisionsFile), int64(keep)); err != nil {
		return nil, err
	}
	slog.Warn("đã sửa phần đuôi chưa commit của audit phán định",
		"module", "store", "file", decisionsFile, "discarded_bytes", len(data)-keep)
	return data[:keep], nil
}

func parseDecisionRecords(data []byte) ([]DecisionRecord, error) {
	var all []DecisionRecord
	lines := bytes.Split(data, []byte{'\n'})
	for i, raw := range lines {
		if i == len(lines)-1 && len(raw) == 0 {
			break
		}
		var rec DecisionRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", decisionsFile, i+1, err)
		}
		all = append(all, rec)
	}
	return all, nil
}

func newDecisionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("dec-%d", time.Now().UnixNano())
	}
	return "dec-" + hex.EncodeToString(b[:])
}
