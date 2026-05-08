// Package model defines the domain types for LiteMLflow.
//
// These types are the canonical representation of experiments, runs,
// metrics, and related entities. The store layer translates them to and
// from SQLite rows. The API layers (mlflow compat, native) translate them
// to and from JSON.
package model

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// LifecycleStage values.
const (
	LifecycleActive  = "active"
	LifecycleDeleted = "deleted"
)

// RunStatus values mirror MLflow's enum.
const (
	StatusRunning   = "RUNNING"
	StatusScheduled = "SCHEDULED"
	StatusFinished  = "FINISHED"
	StatusFailed    = "FAILED"
	StatusKilled    = "KILLED"
)

// RunKind discriminates classic ML runs from LLM-style trace runs and evals.
const (
	KindClassic = "classic"
	KindTrace   = "trace"
	KindEval    = "eval"
)

// Experiment represents an MLflow experiment.
type Experiment struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	ArtifactLocation string `json:"artifact_location"`
	LifecycleStage   string `json:"lifecycle_stage"`
	CreationTime     int64  `json:"creation_time"`
	LastUpdateTime   int64  `json:"last_update_time"`
	Tags             []KV   `json:"tags,omitempty"`
}

// Run represents a single tracked run.
type Run struct {
	ID             string `json:"id"`
	ExperimentID   int64  `json:"experiment_id"`
	Name           string `json:"name,omitempty"`
	Status         string `json:"status"`
	StartTime      int64  `json:"start_time"`
	EndTime        *int64 `json:"end_time,omitempty"`
	ArtifactURI    string `json:"artifact_uri"`
	LifecycleStage string `json:"lifecycle_stage"`
	UserID         string `json:"user_id,omitempty"`
	SourceType     string `json:"source_type,omitempty"`
	SourceName     string `json:"source_name,omitempty"`
	Kind           string `json:"kind"`
}

// Metric is a single metric observation.
type Metric struct {
	Key       string  `json:"key"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	Step      int64   `json:"step"`
}

// Param is an immutable run parameter.
type Param struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// KV is a generic key/value tag.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Span represents one node in a trace tree.
type Span struct {
	ID             string `json:"id"`
	TraceID        string `json:"trace_id"`
	ParentID       string `json:"parent_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Name           string `json:"name"`
	Kind           string `json:"kind,omitempty"`
	StartTimeNS    int64  `json:"start_time_ns"`
	EndTimeNS      *int64 `json:"end_time_ns,omitempty"`
	AttributesJSON string `json:"-"`
	EventsJSON     string `json:"-"`
	StatusCode     string `json:"status_code,omitempty"`
	StatusMessage  string `json:"status_message,omitempty"`
}

// Prompt is a versioned prompt template.
type Prompt struct {
	Name        string `json:"name"`
	Version     int64  `json:"version"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	CreatedAt   int64  `json:"created_at"`
	CreatedBy   string `json:"created_by,omitempty"`
	Description string `json:"description,omitempty"`
}

// Eval is the structured payload of a run with kind=eval.
type Eval struct {
	RunID        string   `json:"run_id"`
	TargetRunIDs []string `json:"target_run_ids"`
	DatasetRef   string   `json:"dataset_ref,omitempty"`
	Score        *float64 `json:"score,omitempty"`
	MetricsJSON  string   `json:"-"`
}

// ValidName enforces a relaxed identifier rule used for keys, names, etc.
// Empty is rejected; control characters are rejected; length is bounded.
func ValidName(s string, maxLen int) error {
	if s == "" {
		return errors.New("name cannot be empty")
	}
	if maxLen > 0 && len(s) > maxLen {
		return fmt.Errorf("name exceeds %d bytes", maxLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("name contains control characters")
		}
	}
	return nil
}

// ValidKey is the same as ValidName but with a stricter prefix rule
// (no leading/trailing whitespace).
func ValidKey(s string) error {
	if err := ValidName(s, 250); err != nil {
		return err
	}
	if strings.TrimSpace(s) != s {
		return errors.New("key has leading or trailing whitespace")
	}
	return nil
}

// NewRunID returns a 32-hex-character ID compatible with MLflow's run IDs.
//
// MLflow uses UUID4 hex without dashes, 32 chars. We do the same so its
// clients accept our IDs without complaint. The randomness source is
// crypto/rand seeded by time.Now to avoid collisions even with bursts.
func NewRunID() string {
	b := make([]byte, 16)
	t := time.Now().UnixNano()
	// Mix time into the buffer first; final bytes overwritten by rand below.
	for i := 0; i < 8; i++ {
		b[i] = byte(t >> (8 * i))
	}
	if err := readRand(b[8:]); err != nil {
		// Fall back to clock; collisions still rare given the time prefix.
		extra := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[8+i] = byte(extra >> (8 * i))
		}
	}
	// RFC 4122 v4 marker (best-effort; clients generally don't validate).
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

// NewSpanID returns a 16-hex span id.
func NewSpanID() string {
	b := make([]byte, 8)
	if err := readRand(b); err != nil {
		t := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(t >> (8 * i))
		}
	}
	return hex.EncodeToString(b)
}

// Session represents an authenticated session stored in the DB.
// The ID is a 32-byte random hex string used as the cookie value.
type Session struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	UserEmail  string `json:"user_email,omitempty"`
	UserName   string `json:"user_name,omitempty"`
	AuthMethod string `json:"auth_method"` // "basic" | "oidc"
	CreatedAt  int64  `json:"created_at"`  // unix ms
	ExpiresAt  int64  `json:"expires_at"`  // unix ms
	LastSeen   int64  `json:"last_seen"`   // unix ms
}

// NewTraceID returns a 32-hex trace id.
func NewTraceID() string {
	b := make([]byte, 16)
	if err := readRand(b); err != nil {
		t := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			b[i] = byte(t >> (8 * (i % 8)))
		}
	}
	return hex.EncodeToString(b)
}

// ---- Model Registry ---------------------------------------------------------

// Stage constants for model version lifecycle.
const (
	StageNone       = "None"
	StageStaging    = "Staging"
	StageProduction = "Production"
	StageArchived   = "Archived"
)

// ValidStage returns true when s is one of the four legal stage values.
func ValidStage(s string) bool {
	switch s {
	case StageNone, StageStaging, StageProduction, StageArchived:
		return true
	}
	return false
}

// RegisteredModel is a named model in the registry.
type RegisteredModel struct {
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	CreationTime   int64  `json:"creation_time"`
	LastUpdateTime int64  `json:"last_update_time"`
	Tags           []KV   `json:"tags,omitempty"`
	// LatestVersions is populated on demand (GetLatestModelVersions).
	LatestVersions []*ModelVersion `json:"latest_versions,omitempty"`
}

// ModelVersion is one versioned snapshot of a registered model.
type ModelVersion struct {
	Name           string `json:"name"`
	Version        int64  `json:"version"`
	Description    string `json:"description,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	CurrentStage   string `json:"current_stage"`
	Source         string `json:"source"`
	RunID          string `json:"run_id,omitempty"`
	Status         string `json:"status"`
	StatusMessage  string `json:"status_message,omitempty"`
	CreationTime   int64  `json:"creation_time"`
	LastUpdateTime int64  `json:"last_update_time"`
	Tags           []KV   `json:"tags,omitempty"`
}

// ModelTag is a key/value tag scoped to a registered model or model version.
// Alias for KV for clarity in registry context; KV is used internally.
type ModelTag = KV
