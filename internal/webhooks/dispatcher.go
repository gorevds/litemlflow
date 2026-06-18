// Package webhooks implements async webhook delivery for run-status events.
//
// Architecture:
//
//	API handlers call Dispatcher.Notify(event, run) which enqueues a job onto
//	a bounded channel (capacity 1024). If the channel is full the job is dropped
//	and a counter is incremented (backpressure: log+drop). A fixed worker pool
//	(max 8 goroutines) drains the queue and makes HTTP POST requests. Failed
//	deliveries are retried with exponential backoff (1s → 5s → 25s). After 3
//	attempts the last HTTP status is recorded and the delivery is abandoned.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// Event name constants.
const (
	EventRunStarted  = "run_started"
	EventRunFinished = "run_finished"
	EventRunFailed   = "run_failed"
	EventRunKilled   = "run_killed"
)

// StatusToEvent maps a run status to a webhook event name. Returns empty
// string if the status should not trigger a webhook.
func StatusToEvent(status string) string {
	switch status {
	case model.StatusRunning:
		return EventRunStarted
	case model.StatusFinished:
		return EventRunFinished
	case model.StatusFailed:
		return EventRunFailed
	case model.StatusKilled:
		return EventRunKilled
	default:
		return ""
	}
}

// WebhookLookup is the store interface Dispatcher needs.
type WebhookLookup interface {
	ListWebhooks(ctx context.Context, workspaceID string, expID *int64) ([]*model.Webhook, error)
	RecordWebhookAttempt(ctx context.Context, id int64, status int, attempt int64) error
}

// job is a pending webhook delivery.
type job struct {
	wh    *model.Webhook
	event string
	run   *model.Run
}

// Payload is the JSON body POSTed to the webhook URL.
type Payload struct {
	Event     string     `json:"event"`
	Run       *model.Run `json:"run"`
	Timestamp int64      `json:"timestamp"` // unix ms
}

// Dispatcher manages async webhook delivery.
type Dispatcher struct {
	store     WebhookLookup
	queue     chan job
	client    *http.Client
	logger    *slog.Logger
	retryBase time.Duration
	echo      *EchoLog
	dropped   atomic.Int64 // count of dropped jobs due to backpressure

	// Drain coordination — Stop() closes stopCh and waits for stopped.
	stopCh   chan struct{} // closed by Stop() to tell workers to drain
	stopped  chan struct{} // closed once workers have all exited
	workerWG sync.WaitGroup
	once     sync.Once
}

const (
	queueCapacity   = 1024
	maxWorkers      = 8
	maxRetries      = 3
	retryBase       = time.Second
	retryMultiplier = 5 // 1s, 5s, 25s
)

// Options allows customizing Dispatcher behavior (primarily for tests).
type Options struct {
	// RetryBase overrides the first retry delay (default 1s).
	RetryBase time.Duration
	// HTTPClient overrides the default 10s-timeout client.
	HTTPClient *http.Client
	// Echo is the in-process echo ring buffer. If non-nil, deliveries to
	// lmf://echo URLs are routed here instead of dispatched as HTTP.
	Echo *EchoLog
}

// New creates a Dispatcher and starts the worker pool. Call Stop to drain.
func New(ctx context.Context, store WebhookLookup, logger *slog.Logger) *Dispatcher {
	return NewWithOptions(ctx, store, logger, Options{})
}

// NewWithOptions creates a Dispatcher with custom options.
func NewWithOptions(ctx context.Context, store WebhookLookup, logger *slog.Logger, opts Options) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base := opts.RetryBase
	if base == 0 {
		base = retryBase
	}
	d := &Dispatcher{
		store:     store,
		queue:     make(chan job, queueCapacity),
		client:    client,
		logger:    logger,
		retryBase: base,
		echo:      opts.Echo,
		stopCh:    make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	for i := 0; i < maxWorkers; i++ {
		d.workerWG.Add(1)
		go d.worker(ctx)
	}
	// Once all workers have returned (queue drained or stop fired), close
	// stopped so Stop() can return.
	go func() {
		d.workerWG.Wait()
		close(d.stopped)
	}()
	return d
}

// Stop tells workers to drain the queue (up to drainTimeout) then exit.
// Subsequent Notify() calls are dropped (counted via DroppedCount).
//
// Returns when all workers have exited or drainTimeout elapses, whichever
// is first. After workers exit we sweep any remaining queue entries
// synchronously — this catches the small window where a Notify lost the
// stopCh race and slipped a job in just as workers were exiting.
//
// Safe to call multiple times.
func (d *Dispatcher) Stop(drainTimeout time.Duration) {
	d.once.Do(func() {
		close(d.stopCh)
	})
	select {
	case <-d.stopped:
	case <-time.After(drainTimeout):
		d.logger.Warn("webhooks: Stop timeout — workers still running, abandoning queue",
			slog.Duration("timeout", drainTimeout),
			slog.Int("queued", len(d.queue)),
		)
		return
	}
	// Final synchronous sweep — workers are gone, but a Notify may have
	// landed a job during the gap between worker-exit and stopped-close.
	// We use a fresh background context bounded by 5s; longer than that
	// means a misconfigured webhook target.
	sweepCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case j := <-d.queue:
			d.deliver(sweepCtx, j)
		default:
			return
		}
	}
}

