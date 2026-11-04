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

// One runner serializes verification, quick checks and one-page scan slices.
// Runtime deadlines are never replay queues. Disk checkpoints are observations,
// not stable offset cursors: restart and failed scans always rescan page one.
type EventsRunner struct {
	Host            notification.StepHost
	Source          *EventsSource
	Fetcher         EventFetcher
	NextQuick       time.Time
	pageReady       time.Time
	NeedFull        bool
	fullThrough     time.Time
	current         bool
	CacheReady      bool
	SliceStarted    time.Time
	slicePages      int
	sliceBytes      int64
	lastStep        time.Time
	windowAfterHint bool
}

// Scope resolution and candidate admission are serialized by the core, outside
// the provider. The returned binding is immutable and contains no secret value.
func (r *EventsRunner) scope(ctx context.Context) (EventsState, Binding, notification.Scope, error) {
	state, scope, err := r.Host.Read(ctx)
	var progress EventsState
	if state != nil {
		var ok bool
		progress, ok = state.(EventsState)
		if !ok {
			return progress, Binding{}, scope, notification.ErrInvalid
		}
	}
	if err != nil {
		return progress, Binding{}, scope, err
	}
	var binding Binding
	dec := json.NewDecoder(bytes.NewReader(scope.Data))
	dec.DisallowUnknownFields()
	if dec.Decode(&binding) != nil || dec.Decode(new(any)) != io.EOF || !validEventBinding(binding) || binding.RoomID != progress.RoomID || binding.RoomInstance != progress.RoomInstance || binding.URL != progress.Binding.URL || binding.Project != progress.Binding.Project {
		return progress, binding, scope, notification.ErrInvalid
	}
	return progress, binding, scope, nil
}

func (r *EventsRunner) RequestFull(now time.Time) {
	r.NeedFull = true
	if now.After(r.fullThrough) {
		r.fullThrough = now
	}
}

