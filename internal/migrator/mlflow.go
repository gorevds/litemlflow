// Package migrator provides the MLflow → LiteMLflow migration importer.
//
// It calls the MLflow REST API (source) and writes directly into a LiteMLflow
// store (target), with no LiteMLflow HTTP server involved.  All writes are
// idempotent: interrupted imports can be resumed by re-running the command.
package migrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorevds/litemlflow/internal/artifact"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// Filter controls which lifecycle stages to import.
type Filter int

const (
	// FilterActive imports only active experiments/runs (default).
	FilterActive Filter = iota
	// FilterDeleted imports only deleted experiments/runs.
	FilterDeleted
	// FilterAll imports all lifecycle stages.
	FilterAll
)

// Stats accumulates import counters.
type Stats struct {
	Experiments int
	Runs        int
	Metrics     int
	Params      int
	Tags        int
	Artifacts   int
	// Skipped counts runs the importer chose not to (re-)import: either
	// they were already present in the target store (idempotent re-run) or
	// the import of that single run failed and we logged-and-continued.
	Skipped int
	Elapsed time.Duration
}

// MLflowImporter copies experiments, runs, metrics, params, tags, and
// artifacts from a running MLflow tracking server into a LiteMLflow store.
type MLflowImporter struct {
	// SourceURL is the base URL of the MLflow tracking server, e.g. "http://localhost:5000".
	SourceURL string
	// Workspace is the LiteMLflow workspace to import into (default "default").
	Workspace string
	// DryRun enumerates but does not write.
	DryRun bool
	// Include controls which lifecycle stages are imported.
	Include Filter
	// HTTP is the client used to call the MLflow API.  Defaults to http.DefaultClient.
	HTTP *http.Client
	// Store is the destination LiteMLflow store.
	Store store.Store
	// ArtifactStore is the destination artifact backend.
	ArtifactStore artifact.Store
	// OnProgress is called periodically with a human-readable stage label and
	// cumulative entity count.  May be nil.
	OnProgress func(stage string, n int)

	// checkpointPath is populated from the data-dir argument to Run.
	checkpointPath string
	// imported is the set of run IDs already written (loaded from checkpoint).
	imported map[string]bool
}

// checkpoint is persisted to <data>/.import-state.json.
type checkpoint struct {
	ImportedRunIDs []string `json:"imported_run_ids"`
}

