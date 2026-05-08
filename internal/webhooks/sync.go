package webhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// SyncDelivery performs a single synchronous webhook delivery.
// Used by the /test endpoint to validate a webhook configuration immediately.
type SyncDelivery struct {
	// Client is the HTTP client to use; defaults to a 10s timeout client.
	Client *http.Client
	// Echo is the in-process echo ring buffer. If non-nil and the webhook URL
	// uses the lmf:// scheme, the delivery is recorded here instead of HTTP.
	Echo *EchoLog
}

// Deliver POSTs a synthetic payload to the webhook URL and returns the HTTP
// status code. On network error the returned status is 0.
func (s *SyncDelivery) Deliver(wh *model.Webhook, event string, run *model.Run) (int, error) {
	payload := Payload{
		Event:     event,
		Run:       run,
		Timestamp: time.Now().UnixMilli(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal payload: %w", err)
	}

	if IsEchoURL(wh.URL) {
		if s.Echo != nil {
			runID := ""
			if run != nil {
				runID = run.ID
			}
			s.Echo.Record(EchoEntry{
				Event:     event,
				WebhookID: wh.ID,
				Body:      string(body),
				RunID:     runID,
			})
		}
		return http.StatusOK, nil
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LiteMLflow-Event", event)
	if wh.Secret != "" {
		sig := hmacSHA256(wh.Secret, body)
		req.Header.Set("X-LiteMLflow-Signature", "sha256="+sig)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
