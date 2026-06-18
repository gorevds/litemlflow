// Package store: analytics query engine.
//
// AnalyticsQuery is a small typed DSL that compiles to safe parameterised
// SQL against the metrics_latest materialised view + runs / params / tags
// tables. NO part of the user-supplied input is concatenated into SQL —
// every dynamic value is bound with ?, every "shape" choice (aggregation,
// group-by family, order direction) is dispatched through a switch over an
// allowlist of constants. This is deliberately not a SQL passthrough: the
// goal is to expose enough power for "best metric per dimension over a
// time window" without re-inventing safe SQL parsing.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// AnalyticsQuery is the input shape for POST /api/v1/analytics/query.
type AnalyticsQuery struct {
	// Metric is the metric key to aggregate. Required.
	Metric string `json:"metric"`

	// Agg is one of: "max", "min", "avg", "last". Required.
	Agg string `json:"agg"`

	// GroupBy is one of: "" (no grouping → single aggregate row),
	// "experiment_id", "status",
	// or "params.<key>", "tags.<key>" — the value of the matching params/tags
	// row. Suffix is parameterised.
	GroupBy string `json:"group_by"`

	// Where bundles all filters.
	Where AnalyticsWhere `json:"where"`

	// OrderBy: "value_desc" (default), "value_asc", "count_desc", "group_asc".
	OrderBy string `json:"order_by"`

	// Limit is capped at 1000 (default 100).
	Limit int `json:"limit"`

	// WorkspaceID scopes results to one workspace; empty = "default".
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// AnalyticsWhere is the filter half of an AnalyticsQuery.
type AnalyticsWhere struct {
	// ExperimentIDs limits the set of experiments scanned. Empty = all in
	// the workspace.
	ExperimentIDs []int64 `json:"experiment_ids,omitempty"`

	// TimeAfter is a unix-ms threshold; runs with start_time < this are
	// excluded. Zero = no lower bound.
	TimeAfter int64 `json:"time_after,omitempty"`

	// Lifecycle is "active" (default), "deleted", or "all".
	Lifecycle string `json:"lifecycle,omitempty"`

	// Status filters by run status (empty = all). Examples: "FINISHED",
	// "FAILED", "RUNNING".
	Status []string `json:"status,omitempty"`

	// MinMetric / MaxMetric clamp the metric value range (e.g. only runs
	// with eval/f1 in [0.7, 1.0]).
	MinMetric *float64 `json:"min_metric,omitempty"`
	MaxMetric *float64 `json:"max_metric,omitempty"`
}

// AnalyticsRow is one row of the result.
type AnalyticsRow struct {
	Group       string  `json:"group,omitempty"`       // value of group-by dimension; empty when no grouping or value is NULL/missing
	AggValue    float64 `json:"agg_value"`             // aggregated metric value
	RunCount    int64   `json:"run_count"`             // distinct runs in this bucket
	BestRunID   string  `json:"best_run_id,omitempty"` // the run id whose metric == AggValue (max/min only)
	BestRunName string  `json:"best_run_name,omitempty"`
	BestExpID   int64   `json:"best_experiment_id,omitempty"`

	// groupIsNull is true when the SQL row had NULL in the group_key column
	// (i.e. the run had no matching params/tags row, or no group_by was used).
	// We track it separately from `Group=""` so that resolveBestRuns can
	// distinguish "param value is empty string" (Group="", groupIsNull=false)
	// from "param row missing" (Group="", groupIsNull=true). Not exposed in
	// JSON because the consumer can derive it from the absence of `group`.
	groupIsNull bool
}

// AnalyticsResult is what the API returns.
type AnalyticsResult struct {
	Rows             []AnalyticsRow `json:"rows"`
	TotalRunsScanned int64          `json:"total_runs_scanned"`
	ExecutionMS      int64          `json:"execution_ms"`
	SQL              string         `json:"sql,omitempty"` // dev only; populated when the env var LITEMLFLOW_DEBUG_ANALYTICS=1
}

// allowedAggs / allowedOrderBy / allowedLifecycle are the safe enum sets
// dispatched by the SQL builder. Anything outside fails Validate().
//
// "last" was deliberately removed in v1.1: implementing it correctly requires
// picking the row with max (timestamp, step) per group rather than max value,
// which is a different SQL shape. The compromise during the v1.1 review was
// to drop the alias rather than ship a misleading agg. Will return in v1.2
// as a separate code path with explicit "latest_by_timestamp" semantics.
var allowedAggs = map[string]string{
	"max": "MAX(ml.value)",
	"min": "MIN(ml.value)",
	"avg": "AVG(ml.value)",
}

var allowedLifecycle = map[string]string{
	"":        "r.lifecycle_stage = 'active'",
	"active":  "r.lifecycle_stage = 'active'",
	"deleted": "r.lifecycle_stage = 'deleted'",
	"all":     "1=1",
}

var allowedStatuses = map[string]bool{
	"RUNNING":   true,
	"FINISHED":  true,
	"FAILED":    true,
	"KILLED":    true,
	"SCHEDULED": true,
}

// Validate enforces the shape constraints. Returns nil on success.
func (q *AnalyticsQuery) Validate() error {
	if strings.TrimSpace(q.Metric) == "" {
		return fmt.Errorf("metric is required")
	}
	if len(q.Metric) > 250 {
		return fmt.Errorf("metric name too long")
	}
	if _, ok := allowedAggs[q.Agg]; !ok {
		return fmt.Errorf("agg must be one of max, min, avg")
	}
	if q.GroupBy != "" && q.GroupBy != "experiment_id" && q.GroupBy != "status" {
		// Must be params.<key> or tags.<key>
		switch {
		case strings.HasPrefix(q.GroupBy, "params."):
			if len(q.GroupBy) <= len("params.") || len(q.GroupBy) > 250+len("params.") {
				return fmt.Errorf("group_by params.<key> has invalid key")
			}
		case strings.HasPrefix(q.GroupBy, "tags."):
			if len(q.GroupBy) <= len("tags.") || len(q.GroupBy) > 250+len("tags.") {
				return fmt.Errorf("group_by tags.<key> has invalid key")
			}
		default:
			return fmt.Errorf("group_by must be empty, experiment_id, status, params.<key>, or tags.<key>")
		}
	}
	if _, ok := allowedLifecycle[q.Where.Lifecycle]; !ok {
		return fmt.Errorf("where.lifecycle must be empty, active, deleted, or all")
	}
	for _, s := range q.Where.Status {
		if !allowedStatuses[s] {
			return fmt.Errorf("where.status contains invalid value %q", s)
		}
	}
	switch q.OrderBy {
	case "", "value_desc", "value_asc", "count_desc", "group_asc":
		// ok
	default:
		return fmt.Errorf("order_by must be empty, value_desc, value_asc, count_desc, or group_asc")
	}
	if q.Limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	if len(q.Where.ExperimentIDs) > 1000 {
		return fmt.Errorf("where.experiment_ids cannot exceed 1000 entries")
	}
	if len(q.Where.Status) > 16 {
		return fmt.Errorf("where.status cannot exceed 16 entries")
	}
	return nil
}

// AnalyticsQuery executes the query against the materialised view and
// associated tables. Always read-only.
//
// Execution strategy:
//   - max/min  →  one GROUP BY scan to get aggregate + run_count per group,
//     then ONE indexed lookup per group to resolve a representative
//     run id. Total cost: O(N) + O(K) where N is the filtered
//     metric count and K is the result groups (capped at q.Limit).
//   - avg/last →  single GROUP BY scan; best-run columns stay NULL.
//
// The single-pass-with-window-functions approach was tried and measured
// 2-3x slower than this two-step approach because SQLite materialises the
// full partition in temp B-tree memory.
func (s *SQLiteStore) AnalyticsQuery(ctx context.Context, q AnalyticsQuery) (*AnalyticsResult, error) {
	if err := q.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	if q.WorkspaceID == "" {
		q.WorkspaceID = "default"
	}

	sqlText, args := buildAnalyticsSQL(q)
	start := time.Now()

	// Optional plan dump for ad-hoc tuning. Cheap when disabled.
	if dbg := os.Getenv("LITEMLFLOW_DEBUG_ANALYTICS"); dbg == "1" || dbg == "stderr" {
		planRows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+sqlText, args...)
		if err == nil {
			fmt.Fprintf(os.Stderr, "analytics plan:\n")
			for planRows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := planRows.Scan(&id, &parent, &notUsed, &detail); err == nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n", id, parent, detail)
				}
			}
			planRows.Close()
		}
		fmt.Fprintf(os.Stderr, "analytics sql:\n%s\n", sqlText)
	}

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		// Don't leak the SQL into the API-visible error. The dev override
		// emits it to stderr above when LITEMLFLOW_DEBUG_ANALYTICS=1.
		return nil, fmt.Errorf("analytics query: %w", err)
	}
	defer rows.Close()

	res := &AnalyticsResult{Rows: []AnalyticsRow{}}
	for rows.Next() {
		var r AnalyticsRow
		var grp sql.NullString
		var aggVal sql.NullFloat64
		if err := rows.Scan(&grp, &aggVal, &r.RunCount); err != nil {
			return nil, err
		}
		if !aggVal.Valid {
			continue
		}
		r.AggValue = aggVal.Float64
		// Distinguish NULL group (no params/tags row) from empty-string group
		// so resolveBestRuns can pick the right SQL clause.
		if grp.Valid {
			r.Group = grp.String
		} else {
			r.groupIsNull = true
		}
		res.TotalRunsScanned += r.RunCount
		res.Rows = append(res.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// For max/min, resolve a representative best run per group via one
	// index lookup each. This avoids materialising the full partition in
	// memory (the alternative window-function approach was measured 2-3x
	// slower on the 100k-run benchmark).
	if (q.Agg == "max" || q.Agg == "min") && len(res.Rows) > 0 {
		if err := s.resolveBestRuns(ctx, q, res.Rows); err != nil {
			return nil, fmt.Errorf("resolve best runs: %w", err)
		}
	}

	res.ExecutionMS = time.Since(start).Milliseconds()
	return res, nil
}

// resolveBestRuns fills in BestRunID/BestRunName/BestExpID for max/min queries.
// One indexed lookup per group via the (key, value) composite index.
func (s *SQLiteStore) resolveBestRuns(ctx context.Context, q AnalyticsQuery, rows []AnalyticsRow) error {
	// Build the per-group lookup. The where clause re-applies the user
	// filters so the chosen run actually satisfies the original query.
	for i := range rows {
		row := &rows[i]
		var sb strings.Builder
		args := []any{}
		sb.WriteString("SELECT ml.run_id, COALESCE(r.name,''), r.experiment_id\n")
		sb.WriteString("FROM metrics_latest ml\n")
		sb.WriteString("JOIN runs r ON r.id = ml.run_id\n")
		sb.WriteString("JOIN experiments e ON e.id = r.experiment_id\n")

		// Group filter joins (params/tags) so the picked run belongs to
		// the right group bucket.
		switch {
		case strings.HasPrefix(q.GroupBy, "params."):
			key := strings.TrimPrefix(q.GroupBy, "params.")
			sb.WriteString("LEFT JOIN params p ON p.run_id = r.id AND p.key = ?\n")
			args = append(args, key)
		case strings.HasPrefix(q.GroupBy, "tags."):
			key := strings.TrimPrefix(q.GroupBy, "tags.")
			sb.WriteString("LEFT JOIN tags t ON t.run_id = r.id AND t.key = ?\n")
			args = append(args, key)
		}

		sb.WriteString("WHERE ml.key = ? AND ml.value = ?\n")
		args = append(args, q.Metric, row.AggValue)
		sb.WriteString("  AND e.workspace_id = ?\n")
		args = append(args, q.WorkspaceID)
		sb.WriteString("  AND ")
		sb.WriteString(allowedLifecycle[q.Where.Lifecycle])
		sb.WriteString("\n")
		if len(q.Where.ExperimentIDs) > 0 {
			sb.WriteString("  AND r.experiment_id IN (")
			for j, id := range q.Where.ExperimentIDs {
				if j > 0 {
					sb.WriteString(",")
				}
				sb.WriteString("?")
				args = append(args, id)
			}
			sb.WriteString(")\n")
		}
		if q.Where.TimeAfter > 0 {
			sb.WriteString("  AND r.start_time >= ?\n")
			args = append(args, q.Where.TimeAfter)
		}
		if len(q.Where.Status) > 0 {
			sb.WriteString("  AND r.status IN (")
			for j, st := range q.Where.Status {
				if j > 0 {
					sb.WriteString(",")
				}
				sb.WriteString("?")
				args = append(args, st)
			}
			sb.WriteString(")\n")
		}
		switch {
		case q.GroupBy == "experiment_id":
			// Bind as INTEGER (the SELECT casts to TEXT only for return-side
			// uniformity). A non-numeric Group means no rows in any case.
			id, perr := strconv.ParseInt(row.Group, 10, 64)
			if perr != nil {
				continue
			}
			sb.WriteString("  AND r.experiment_id = ?\n")
			args = append(args, id)
		case q.GroupBy == "status":
			sb.WriteString("  AND r.status = ?\n")
			args = append(args, row.Group)
		case strings.HasPrefix(q.GroupBy, "params."):
			if row.groupIsNull {
				sb.WriteString("  AND p.value IS NULL\n")
			} else {
				sb.WriteString("  AND p.value = ?\n")
				args = append(args, row.Group)
			}
		case strings.HasPrefix(q.GroupBy, "tags."):
			if row.groupIsNull {
				sb.WriteString("  AND t.value IS NULL\n")
			} else {
				sb.WriteString("  AND t.value = ?\n")
				args = append(args, row.Group)
			}
		}
		sb.WriteString("LIMIT 1")

		qrow := s.db.QueryRowContext(ctx, sb.String(), args...)
		var runID, runName sql.NullString
		var expID sql.NullInt64
		if err := qrow.Scan(&runID, &runName, &expID); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return err
		}
		if runID.Valid {
			row.BestRunID = runID.String
		}
		if runName.Valid {
			row.BestRunName = runName.String
		}
		if expID.Valid {
			row.BestExpID = expID.Int64
		}
	}
	return nil
}