// Run executes the full import and returns aggregate statistics.
func (m *MLflowImporter) Run(ctx context.Context) (Stats, error) {
	start := time.Now()

	if m.HTTP == nil {
		m.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if m.Workspace == "" {
		m.Workspace = "default"
	}

	// Probe the MLflow server and get its version.
	version, err := m.probeMLflow(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("connecting to MLflow at %s: %w", m.SourceURL, err)
	}
	m.progress("connect", 0)
	fmt.Printf("[import] connecting to %s ... ok (mlflow %s)\n", m.SourceURL, version)

	// Load checkpoint (idempotent resume).
	if err := m.loadCheckpoint(); err != nil {
		return Stats{}, fmt.Errorf("load checkpoint: %w", err)
	}

	// Enumerate all experiments.
	experiments, err := m.listExperiments(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("list experiments: %w", err)
	}

	// Count total runs for the summary line.
	totalRuns := 0
	runsByExp := make(map[string][]mlflowRun, len(experiments))
	for _, exp := range experiments {
		runs, err := m.listRuns(ctx, exp.ExperimentID)
		if err != nil {
			return Stats{}, fmt.Errorf("list runs for experiment %s: %w", exp.ExperimentID, err)
		}
		runsByExp[exp.ExperimentID] = runs
		totalRuns += len(runs)
	}
	fmt.Printf("[import] enumerated %d experiments, %d runs\n", len(experiments), totalRuns)

	var stats Stats
	stats.Experiments = 0

	for i, exp := range experiments {
		expStart := time.Now()
		runs := runsByExp[exp.ExperimentID]
		fmt.Printf("[import] importing exp %d/%d: %q ... %d runs ...",
			i+1, len(experiments), exp.Name, len(runs))

		localExpID, err := m.importExperiment(ctx, exp)
		if err != nil {
			return stats, fmt.Errorf("import experiment %q: %w", exp.Name, err)
		}
		stats.Experiments++

		for _, run := range runs {
			if m.imported[run.Info.RunID] {
				// Already imported in a previous run; count toward stats but skip work.
				stats.Runs++
				continue
			}
			// Per-run idempotency: a parallel-running second importer might
			// race the checkpoint file and double-import. Defend by checking
			// the target store directly. ErrNotFound (= not yet imported) is
			// the only path that proceeds; any other error from the lookup is
			// surfaced.
			if !m.DryRun {
				if _, err := m.Store.GetRun(ctx, run.Info.RunID); err == nil {
					// Already in target store — treat as imported, advance.
					stats.Runs++
					m.imported[run.Info.RunID] = true
					continue
				} else if !errors.Is(err, store.ErrNotFound) {
					fmt.Fprintf(os.Stderr, "[import] warn: lookup run %s: %v (skipping)\n", run.Info.RunID, err)
					stats.Skipped++
					continue
				}
			}
			rs, err := m.importRun(ctx, localExpID, run)
			if err != nil {
				// Skip-with-log: a single failed run must not abort the whole
				// import. Operators can re-run with the checkpoint intact and
				// only the failed runs will be re-tried.
				fmt.Fprintf(os.Stderr, "[import] error: import run %s: %v (skipping)\n", run.Info.RunID, err)
				stats.Skipped++
				continue
			}
			stats.Runs += rs.Runs
			stats.Metrics += rs.Metrics
			stats.Params += rs.Params
			stats.Tags += rs.Tags
			stats.Artifacts += rs.Artifacts

			if !m.DryRun {
				m.imported[run.Info.RunID] = true
				if err := m.saveCheckpoint(); err != nil {
					// Non-fatal: checkpoint is best-effort.
					fmt.Fprintf(os.Stderr, "[import] warning: checkpoint save failed: %v\n", err)
				}
			}

			// Progress report every 50 entities.
			total := stats.Runs + stats.Metrics + stats.Params + stats.Tags
			if total%50 == 0 && total > 0 {
				m.progress("importing", total)
			}
		}

		elapsed := time.Since(expStart)
		fmt.Printf(" done in %.1fs\n", elapsed.Seconds())
	}

	stats.Elapsed = time.Since(start)
	fmt.Printf("[import] complete: %d experiments, %d runs, %d metrics, %d params, %d tags, %d artifacts in %.1fs\n",
		stats.Experiments, stats.Runs, stats.Metrics, stats.Params, stats.Tags, stats.Artifacts,
		stats.Elapsed.Seconds())

	return stats, nil
}

// SetCheckpointDir configures the directory where the checkpoint file is stored.
// Must be called before Run when a data directory is available.
func (m *MLflowImporter) SetCheckpointDir(dataDir string) {
	m.checkpointPath = filepath.Join(dataDir, ".import-state.json")
}

// --- MLflow JSON shapes ---

type mlflowExperiment struct {
	ExperimentID   string     `json:"experiment_id"`
	Name           string     `json:"name"`
	LifecycleStage string     `json:"lifecycle_stage"`
	Tags           []mlflowKV `json:"tags"`
}

type mlflowKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type mlflowRunInfo struct {
	RunID          string `json:"run_id"`
	ExperimentID   string `json:"experiment_id"`
	RunName        string `json:"run_name"`
	Status         string `json:"status"`
	StartTime      int64  `json:"start_time"`
	EndTime        int64  `json:"end_time"`
	LifecycleStage string `json:"lifecycle_stage"`
	UserID         string `json:"user_id"`
	ArtifactURI    string `json:"artifact_uri"`
}

type mlflowRunData struct {
	Metrics []mlflowMetric `json:"metrics"`
	Params  []mlflowKV     `json:"params"`
	Tags    []mlflowKV     `json:"tags"`
}

type mlflowRun struct {
	Info mlflowRunInfo `json:"info"`
	Data mlflowRunData `json:"data"`
}

type mlflowMetric struct {
	Key       string  `json:"key"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	Step      int64   `json:"step"`
}

type mlflowArtifact struct {
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	FileSize int64  `json:"file_size"`
}

// --- MLflow API helpers ---

func (m *MLflowImporter) probeMLflow(ctx context.Context) (string, error) {
	// MLflow exposes its version at /version or we can infer from server info.
	// Try /api/2.0/mlflow/experiments/search with max_results=1 to confirm reachability.
	resp, err := m.get(ctx, "/api/2.0/mlflow/experiments/search?max_results=1")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}
	// Try to get the MLflow version from the server header.
	version := resp.Header.Get("X-Mlflow-Version")
	if version == "" {
		version = "unknown"
	}
	return version, nil
}

func (m *MLflowImporter) listExperiments(ctx context.Context) ([]mlflowExperiment, error) {
	var all []mlflowExperiment
	pageToken := ""
	for {
		u := "/api/2.0/mlflow/experiments/search?max_results=1000"
		if pageToken != "" {
			u += "&page_token=" + url.QueryEscape(pageToken)
		}
		// Include lifecycle filter based on m.Include.
		switch m.Include {
		case FilterDeleted:
			u += "&view_type=DELETED_ONLY"
		case FilterAll:
			u += "&view_type=ALL"
		default:
			u += "&view_type=ACTIVE_ONLY"
		}
		resp, err := m.get(ctx, u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var body struct {
			Experiments   []mlflowExperiment `json:"experiments"`
			NextPageToken string             `json:"next_page_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, fmt.Errorf("decode experiments: %w", err)
		}
		all = append(all, body.Experiments...)
		if body.NextPageToken == "" {
			break
		}
		pageToken = body.NextPageToken
	}
	return all, nil
}

