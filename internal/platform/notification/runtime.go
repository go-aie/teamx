package notification

import (
	"errors"
	"time"
)

// RuntimeState is shared scheduling/diagnostic state. Provider progress (dates,
// cursors, filters, scan checkpoints) never belongs here.
type RuntimeState struct {
	RetryAt             time.Time `json:"retry_at"`
	ServerNotBefore     time.Time `json:"server_not_before"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Status              string    `json:"status"`
	DeliveryErrorCode   string    `json:"delivery_error_code,omitempty"`
	ErrorCode           string    `json:"error_code,omitempty"`
	Metrics             Metrics   `json:"metrics"`
}
type Metrics struct {
	Calls         uint64 `json:"calls"`
	Failures      uint64 `json:"failures"`
	DurationMS    int64  `json:"duration_ms"`
	ResponseBytes int64  `json:"response_bytes"`
	NewItems      uint64 `json:"new_items"`
}
type Failure struct {
	Called             bool
	Duration           time.Duration
	CredentialRejected bool
	RetryAfterSeconds  int
}

func (r RuntimeState) QueryPaused() bool {
	return r.Status == "credentials_invalid" || r.Status == "capacity" || r.Status == "binding_changed"
}
func (r RuntimeState) Validate(enabled bool) error {
	if r.ConsecutiveFailures < 0 || r.ConsecutiveFailures > 30 || r.Metrics.DurationMS < 0 || r.Metrics.ResponseBytes < 0 || !enabled && r.Status != "disabled" || enabled && r.Status == "disabled" || r.DeliveryErrorCode != "" && r.DeliveryErrorCode != "room_capacity" && r.DeliveryErrorCode != "inbox_capacity" && r.DeliveryErrorCode != "target_unavailable" && r.DeliveryErrorCode != "target_removed" {
		return ErrInvalid
	}
	switch r.Status {
	case "initializing", "running", "disabled", "backoff", "credentials_invalid", "capacity", "binding_changed":
		return nil
	}
	return ErrInvalid
}
func (r *RuntimeState) Reset(enabled, initializing bool) {
	r.RetryAt = time.Time{}
	r.ConsecutiveFailures = 0
	r.ErrorCode = ""
	r.Status = "running"
	if !enabled {
		r.Status = "disabled"
	} else if initializing {
		r.Status = "initializing"
	}
}
func (r *RuntimeState) InvalidateCredential(enabled bool) {
	r.Status = "credentials_invalid"
	r.ErrorCode = "credential_unavailable"
	r.RetryAt = time.Time{}
	if !enabled {
		r.Status = "disabled"
		r.ErrorCode = ""
	}
}
func (r *RuntimeState) ScopeFailed(err error) {
	if errors.Is(err, ErrConflict) {
		r.Status = "binding_changed"
		r.ErrorCode = "binding_changed"
	} else {
		r.Status = "credentials_invalid"
		r.ErrorCode = "credential_unavailable"
	}
}
func (r *RuntimeState) Failure(now time.Time, failure Failure, jitter func(time.Duration) time.Duration) time.Duration {
	r.Metrics.Failures++
	if failure.Called {
		r.Metrics.Calls++
		r.Metrics.DurationMS += failure.Duration.Milliseconds()
	}
	r.ConsecutiveFailures = min(30, r.ConsecutiveFailures+1)
	r.Status = "backoff"
	r.ErrorCode = "query_failed"
	if failure.CredentialRejected {
		r.Status = "credentials_invalid"
		r.ErrorCode = "credential_rejected"
	}
	delay := Backoff(r.ConsecutiveFailures, jitter, failure.RetryAfterSeconds)
	if failure.RetryAfterSeconds > 0 {
		r.ServerNotBefore = now.Add(time.Duration(min(86400, failure.RetryAfterSeconds)) * time.Second)
	}
	r.RetryAt = now.Add(delay)
	return delay
}
