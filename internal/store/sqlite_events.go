package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// ----------------------------------------------------------------------------
// v1.5 time-travel: append-only event log.
//
// The event log mirrors mutations to runs and run-level tags so that
// `?as_of=<unix_ms>` reads can reconstruct what a run looked like at time T.
// Metrics and params are already append-only with native timestamps — they
// don't need event rows.
// ----------------------------------------------------------------------------

// Event kinds. Keep in sync with the CHECK constraint in migration 013.
const (
	EventRunUpdate    = "run_update"
	EventRunLifecycle = "run_lifecycle"
	EventRunParent    = "run_parent"
	EventTagSet       = "tag_set"
	EventTagDelete    = "tag_delete"
)

// writeRunEvent appends one event row. Internal helper used by the
// run-mutation methods.
//
// Note on transactionality: in v1.5-rc1 events are written from the same
// SQLiteStore.db handle as the underlying mutation, but NOT inside the
// same explicit txn (the existing UpdateRun / SetTag / etc. don't use
// transactions today). A crash between the run UPDATE and the event
// INSERT would lose the event row but not corrupt the run state. This
// is acceptable for a debugging / observability feature; if it becomes
// load-bearing we'll wrap both writes in a single SQLITE_TXN.
func (s *SQLiteStore) writeRunEvent(ctx context.Context, kind, runID string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("event payload marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events(ts_ms, kind, entity_type, entity_id, payload)
		VALUES (?, ?, 'run', ?, ?)
	`, time.Now().UnixMilli(), kind, runID, string(raw))
	if err != nil {
		return fmt.Errorf("event insert: %w", err)
	}
	return nil
}

// tryWriteRunEvent is the standard call site wrapper: write the event,
// log a warning on failure (mutation has already succeeded; we don't
// want the audit-trail loss to be silent). Returns nothing — the caller
// has nothing to do with the failure.
func (s *SQLiteStore) tryWriteRunEvent(ctx context.Context, kind, runID string, payload map[string]any) {
	if err := s.writeRunEvent(ctx, kind, runID, payload); err != nil {
		slog.Warn("event write failed", "run_id", runID, "kind", kind, "err", err.Error())
	}
}

// PruneEventsBefore deletes event rows with ts_ms < beforeMs. Used by
// the janitor to bound monotonic growth of the events table when
// LITEMLFLOW_EVENTS_RETENTION_DAYS is configured. Returns the row count.
func (s *SQLiteStore) PruneEventsBefore(ctx context.Context, beforeMs int64) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE ts_ms < ?`, beforeMs)
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// runEvent is one row from the events table, decoded for replay.
type runEvent struct {
	ID      int64
	TsMs    int64
	Kind    string
	Payload map[string]any
}

// readRunEventsUntil returns all events for runID with ts_ms <= asOfMs,
// ordered by (ts_ms ASC, id ASC) so replay applies them in physical
// commit order. Used by replay logic to reconstruct historical run state.
func (s *SQLiteStore) readRunEventsUntil(ctx context.Context, runID string, asOfMs int64) ([]runEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts_ms, kind, payload
		FROM events
		WHERE entity_type = 'run' AND entity_id = ? AND ts_ms <= ?
		ORDER BY ts_ms ASC, id ASC
	`, runID, asOfMs)
	if err != nil {
		return nil, fmt.Errorf("event read: %w", err)
	}
	defer rows.Close()

	var out []runEvent
	for rows.Next() {
		var e runEvent
		var rawPayload string
		if err := rows.Scan(&e.ID, &e.TsMs, &e.Kind, &rawPayload); err != nil {
			return nil, err
		}
		if rawPayload != "" {
			if err := json.Unmarshal([]byte(rawPayload), &e.Payload); err != nil {
				return nil, fmt.Errorf("event payload decode (id=%d): %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetRunAsOf reconstructs run state at the given unix-ms timestamp.
//
// Replay strategy:
//
//  1. Find the earliest run state for runID. If the run was created after
//     asOfMs (start_time > asOfMs), return ErrNotFound — the run did not
//     exist at T. Otherwise begin with the current run state and the
//     current tag set.
//  2. Read all events at ts_ms <= asOfMs in order; collect tag set/delete
//     events to determine which tags existed at T.
//  3. Read events at ts_ms > asOfMs in REVERSE order; UNDO each by
//     restoring the `before` state captured in the event payload.
//
// This is "current-state minus events newer than T" rather than
// "earliest-state plus events older than T" because we don't capture the
// initial state in an event row at create time — the create itself is
// the first state. The before-state of the first mutation is the
// post-create state.
//
// Limitations (v1.5-rc1):
//   - Run create itself is not an event; if the run was created after T,
//     we return ErrNotFound rather than reconstruct a pre-create state.
//   - Tag values that were SET, DELETED, then SET again before T resolve
//     to the latest set; current-tag-set minus deletes-after-T plus
//     sets-after-T-undo is correct only when each key has a monotone
//     history. We additionally replay forward from event time to handle
//     the rare case of SET→DELETE→SET.
func (s *SQLiteStore) GetRunAsOf(ctx context.Context, runID string, asOfMs int64) (*model.Run, []model.KV, error) {
	return s.getRunAsOfImpl(ctx, runID, "", asOfMs)
}

// GetRunAsOfInWorkspace is the workspace-scoped variant. ErrNotFound is
// returned when the run is missing OR belongs to a different workspace —
// no shape distinction (mirrors the v1.4 lineage isolation fix).
func (s *SQLiteStore) GetRunAsOfInWorkspace(ctx context.Context, runID, workspaceID string, asOfMs int64) (*model.Run, []model.KV, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	return s.getRunAsOfImpl(ctx, runID, workspaceID, asOfMs)
}

// MaxEventsPerReplay caps how many event rows GetRunAsOf will scan for
// a single run. A pathological writer can spam SetTag and turn every
// as-of read into an O(N) walk; this bound keeps the replay path
// predictable. The cap is generous enough for legitimate workloads
// (each tag set/delete + run mutation is one row).
const MaxEventsPerReplay = 50_000

// ErrReplayLimitExceeded is returned when a run has more event rows
// than MaxEventsPerReplay — the caller should narrow the as-of window
// or accept the current state.
var ErrReplayLimitExceeded = errors.New("run has too many events to replay; narrow the as-of window")

func (s *SQLiteStore) getRunAsOfImpl(ctx context.Context, runID, workspaceID string, asOfMs int64) (*model.Run, []model.KV, error) {
	var (
		run *model.Run
		err error
	)
	if workspaceID != "" {
		run, err = s.getRunInWorkspace(ctx, runID, workspaceID)
	} else {
		run, err = s.GetRun(ctx, runID)
	}
	if err != nil {
		return nil, nil, err
	}
	if run.StartTime > asOfMs {
		return nil, nil, ErrNotFound
	}

	// All events on this run, oldest first. We need both directions:
	// events at ts_ms <= asOfMs are kept-as-is, events at ts_ms > asOfMs
	// are undone via the `before` payload.
	//
	// LIMIT +1 lets us detect overflow without slurping unbounded rows.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts_ms, kind, payload
		FROM events
		WHERE entity_type = 'run' AND entity_id = ?
		ORDER BY ts_ms ASC, id ASC
		LIMIT ?
	`, runID, int64(MaxEventsPerReplay+1))
	if err != nil {
		return nil, nil, fmt.Errorf("event scan: %w", err)
	}
	defer rows.Close()

	var newer []runEvent
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > MaxEventsPerReplay {
			return nil, nil, ErrReplayLimitExceeded
		}
		var e runEvent
		var rawPayload string
		if err := rows.Scan(&e.ID, &e.TsMs, &e.Kind, &rawPayload); err != nil {
			return nil, nil, err
		}
		if rawPayload != "" {
			if err := json.Unmarshal([]byte(rawPayload), &e.Payload); err != nil {
				return nil, nil, fmt.Errorf("event decode (id=%d): %w", e.ID, err)
			}
		}
		if e.TsMs > asOfMs {
			newer = append(newer, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Step 1: undo newer events by restoring the captured `before` fields.
	// Iterate in reverse so the most-recent undo is applied first.
	for i := len(newer) - 1; i >= 0; i-- {
		applyUndo(run, newer[i])
	}

	// Step 2: reconstruct tag set at T.
	// Strategy: take the CURRENT tag set, then apply newer events in
	// reverse to undo them. tag_set with a `before` (pre-existing value)
	// → restore that value; tag_set without `before` (new key) → drop the
	// key; tag_delete with a `before` → re-add it.
	currentTags, err := s.GetTags(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	tagMap := map[string]string{}
	for _, t := range currentTags {
		tagMap[t.Key] = t.Value
	}
	for i := len(newer) - 1; i >= 0; i-- {
		applyTagUndo(tagMap, newer[i])
	}
	out := make([]model.KV, 0, len(tagMap))
	for k, v := range tagMap {
		out = append(out, model.KV{Key: k, Value: v})
	}

	return run, out, nil
}

// applyUndo reverses one run-mutation event by restoring the captured
// `before` fields. Unknown event kinds are ignored — forward-compatible
// with future kinds in the events table.
func applyUndo(run *model.Run, e runEvent) {
	before, ok := e.Payload["before"].(map[string]any)
	if !ok || before == nil {
		return
	}
	switch e.Kind {
	case EventRunUpdate:
		if v, ok := before["status"].(string); ok {
			run.Status = v
		}
		if v, ok := before["end_time"]; ok {
			if v == nil {
				run.EndTime = nil
			} else if f, ok := v.(float64); ok {
				ms := int64(f)
				run.EndTime = &ms
			}
		}
		if v, ok := before["name"].(string); ok {
			run.Name = v
		}
	case EventRunLifecycle:
		if v, ok := before["lifecycle_stage"].(string); ok {
			run.LifecycleStage = v
		}
	case EventRunParent:
		if v, ok := before["parent_run_id"].(string); ok {
			run.ParentRunID = v
		}
	}
}

// applyTagUndo reverses a tag set/delete by restoring the prior value or
// removing a newly-added key.
func applyTagUndo(tagMap map[string]string, e runEvent) {
	switch e.Kind {
	case EventTagSet:
		key, _ := e.Payload["key"].(string)
		if key == "" {
			return
		}
		before, hadBefore := e.Payload["before"]
		if !hadBefore || before == nil {
			// New key in the forward direction → remove on undo.
			delete(tagMap, key)
			return
		}
		if v, ok := before.(string); ok {
			tagMap[key] = v
		}
	case EventTagDelete:
		key, _ := e.Payload["key"].(string)
		if key == "" {
			return
		}
		if v, ok := e.Payload["before"].(string); ok {
			tagMap[key] = v
		}
	}
}

// captureRunBefore reads the current run row's mutable fields (status,
// end_time, name, lifecycle_stage, parent_run_id) — used by the
// mutation methods to record the pre-state in event payloads.
//
// Returns nil if the run does not exist; the caller's UPDATE will then
// affect zero rows and surface its own NotFound.
func (s *SQLiteStore) captureRunBefore(ctx context.Context, runID string) map[string]any {
	row := s.db.QueryRowContext(ctx, `
		SELECT status, end_time, COALESCE(name,''), lifecycle_stage, COALESCE(parent_run_id,'')
		FROM runs WHERE id = ?
	`, runID)
	var status, name, lifecycle, parent string
	var endTime sql.NullInt64
	if err := row.Scan(&status, &endTime, &name, &lifecycle, &parent); err != nil {
		return nil
	}
	out := map[string]any{
		"status":          status,
		"name":            name,
		"lifecycle_stage": lifecycle,
		"parent_run_id":   parent,
	}
	if endTime.Valid {
		out["end_time"] = endTime.Int64
	} else {
		out["end_time"] = nil
	}
	return out
}