func (m *MLflowImporter) listRuns(ctx context.Context, experimentID string) ([]mlflowRun, error) {
	var all []mlflowRun
	pageToken := ""
	for {
		payload := map[string]any{
			"experiment_ids": []string{experimentID},
			"max_results":    1000,
		}
		switch m.Include {
		case FilterDeleted:
			payload["run_view_type"] = "DELETED_ONLY"
		case FilterAll:
			payload["run_view_type"] = "ALL"
		default:
			payload["run_view_type"] = "ACTIVE_ONLY"
		}
		if pageToken != "" {
			payload["page_token"] = pageToken
		}
		var body struct {
			Runs          []mlflowRun `json:"runs"`
			NextPageToken string      `json:"next_page_token"`
		}
		if err := m.post(ctx, "/api/2.0/mlflow/runs/search", payload, &body); err != nil {
			return nil, err
		}
		all = append(all, body.Runs...)
		if body.NextPageToken == "" {
			break
		}
		pageToken = body.NextPageToken
	}
	return all, nil
}

func (m *MLflowImporter) getMetricHistory(ctx context.Context, runID, key string) ([]mlflowMetric, error) {
	var all []mlflowMetric
	pageToken := ""
	for {
		u := fmt.Sprintf("/api/2.0/mlflow/metrics/get-history?run_id=%s&metric_key=%s&max_results=5000",
			url.QueryEscape(runID), url.QueryEscape(key))
		if pageToken != "" {
			u += "&page_token=" + url.QueryEscape(pageToken)
		}
		resp, err := m.get(ctx, u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var body struct {
			Metrics       []mlflowMetric `json:"metrics"`
			NextPageToken string         `json:"next_page_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, fmt.Errorf("decode metric history for %s/%s: %w", runID, key, err)
		}
		all = append(all, body.Metrics...)
		if body.NextPageToken == "" {
			break
		}
		pageToken = body.NextPageToken
	}
	return all, nil
}

func (m *MLflowImporter) listArtifacts(ctx context.Context, runID, path string) ([]mlflowArtifact, error) {
	u := fmt.Sprintf("/api/2.0/mlflow/artifacts/list?run_id=%s", url.QueryEscape(runID))
	if path != "" {
		u += "&path=" + url.QueryEscape(path)
	}
	resp, err := m.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Files []mlflowArtifact `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode artifact list: %w", err)
	}
	return body.Files, nil
}

// listArtifactsRecursive returns all leaf files under the run's artifact root.
func (m *MLflowImporter) listArtifactsRecursive(ctx context.Context, runID string) ([]mlflowArtifact, error) {
	entries, err := m.listArtifacts(ctx, runID, "")
	if err != nil {
		return nil, err
	}
	var files []mlflowArtifact
	for _, e := range entries {
		if e.IsDir {
			sub, err := m.listArtifactsRecursive2(ctx, runID, e.Path)
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		} else {
			files = append(files, e)
		}
	}
	return files, nil
}

func (m *MLflowImporter) listArtifactsRecursive2(ctx context.Context, runID, dir string) ([]mlflowArtifact, error) {
	entries, err := m.listArtifacts(ctx, runID, dir)
	if err != nil {
		return nil, err
	}
	var files []mlflowArtifact
	for _, e := range entries {
		if e.IsDir {
			sub, err := m.listArtifactsRecursive2(ctx, runID, e.Path)
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		} else {
			files = append(files, e)
		}
	}
	return files, nil
}

func (m *MLflowImporter) downloadArtifact(ctx context.Context, runID, path string) (io.ReadCloser, error) {
	// MLflow 2.x: GET /api/2.0/mlflow-artifacts/artifacts/<run_id>/<path>
	u := fmt.Sprintf("/api/2.0/mlflow-artifacts/artifacts/%s/%s",
		url.PathEscape(runID), path)
	resp, err := m.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("download artifact %s: status %d: %s", path, resp.StatusCode, body)
	}
	return resp.Body, nil
}

// --- import helpers ---

// importExperiment creates the target experiment and returns its local ID.
// If the name already exists in the workspace, it is renamed with an
// "-imported-<ts>" suffix to avoid collision (rather than reusing the
// existing experiment, which could mix unrelated data).
func (m *MLflowImporter) importExperiment(ctx context.Context, exp mlflowExperiment) (int64, error) {
	if m.DryRun {
		return 0, nil
	}

	lifecycle := model.LifecycleActive
	if exp.LifecycleStage == "deleted" {
		lifecycle = model.LifecycleDeleted
	}

	e := &model.Experiment{
		Name:           exp.Name,
		LifecycleStage: lifecycle,
		WorkspaceID:    m.Workspace,
	}

	id, err := m.Store.CreateExperiment(ctx, e)
	if errors.Is(err, store.ErrAlreadyExists) {
		// Name collision: create a disambiguated copy rather than silently
		// merging runs into an unrelated existing experiment.
		ts := time.Now().UnixMilli()
		e.Name = fmt.Sprintf("%s-imported-%d", exp.Name, ts)
		id, err = m.Store.CreateExperiment(ctx, e)
		if err != nil {
			return 0, fmt.Errorf("create experiment %q (renamed): %w", exp.Name, err)
		}
		fmt.Printf("[import] note: experiment %q already exists; importing as %q\n", exp.Name, e.Name)
	}
	if err != nil {
		return 0, err
	}

	// Import experiment tags.
	for _, tag := range exp.Tags {
		if tagErr := m.Store.SetExperimentTag(ctx, id, tag.Key, tag.Value); tagErr != nil {
			fmt.Fprintf(os.Stderr, "[import] warning: experiment tag %q: %v\n", tag.Key, tagErr)
		}
	}

	return id, nil
}

// importRun copies a single run into the target store and returns partial stats.
func (m *MLflowImporter) importRun(ctx context.Context, localExpID int64, run mlflowRun) (Stats, error) {
	var stats Stats
	stats.Runs = 1

	info := run.Info

	if m.DryRun {
		stats.Params = len(run.Data.Params)
		stats.Tags = len(run.Data.Tags)
		// Can't count metrics without fetching history; approximate from run.Data.Metrics.
		stats.Metrics = len(run.Data.Metrics)
		return stats, nil
	}

	// Map MLflow status to our constants.
	status := mapStatus(info.Status)
	lifecycle := model.LifecycleActive
	if info.LifecycleStage == "deleted" {
		lifecycle = model.LifecycleDeleted
	}

	r := &model.Run{
		ID:             info.RunID,
		ExperimentID:   localExpID,
		Name:           info.RunName,
		Status:         status,
		StartTime:      info.StartTime,
		LifecycleStage: lifecycle,
		UserID:         info.UserID,
		Kind:           model.KindClassic,
	}
	if info.EndTime > 0 {
		et := info.EndTime
		r.EndTime = &et
	}

	if err := m.Store.CreateRun(ctx, r); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
		return stats, fmt.Errorf("create run: %w", err)
	}

	// Params.
	for _, p := range run.Data.Params {
		if err := m.Store.LogParam(ctx, info.RunID, model.Param{Key: p.Key, Value: p.Value}); err != nil {
			if !errors.Is(err, store.ErrAlreadyExists) {
				fmt.Fprintf(os.Stderr, "[import] warning: param %q on run %s: %v\n", p.Key, info.RunID, err)
			}
		} else {
			stats.Params++
		}
	}

	// Tags.
	for _, t := range run.Data.Tags {
		if err := m.Store.SetTag(ctx, info.RunID, model.KV{Key: t.Key, Value: t.Value}); err != nil {
			fmt.Fprintf(os.Stderr, "[import] warning: tag %q on run %s: %v\n", t.Key, info.RunID, err)
		} else {
			stats.Tags++
		}
	}

	// Metric history — fetch full history for each key.
	metricKeys := collectMetricKeys(run.Data.Metrics)
	for _, key := range metricKeys {
		history, err := m.getMetricHistory(ctx, info.RunID, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[import] warning: metric history %q on run %s: %v\n", key, info.RunID, err)
			continue
		}
		batch := make([]model.Metric, 0, len(history))
		for _, h := range history {
			batch = append(batch, model.Metric{
				Key:       h.Key,
				Value:     h.Value,
				Timestamp: h.Timestamp,
				Step:      h.Step,
			})
		}
		if err := m.Store.LogMetrics(ctx, info.RunID, batch); err != nil {
			fmt.Fprintf(os.Stderr, "[import] warning: log metrics %q on run %s: %v\n", key, info.RunID, err)
		} else {
			stats.Metrics += len(batch)
		}
	}

	// Artifacts.
	artFiles, err := m.listArtifactsRecursive(ctx, info.RunID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[import] warning: list artifacts for run %s: %v\n", info.RunID, err)
	} else {
		for _, af := range artFiles {
			if err := m.importArtifact(ctx, info.RunID, af.Path); err != nil {
				fmt.Fprintf(os.Stderr, "[import] warning: artifact %q on run %s: %v\n", af.Path, info.RunID, err)
			} else {
				stats.Artifacts++
			}
		}
	}

	// Update run status / end_time now that all data is written.
	if err := m.Store.UpdateRun(ctx, info.RunID, &status, r.EndTime, nil); err != nil && !errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "[import] warning: update run %s: %v\n", info.RunID, err)
	}

	return stats, nil
}