// buildAnalyticsSQL compiles q into a parameterised SELECT.
//
// Strategy:
//   - For "max" / "min" we want the aggregate AND a representative run id /
//     name / experiment_id per group. Doing three correlated subqueries
//     re-evaluates the same join+filter; instead we use ROW_NUMBER() over
//     the partition to pick the top-1 row, plus COUNT() OVER for the per-
//     group cardinality. Single scan of metrics_latest.
//   - For "avg" / "last" the best-run columns stay NULL — we just GROUP BY
//     the group expression, since no single row carries the aggregate.
//
// Every dynamic table/column choice is via a switch over an allowlist; every
// value is bound via ?. There is no path from user input to raw SQL.
func buildAnalyticsSQL(q AnalyticsQuery) (string, []any) {
	args := []any{}

	groupExpr := "NULL"
	groupJoin := ""
	groupGroupBy := ""

	switch {
	case q.GroupBy == "":
	case q.GroupBy == "experiment_id":
		groupExpr = "CAST(r.experiment_id AS TEXT)"
		groupGroupBy = "r.experiment_id"
	case q.GroupBy == "status":
		groupExpr = "r.status"
		groupGroupBy = "r.status"
	case strings.HasPrefix(q.GroupBy, "params."):
		key := strings.TrimPrefix(q.GroupBy, "params.")
		groupExpr = "p_grp.value"
		groupJoin = "LEFT JOIN params p_grp ON p_grp.run_id = r.id AND p_grp.key = ?"
		groupGroupBy = "p_grp.value"
		args = append(args, key)
	case strings.HasPrefix(q.GroupBy, "tags."):
		key := strings.TrimPrefix(q.GroupBy, "tags.")
		groupExpr = "t_grp.value"
		groupJoin = "LEFT JOIN tags t_grp ON t_grp.run_id = r.id AND t_grp.key = ?"
		groupGroupBy = "t_grp.value"
		args = append(args, key)
	}

	// Common WHERE+JOIN fragment we reuse in both branches.
	var where strings.Builder
	whereArgs := []any{}
	where.WriteString("  WHERE ml.key = ?\n")
	whereArgs = append(whereArgs, q.Metric)
	where.WriteString("    AND e.workspace_id = ?\n")
	whereArgs = append(whereArgs, q.WorkspaceID)
	where.WriteString("    AND ")
	where.WriteString(allowedLifecycle[q.Where.Lifecycle])
	where.WriteString("\n")
	if len(q.Where.ExperimentIDs) > 0 {
		where.WriteString("    AND r.experiment_id IN (")
		for i, id := range q.Where.ExperimentIDs {
			if i > 0 {
				where.WriteString(",")
			}
			where.WriteString("?")
			whereArgs = append(whereArgs, id)
		}
		where.WriteString(")\n")
	}
	if q.Where.TimeAfter > 0 {
		where.WriteString("    AND r.start_time >= ?\n")
		whereArgs = append(whereArgs, q.Where.TimeAfter)
	}
	if len(q.Where.Status) > 0 {
		where.WriteString("    AND r.status IN (")
		for i, st := range q.Where.Status {
			if i > 0 {
				where.WriteString(",")
			}
			where.WriteString("?")
			whereArgs = append(whereArgs, st)
		}
		where.WriteString(")\n")
	}
	if q.Where.MinMetric != nil {
		where.WriteString("    AND ml.value >= ?\n")
		whereArgs = append(whereArgs, *q.Where.MinMetric)
	}
	if q.Where.MaxMetric != nil {
		where.WriteString("    AND ml.value <= ?\n")
		whereArgs = append(whereArgs, *q.Where.MaxMetric)
	}

	aggExpr := allowedAggs[q.Agg]

	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(groupExpr)
	sb.WriteString(" AS group_key, ")
	sb.WriteString(aggExpr)
	// (run_id, key) is the PRIMARY KEY of metrics_latest, so COUNT(*) is
	// the same as COUNT(DISTINCT run_id) once the key filter is applied —
	// and ~3x cheaper because COUNT(DISTINCT) materialises a set.
	sb.WriteString(" AS agg_value, COUNT(*) AS run_count\n")
	sb.WriteString("FROM metrics_latest ml\n")
	sb.WriteString("JOIN runs r ON r.id = ml.run_id\n")
	sb.WriteString("JOIN experiments e ON e.id = r.experiment_id\n")
	if groupJoin != "" {
		sb.WriteString(groupJoin)
		sb.WriteString("\n")
	}
	sb.WriteString(where.String())
	args = append(args, whereArgs...)
	if groupGroupBy != "" {
		sb.WriteString("GROUP BY ")
		sb.WriteString(groupGroupBy)
		sb.WriteString("\n")
	}

	switch q.OrderBy {
	case "", "value_desc":
		sb.WriteString("ORDER BY agg_value DESC\n")
	case "value_asc":
		sb.WriteString("ORDER BY agg_value ASC\n")
	case "count_desc":
		sb.WriteString("ORDER BY run_count DESC, agg_value DESC\n")
	case "group_asc":
		sb.WriteString("ORDER BY group_key ASC\n")
	}
	sb.WriteString("LIMIT ?\n")
	args = append(args, q.Limit)

	return sb.String(), args
}
