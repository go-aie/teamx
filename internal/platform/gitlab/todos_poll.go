package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// Complete reconciliation retains coverage when offset pages move or an item
// changes state outside the latest page. Ordinary polls read only two pages.
const TodosFullInterval = 5 * time.Minute

type TodosRunner struct {
	quickState   string
	Host         notification.StepHost
	Source       *TodosSource
	Fetcher      TodoFetcher
	ready        bool
	lastStep     time.Time
	nextPoll     time.Time
	sliceStarted time.Time
	slicePages   int
	sliceBytes   int64
	current      bool
}

func (r *TodosRunner) read(ctx context.Context) (TodosState, TodosQueryScope, notification.Scope, error) {
	state, scope, err := r.Host.Read(ctx)
	s, ok := state.(TodosState)
	if err != nil {
		return s, TodosQueryScope{}, scope, err
	}
	if !ok {
		return s, TodosQueryScope{}, scope, notification.ErrInvalid
	}
	var b TodosQueryScope
	dec := json.NewDecoder(bytes.NewReader(scope.Data))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || dec.Decode(new(any)) != io.EOF || !b.Binding.Valid() || b.ConnectionVersion == 0 || b.Binding.ConnectionID != s.Binding.ConnectionID || b.Binding.ConnectionGeneration != s.Binding.ConnectionGeneration || !b.Binding.SameAccount(s.Binding) || b.Binding.CredentialVersion != s.Binding.CredentialVersion || b.Binding.CredentialFingerprint != s.Binding.CredentialFingerprint {
		return s, b, scope, notification.ErrInvalid
	}
	return s, b, scope, nil
}
func (r *TodosRunner) interval(s TodosState, current bool) time.Duration {
	if current {
		return time.Duration(s.Config.IntervalSeconds) * time.Second
	}
	return time.Duration(InactiveIntervalSeconds) * time.Second
}
func todoCommitResult(err error) (time.Duration, bool) {
	return time.Second, err != nil && !errors.Is(err, notification.ErrBusy)
}
func (r *TodosRunner) Step(ctx context.Context) (time.Duration, bool) {
	force := r.Host.ConsumeForce()
	s, b, scope, err := r.read(ctx)
	if err != nil {
		return time.Second, !errors.Is(err, notification.ErrBusy)
	}
	if s.QueryPaused() || s.Retired {
		return 0, true
	}
	now := r.Host.Now()
	interval := r.interval(s, scope.Current)
	if scope.Current != r.current {
		if scope.Current {
			force = true
		} else if !r.nextPoll.IsZero() {
			r.nextPoll = now.Add(interval)
		}
		r.current = scope.Current
	}
	if !r.lastStep.IsZero() && now.Sub(r.lastStep) > max(2*interval, time.Minute) {
		r.ready = false
		r.Fetcher = nil
	}
	r.lastStep = now
	if force {
		r.ready = false
		r.Fetcher = nil
		r.nextPoll = time.Time{}
	}
	if wait := maxEventTime(s.RetryAt, s.ServerNotBefore); wait.After(now) {
		return wait.Sub(now), false
	}
	// A source instance has no stable remote offset across restarts or explicit
	// reconnects. Persist a restart at pending page one before reading any page.
	if !r.ready {
		next := s.Clone()
		next.Scan = &TodosScan{StartedAt: now, State: "pending", NextPage: 1}
		if err = r.Host.Commit(next); err != nil {
			return todoCommitResult(err)
		}
		s = next
		r.ready = true
		r.quickState = ""
		r.sliceStarted = time.Time{}
		r.slicePages = 0
		r.sliceBytes = 0
	}
	quick := false
	if s.Scan == nil {
		if r.nextPoll.IsZero() {
			r.nextPoll = s.LastFull.Add(interval)
		}
		if r.nextPoll.After(now) {
			return r.nextPoll.Sub(now), false
		}
		if !s.LastFull.IsZero() && now.Before(s.LastFull.Add(TodosFullInterval)) {
			quick = true
		} else {
			next := s.Clone()
			next.Scan = &TodosScan{StartedAt: now, State: "pending", NextPage: 1}
			if err = r.Host.Commit(next); err != nil {
				return todoCommitResult(err)
			}
			s = next
			r.quickState = ""
		}
	}
	limits := r.Host.Limits()
	if r.sliceStarted.IsZero() {
		r.sliceStarted = time.Now()
	}
	remaining := limits.SliceTime - time.Since(r.sliceStarted)
	if r.slicePages >= limits.SlicePages || r.sliceBytes >= limits.SliceBytes || remaining <= 0 {
		next := s.Clone()
		if next.Scan != nil {
			next.Scan.Limited = true
		}
		next.ErrorCode = "scan_incomplete"
		next.RetryAt = now.Add(interval)
		if err = r.Host.Commit(next); err != nil {
			return todoCommitResult(err)
		}
		r.sliceStarted = time.Time{}
		r.slicePages = 0
		r.sliceBytes = 0
		return interval, false
	}
	requestCtx, cancel := notification.QueryContext(ctx, remaining)
	started := time.Now()
	if r.Fetcher == nil {
		r.Fetcher, err = r.Source.Prepare(requestCtx, scope.Directory, s.RoomIdentity, b)
		if err == nil && r.Fetcher == nil {
			err = notification.ErrInvalid
		}
	}
	if err != nil {
		cancel()
		return r.failure(ctx, err, time.Since(started), false)
	}
	q := TodoQuery{Page: 1, PerPage: 20, State: "pending", MaxBytes: min(int64(maxTodoOutput), limits.SliceBytes-r.sliceBytes), Timeout: min(30*time.Second, max(time.Millisecond, remaining-time.Since(started)))}
	if quick {
		if r.quickState != "" {
			q.State = r.quickState
		}
	} else {
		q.State, q.Page, q.PerPage = s.Scan.State, s.Scan.NextPage, 100
	}
	batch, fetchErr := r.Fetcher.FetchTodos(requestCtx, q)
	if fetchErr == nil && requestCtx.Err() != nil {
		fetchErr = requestCtx.Err()
	}
	cancel()
	duration := time.Since(started)
	if fetchErr != nil {
		if errors.Is(fetchErr, notification.ErrBusy) {
			r.Fetcher = nil
			return time.Second, false
		}
		return r.failure(ctx, fetchErr, duration, true)
	}
	latest, _, latestScope, readErr := r.read(ctx)
	if readErr != nil {
		return time.Second, !errors.Is(readErr, notification.ErrBusy)
	}
	if scope.Revision != latestScope.Revision {
		r.Fetcher = nil
		return time.Second, false
	}
	// Re-read to include concurrent acknowledgements. A competing candidate after
	// this read is rejected by Commit; runtime page/budget progress stays unchanged.
	if (latest.Scan == nil) != (s.Scan == nil) || s.Scan != nil && *latest.Scan != *s.Scan {
		return time.Second, false
	}
	if !validTodosBatch(q, batch, latest) {
		return r.recordFailure(latest, errors.New("invalid todo batch"), duration, true)
	}
	next := latest.Clone()
	now = r.Host.Now()
	next.Accept(batch.Todos, now)
	next.LastCheck = now
	next.Metrics.Calls++
	next.Metrics.DurationMS += duration.Milliseconds()
	next.Metrics.ResponseBytes += batch.ResponseBytes
	next.ConsecutiveFailures = 0
	next.ErrorCode = ""
	next.Status = "running"
	next.RetryAt = time.Time{}
	if batch.RetryAfterSeconds > 0 {
		next.ServerNotBefore = now.Add(time.Duration(batch.RetryAfterSeconds) * time.Second)
	}
	if !quick {
		next.Scan.Pages++
		next.Scan.Bytes += batch.ResponseBytes
		next.Scan.Limited = false
		if batch.Complete {
			if q.State == "pending" {
				next.Scan.State = "done"
				next.Scan.NextPage = 1
			} else {
				next.Scan = nil
				next.LastFull = now
			}
		} else {
			next.Scan.NextPage = batch.NextPage
		}
	}
	if err = r.Host.Commit(next); err != nil {
		if errors.Is(err, notification.ErrCapacity) {
			paused := latest.Clone()
			paused.Status = "capacity"
			paused.ErrorCode = "capacity"
			_ = r.Host.Commit(paused)
		}
		return todoCommitResult(err)
	}
	r.slicePages++
	r.sliceBytes += batch.ResponseBytes
	if quick && q.State == "pending" {
		r.quickState = "done"
		return time.Millisecond, false
	}
	if next.Scan == nil {
		r.quickState = ""
		r.nextPoll = now.Add(r.Host.Jitter(interval))
		if fullDue := next.LastFull.Add(TodosFullInterval); r.nextPoll.After(fullDue) {
			r.nextPoll = fullDue
		}
		r.sliceStarted = time.Time{}
		r.slicePages = 0
		r.sliceBytes = 0
		return interval, false
	}
	return time.Millisecond, false
}
func validTodosBatch(q TodoQuery, b TodoBatch, s TodosState) bool {
	if b.Version != TodosProtocolVersion || b.Todos == nil || len(b.Todos) > q.PerPage || b.NextPage < 0 || b.NextPage > 10000 || b.NextPage != 0 && b.NextPage <= q.Page || b.Complete != (b.NextPage == 0) || b.ResponseBytes < 0 || b.ResponseBytes > q.MaxBytes || b.RetryAfterSeconds < 0 || b.RetryAfterSeconds > 86400 {
		return false
	}
	seen := map[int64]bool{}
	for _, t := range b.Todos {
		if !validTodo(t, s.Binding.URL, s.Binding.UserID, q) || seen[t.ID] {
			return false
		}
		seen[t.ID] = true
	}
	return true
}
func (r *TodosRunner) failure(ctx context.Context, err error, duration time.Duration, called bool) (time.Duration, bool) {
	s, _, _, readErr := r.read(ctx)
	if readErr != nil {
		return time.Second, !errors.Is(readErr, notification.ErrBusy)
	}
	return r.recordFailure(s, err, duration, called)
}
func (r *TodosRunner) recordFailure(s TodosState, err error, duration time.Duration, called bool) (time.Duration, bool) {
	next := s.Clone()
	failure := notification.Failure{Called: called, Duration: duration}
	var qe *TodoQueryError
	if errors.As(err, &qe) {
		failure.CredentialRejected = qe.HTTPStatus == 401 || qe.HTTPStatus == 403
		failure.RetryAfterSeconds = qe.RetryAfterSeconds
	}
	delay := next.RuntimeState.Failure(r.Host.Now(), failure, r.Host.Jitter)
	next.Scan = &TodosScan{StartedAt: r.Host.Now(), State: "pending", NextPage: 1}
	if err = r.Host.Commit(next); err != nil {
		return todoCommitResult(err)
	}
	r.ready = true
	r.Fetcher = nil
	r.sliceStarted = time.Time{}
	r.slicePages = 0
	r.sliceBytes = 0
	return delay, next.Status == "credentials_invalid"
}