func (m *MLflowImporter) importArtifact(ctx context.Context, runID, path string) error {
	if m.ArtifactStore == nil {
		return nil
	}
	rc, err := m.downloadArtifact(ctx, runID, path)
	if err != nil {
		return err
	}
	defer rc.Close()
	return m.ArtifactStore.Upload(runID, path, rc, 0)
}

// --- checkpoint helpers ---

func (m *MLflowImporter) loadCheckpoint() error {
	m.imported = make(map[string]bool)
	if m.checkpointPath == "" {
		return nil
	}
	data, err := os.ReadFile(m.checkpointPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cp checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		// Corrupt checkpoint — start fresh.
		fmt.Fprintf(os.Stderr, "[import] warning: checkpoint corrupt, starting fresh: %v\n", err)
		return nil
	}
	for _, id := range cp.ImportedRunIDs {
		m.imported[id] = true
	}
	if len(m.imported) > 0 {
		fmt.Printf("[import] resuming: %d runs already imported\n", len(m.imported))
	}
	return nil
}

func (m *MLflowImporter) saveCheckpoint() error {
	if m.checkpointPath == "" {
		return nil
	}
	ids := make([]string, 0, len(m.imported))
	for id := range m.imported {
		ids = append(ids, id)
	}
	cp := checkpoint{ImportedRunIDs: ids}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.checkpointPath + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.checkpointPath)
}