// Notify enqueues webhook deliveries for all matching webhooks.
// It resolves webhooks for the given run's experiment and workspace, then
// enqueues one job per webhook that subscribes to the event. If the queue is
// full a warning is logged and the job is dropped (backpressure design).
//
// Once Stop() has been called, subsequent Notify() calls drop their work
// (counted via DroppedCount): the dispatcher is closed and the caller can't
// expect delivery anyway.
//
// Race window (post-T1 review fix): Notify is called from request handlers
// while Stop runs concurrently from server shutdown. We close the residual
// "stopCh-closed-and-queue-still-has-space" race by re-checking stopCh after
// the send-or-drop fast path. A vanishingly small window remains where Stop
// fires between the second stopCh check and the worker draining; in that
// case the job is enqueued but workers may have already exited. A
// post-Stop drain in Stop() itself catches anything that lands during
// drainTimeout. Documented residual: jobs Notified strictly after the
// drain deadline are silently dropped (counted in `dropped`).
func (d *Dispatcher) Notify(ctx context.Context, event string, run *model.Run) {
	if event == "" || run == nil {
		return
	}
	select {
	case <-d.stopCh:
		// Already stopped — drop the whole batch.
		return
	default:
	}
	expID := run.ExperimentID
	whs, err := d.store.ListWebhooks(ctx, "", &expID)
	if err != nil {
		d.logger.Warn("webhooks: list failed", slog.String("err", err.Error()))
		return
	}
	for _, wh := range whs {
		if !wh.Enabled {
			continue
		}
		if !matchesEvent(wh.Events, event) {
			continue
		}
		j := job{wh: wh, event: event, run: run}
		// Stage 1: check stopCh first so that a Stop racing with Notify
		// drops the job rather than enqueueing into a dying queue.
		select {
		case <-d.stopCh:
			d.dropped.Add(1)
			return
		default:
		}
		// Stage 2: send or drop on full queue.
		select {
		case d.queue <- j:
		default:
			d.dropped.Add(1)
			d.logger.Warn("webhooks: queue full, dropping delivery",
				slog.Int64("webhook_id", wh.ID),
				slog.String("event", event),
				slog.Int64("dropped_total", d.dropped.Load()),
			)
		}
	}
}

// DroppedCount returns the cumulative number of dropped deliveries.
func (d *Dispatcher) DroppedCount() int64 { return d.dropped.Load() }

func (d *Dispatcher) worker(ctx context.Context) {
	defer d.workerWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			// Drain remaining queue items best-effort, then exit. The
			// stopCh-vs-queue race is fine: if Stop() fires while a job
			// is sitting in the queue we still try to deliver it. The
			// outer Stop() bounds total drain time via drainTimeout.
			for {
				select {
				case j := <-d.queue:
					d.deliver(ctx, j)
				default:
					return
				}
			}
		case j := <-d.queue:
			d.deliver(ctx, j)
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, j job) {
	payload := Payload{
		Event:     j.event,
		Run:       j.run,
		Timestamp: time.Now().UnixMilli(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		d.logger.Error("webhooks: marshal payload failed", slog.String("err", err.Error()))
		return
	}

	// Echo short-circuit: write to in-process ring buffer, no HTTP.
	if IsEchoURL(j.wh.URL) {
		if d.echo != nil {
			runID := ""
			if j.run != nil {
				runID = j.run.ID
			}
			d.echo.Record(EchoEntry{
				Event:     j.event,
				WebhookID: j.wh.ID,
				Body:      string(body),
				RunID:     runID,
			})
		}
		_ = d.store.RecordWebhookAttempt(ctx, j.wh.ID, http.StatusOK, time.Now().UnixMilli())
		return
	}

	backoff := d.retryBase
	var lastStatus int
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				// Stop() initiated: abort further retries promptly instead of
				// sleeping through the backoff, so the worker exits within
				// Stop's drainTimeout rather than leaking (independent-review).
				return
			case <-time.After(backoff):
			}
			backoff *= retryMultiplier
		}

		status, err := d.post(j.wh, body)
		lastStatus = status
		if err == nil && status >= 200 && status < 300 {
			// Success.
			_ = d.store.RecordWebhookAttempt(ctx, j.wh.ID, status, time.Now().UnixMilli())
			return
		}
		if err != nil {
			d.logger.Warn("webhooks: delivery failed",
				slog.Int64("webhook_id", j.wh.ID),
				slog.Int("attempt", attempt+1),
				slog.String("err", err.Error()),
			)
		} else {
			d.logger.Warn("webhooks: delivery failed",
				slog.Int64("webhook_id", j.wh.ID),
				slog.Int("attempt", attempt+1),
				slog.Int("status", status),
			)
		}
	}
	// Record final failure.
	_ = d.store.RecordWebhookAttempt(ctx, j.wh.ID, lastStatus, time.Now().UnixMilli())
}

func (d *Dispatcher) post(wh *model.Webhook, body []byte) (int, error) {
	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Parse event name back from body for the header.
	var p Payload
	_ = json.Unmarshal(body, &p)
	req.Header.Set("X-LiteMLflow-Event", p.Event)

	if wh.Secret != "" {
		sig := hmacSHA256(wh.Secret, body)
		req.Header.Set("X-LiteMLflow-Signature", "sha256="+sig)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func hmacSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature returns true if the X-LiteMLflow-Signature header matches
// the HMAC-SHA256 of body with secret. Exported for test use.
func VerifySignature(secret string, body []byte, header string) bool {
	expected := "sha256=" + hmacSHA256(secret, body)
	// constant-time comparison
	if len(header) != len(expected) {
		return false
	}
	a := []byte(header)
	b := []byte(expected)
	result := 0
	for i := range a {
		result |= int(a[i] ^ b[i])
	}
	return result == 0
}

func matchesEvent(events, event string) bool {
	if events == "" {
		return false
	}
	for _, e := range strings.Split(events, ",") {
		if strings.TrimSpace(e) == event {
			return true
		}
	}
	return false
}