// One step performs at most one verification or FetchEvents call. Tests use a
// deterministic clock and this same entry rather than sleeping through ticks.
func (r *EventsRunner) Step(ctx context.Context) (time.Duration, bool) {
	force := r.Host.ConsumeForce()
	if force {
		r.Fetcher = nil
		r.CacheReady = false
	}
	s, b, scope, err := r.scope(ctx)
	if err != nil {
		return time.Second, !errors.Is(err, notification.ErrBusy)
	}
	if s.QueryPaused() {
		return 0, true
	}
	now := r.Host.Now()
	// A suspended computer may keep the process alive while offset pagination
	// changes remotely. Treat a long scheduling gap like reconnect, not page N.
	if !r.lastStep.IsZero() && now.Sub(r.lastStep) > time.Minute {
		next := s.Clone()
		if next.Scan != nil {
			next.Scan.NextPage, next.Scan.Pages, next.Scan.Bytes = 1, 0, 0
		}
		next.QuickCache = nil
		if now.Sub(next.Watermark) > 24*time.Hour {
			if next.Gap == nil {
				next.Gap = &EventsGap{Start: next.Watermark, End: now, Reason: "retention_unverified"}
			} else {
				next.Gap.End = now
			}
		}
		if err := r.Host.Commit(next); err != nil {
			return time.Second, !errors.Is(err, notification.ErrBusy)
		}
		s = next
		r.CacheReady = false
		r.windowAfterHint = false
		r.NextQuick = now
		r.SliceStarted = time.Time{}
		r.slicePages, r.sliceBytes = 0, 0
		r.RequestFull(now)
	}
	r.lastStep = now
	active := scope.Current
	if active != r.current {
		r.current = active
		if active {
			r.NextQuick = now
			r.RequestFull(now)
		} else if !r.NextQuick.IsZero() {
			r.NextQuick = now.Add(30 * time.Second)
		}
	}
	if force {
		r.windowAfterHint = false
		r.NextQuick = now
		r.RequestFull(now)
	}
	if notBefore := maxEventTime(s.RetryAt, s.ServerNotBefore); notBefore.After(now) {
		delay := notBefore.Sub(now)
		return delay, false
	}
	if r.Fetcher == nil {
		started := time.Now()
		prepCtx, cancel := notification.QueryContext(ctx, 30*time.Second)
		fetcher, projectID, prepErr := r.Source.Prepare(prepCtx, scope.Directory, b)
		if prepErr == nil && prepCtx.Err() != nil {
			prepErr = prepCtx.Err()
		}
		cancel()
		if prepErr != nil {
			return r.failure(ctx, prepErr, time.Since(started), false)
		}
		latest, _, latestScope, scopeErr := r.scope(ctx)
		if scopeErr != nil {
			return time.Second, !errors.Is(scopeErr, notification.ErrBusy)
		}
		if scope.Revision != latestScope.Revision {
			return time.Second, false
		}
		if projectID <= 0 || fetcher == nil || latest.ProjectID != 0 && latest.ProjectID != projectID {
			next := latest.Clone()
			next.Status = "binding_changed"
			next.ErrorCode = "project_identity_changed"
			if err := r.Host.Commit(next); errors.Is(err, notification.ErrBusy) {
				return time.Second, false
			}
			return 0, true
		}
		next := latest.Clone()
		next.ProjectID = projectID
		if next.Scan != nil {
			next.Scan.NextPage = 1
			next.Scan.Pages = 0
			next.Scan.Bytes = 0
		}
		next.QuickCache = nil // old validators cannot prove the new process's credential scope
		now = r.Host.Now()
		if now.Sub(next.Watermark) > 24*time.Hour {
			if next.Gap == nil {
				next.Gap = &EventsGap{Start: next.Watermark, End: now, Reason: "retention_unverified"}
			} else {
				next.Gap.End = now
			}
		}
		next.Status = "running"
		if err = r.Host.Commit(next); err != nil {
			return time.Second, !errors.Is(err, notification.ErrBusy)
		}
		r.Fetcher = fetcher
		r.RequestFull(now)
		r.NextQuick = now
		return time.Millisecond, false
	}
	q := EventQuery{Mode: "quick", Page: 1, PerPage: 20}
	// A long server polling hint can make every next quick tick overdue at
	// the same gate. Reserve one window request after a hinted quick result,
	// otherwise repeated quick checks could starve catch-up indefinitely.
	windowPending := s.Scan != nil || r.NeedFull || s.LastFull.IsZero() || !now.Before(s.LastFull.Add(EventsFullInterval))
	if !r.NextQuick.IsZero() && r.NextQuick.After(now) || r.windowAfterHint && windowPending {
		if s.Scan == nil && (r.NeedFull || s.LastFull.IsZero() || !now.Before(s.LastFull.Add(EventsFullInterval))) {
			next := s.Clone()
			start := maxEventTime(next.IgnoreBefore, next.Watermark.Add(-EventsOverlap))
			if !start.Before(now) {
				return minEventDelay(now, r.NextQuick, now.Add(time.Millisecond)), false
			}
			next.Scan = &EventsScan{Start: start, End: now, NextPage: 1}
			if err = r.Host.Commit(next); err != nil {
				return time.Second, !errors.Is(err, notification.ErrBusy)
			}
			s = next
		}
		if s.Scan == nil {
			delay := minEventDelay(now, r.NextQuick, s.LastFull.Add(EventsFullInterval))
			return delay, false
		}
		if r.pageReady.After(now) {
			delay := minEventDelay(now, r.NextQuick, r.pageReady)
			return delay, false
		}
		q = EventQuery{Mode: "window", Page: s.Scan.NextPage, PerPage: 20, Start: s.Scan.Start, End: s.Scan.End}
		if r.SliceStarted.IsZero() {
			r.SliceStarted = time.Now()
		}
		if r.slicePages >= r.Host.Limits().SlicePages || r.sliceBytes >= r.Host.Limits().SliceBytes || time.Since(r.SliceStarted) >= r.Host.Limits().SliceTime {
			r.SliceStarted = time.Time{}
			r.slicePages = 0
			r.sliceBytes = 0
			r.pageReady = now.Add(time.Second)
			delay := minEventDelay(now, r.NextQuick, r.pageReady)
			return delay, false
		}
	} else if s.Config.ETagVerified && r.CacheReady && s.QuickCache != nil {
		q.ETag = s.QuickCache.ETag
	}
	budget := 30 * time.Second
	if q.Mode == "window" {
		budget = min(budget, r.Host.Limits().SliceTime-time.Since(r.SliceStarted))
	}
	started := time.Now()
	requestCtx, cancel := notification.QueryContext(ctx, budget)
	batch, fetchErr := r.Fetcher.FetchEvents(requestCtx, q)
	if fetchErr == nil && requestCtx.Err() != nil {
		fetchErr = requestCtx.Err()
	}
	cancel()
	duration := time.Since(started)
	if fetchErr != nil {
		return r.failure(ctx, fetchErr, duration, true)
	}
	latest, _, latestScope, scopeErr := r.scope(ctx)
	if scopeErr != nil {
		return time.Second, !errors.Is(scopeErr, notification.ErrBusy)
	}
	if scope.Revision != latestScope.Revision {
		return time.Second, false
	}
	now = r.Host.Now()
	next := latest.Clone()
	if !validEventsBatch(q, batch, next) {
		return r.recordFailure(latest, now, errors.New("invalid event batch"), duration, true)
	}
	next.Metrics.Calls++
	next.Metrics.DurationMS += duration.Milliseconds()
	next.Metrics.ResponseBytes += batch.ResponseBytes
	wasDisconnected := next.ConsecutiveFailures > 0
	next.ConsecutiveFailures = 0
	next.Status = "running"
	next.ErrorCode = ""
	next.RetryAt = time.Time{}
	if batch.RetryAfterSeconds > 0 {
		next.ServerNotBefore = now.Add(time.Duration(batch.RetryAfterSeconds) * time.Second)
	}
	unknown, known, err := next.Accept(batch.Events, now)
	if err != nil {
		return r.recordFailure(latest, now, err, duration, true)
	}
	if q.Mode == "quick" {
		next.LastCheck = now
		if !batch.NotModified {
			next.QuickCache = nil
			if next.Config.ETagVerified && batch.ETag != "" {
				ids := make([]int64, 0, len(batch.Events))
				for _, e := range batch.Events {
					ids = append(ids, e.ID)
				}
				next.QuickCache = &EventsCache{SubscriptionVersion: next.SubscriptionVersion, ETag: batch.ETag, IDs: ids}
			}
		} else if batch.ETag != "" {
			next.QuickCache.ETag = batch.ETag
		}
	} else {
		next.Scan.Pages++
		next.Scan.Bytes += batch.ResponseBytes
		if batch.Complete {
			next.Watermark = next.Scan.End
			next.LastFull = now
			next.IgnoreBefore = maxEventTime(next.Baseline, next.Watermark.Add(-EventsOverlap))
			next.Scan = nil
			next.Compact()
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
		return time.Second, !errors.Is(err, notification.ErrBusy) // no candidate or validator was accepted
	}
	if q.Mode == "quick" {
		interval := 30 * time.Second
		if r.current {
			interval = time.Duration(next.Config.IntervalSeconds) * time.Second
		}
		r.NextQuick = now.Add(r.Host.Jitter(interval))
		r.CacheReady = true
		r.windowAfterHint = batch.RetryAfterSeconds > 0
		if wasDisconnected || next.LastFull.IsZero() || unknown > 0 && known == 0 {
			r.RequestFull(now)
		}
	} else {
		r.windowAfterHint = false
		r.slicePages++
		r.sliceBytes += batch.ResponseBytes
		r.pageReady = now.Add(100 * time.Millisecond)
		if batch.Complete {
			r.NeedFull = next.Watermark.Before(r.fullThrough)
			r.SliceStarted = time.Time{}
			r.slicePages = 0
			r.sliceBytes = 0
		}
	}
	return time.Millisecond, false
}

func (r *EventsRunner) failure(ctx context.Context, err error, duration time.Duration, called bool) (time.Duration, bool) {
	s, _, _, scopeErr := r.scope(ctx)
	if scopeErr != nil {
		return time.Second, !errors.Is(scopeErr, notification.ErrBusy)
	}
	return r.recordFailure(s, r.Host.Now(), err, duration, called)
}

func (r *EventsRunner) recordFailure(s EventsState, now time.Time, err error, duration time.Duration, called bool) (time.Duration, bool) {
	next := s.Clone()
	failure := notification.Failure{Called: called, Duration: duration}
	var queryErr *EventQueryError
	if errors.As(err, &queryErr) {
		failure.CredentialRejected = queryErr.HTTPStatus == 401 || queryErr.HTTPStatus == 403
		failure.RetryAfterSeconds = queryErr.RetryAfterSeconds
	}
	delay := next.RuntimeState.Failure(now, failure, r.Host.Jitter)
	if next.Scan != nil {
		next.Scan.NextPage = 1
		next.Scan.Pages = 0
		next.Scan.Bytes = 0
	}
	if saveErr := r.Host.Commit(next); saveErr != nil {
		return time.Second, !errors.Is(saveErr, notification.ErrBusy)
	}
	r.RequestFull(now)
	r.windowAfterHint = false
	r.NextQuick = now // successful reconnect must check both quick page and full window
	r.SliceStarted = time.Time{}
	r.slicePages = 0
	r.sliceBytes = 0
	return delay, next.Status == "credentials_invalid"
}

func validEventsBatch(q EventQuery, b EventBatch, s EventsState) bool {
	if b.Version != EventsProtocolVersion || b.Events == nil || len(b.Events) > 20 || b.ResponseBytes < 0 || b.ResponseBytes > 4<<20 || b.RetryAfterSeconds < 0 || b.RetryAfterSeconds > 86400 || len(b.ETag) > 1024 {
		return false
	}
	if b.NotModified {
		return q.Mode == "quick" && q.ETag != "" && s.QuickCache != nil && len(b.Events) == 0 && b.NextPage == 0 && !b.Complete
	}
	if b.NextPage < 0 || b.NextPage > 10000 || b.NextPage != 0 && b.NextPage <= q.Page || b.Complete != (b.NextPage == 0) {
		return false
	}
	for _, e := range b.Events {
		if e.ID <= 0 || e.ProjectID != s.ProjectID || e.CreatedAt.IsZero() || q.Mode == "window" && (e.CreatedAt.Before(q.Start) || !e.CreatedAt.Before(q.End)) {
			return false
		}
		switch e.Kind {
		case "merge_request", "note", "push", "unknown":
		default:
			return false
		}
	}
	return true
}

func maxEventTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minEventDelay(now time.Time, a, b time.Time) time.Duration {
	if b.Before(a) {
		return b.Sub(now)
	}
	return a.Sub(now)
}