// --- HTTP helpers ---

func (m *MLflowImporter) get(ctx context.Context, path string) (*http.Response, error) {
	rawURL := strings.TrimRight(m.SourceURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}
	return resp, nil
}

func (m *MLflowImporter) post(ctx context.Context, path string, payload any, dst any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	rawURL := strings.TrimRight(m.SourceURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, body)
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}

// --- misc helpers ---

func (m *MLflowImporter) progress(stage string, n int) {
	if m.OnProgress != nil {
		m.OnProgress(stage, n)
	}
}

// mapStatus converts an MLflow run status string to a LiteMLflow constant.
func mapStatus(s string) string {
	switch strings.ToUpper(s) {
	case "RUNNING":
		return model.StatusRunning
	case "FINISHED":
		return model.StatusFinished
	case "FAILED":
		return model.StatusFailed
	case "KILLED":
		return model.StatusKilled
	case "SCHEDULED":
		return model.StatusScheduled
	default:
		return model.StatusFinished
	}
}

// collectMetricKeys returns the unique metric keys referenced in a run's
// metric summary list (from the run's Data field).
func collectMetricKeys(metrics []mlflowMetric) []string {
	seen := make(map[string]struct{}, len(metrics))
	var out []string
	for _, m := range metrics {
		if _, ok := seen[m.Key]; !ok {
			seen[m.Key] = struct{}{}
			out = append(out, m.Key)
		}
	}
	return out
}
