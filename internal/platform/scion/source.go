package scion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type Source struct{ Execute NativeExecute }

func (*Source) ID() notification.SourceID { return WorkitemsSourceID }
func (*Source) Schema() int               { return 1 }
func (*Source) Decode(b []byte, o platform.RoomIdentity) (notification.State, error) {
	var s State
	if inputstream.StrictDecode(b, &s) != nil || s.Validate(o) != nil {
		return nil, notification.ErrInvalid
	}
	return s, nil
}
func (s *Source) NewRunner(h notification.StepHost) notification.Runner {
	return &runner{source: s, host: h}
}

type runner struct {
	source   *Source
	host     notification.StepHost
	verified bool
}

func (r *runner) Step(ctx context.Context) (time.Duration, bool) {
	state, scope, e := r.host.Read(ctx)
	if e != nil {
		return time.Second, false
	}
	s, ok := state.(State)
	if !ok || !s.Config.Enabled || s.QueryPaused() {
		return time.Second, false
	}
	now := r.host.Now()
	force := r.host.ConsumeForce()
	if !force && now.Before(s.Discovery.Next) {
		return s.Discovery.Next.Sub(now), false
	}
	if now.Before(s.RetryAt) {
		return s.RetryAt.Sub(now), false
	}
	var q QueryScope
	if json.Unmarshal(scope.Data, &q) != nil || q.Binding != s.Binding || q.Owner != s.RoomIdentity || q.SubscriptionID != s.SubscriptionID || q.Target != s.Config.Target || !ValidProfile(q.Profile) || r.source.Execute == nil {
		return time.Second, false
	}
	n := s.Clone()
	n = n.Expire(r.host.Limits().ExpireBefore).(State)
	query, cancel := notification.QueryContext(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	var issues []Issue
	var truncated bool
	var size int
	verify := s.Discovery.Verify || !r.verified
	if verify {
		var repo RepositoryEvidence
		repo, e = ReadRepository(query, r.source.Execute, q, s.Config.Repository)
		if e == nil && repo != s.Repository {
			n.Status = "binding_changed"
			n.ErrorCode = "repository_identity_changed"
			n.Discovery.ErrorCode = n.ErrorCode
			_ = r.host.Commit(n)
			return time.Second, false
		}
		if e == nil {
			n.Discovery.Verify = false
		}
	} else {
		issues, truncated, size, e = readIssues(query, r.source.Execute, q, s.Config.Repository, s.Discovery.Limit)
	}
	n.LastCheck = now
	if e != nil {
		delay := n.RuntimeState.Failure(now, notification.Failure{Called: true, Duration: time.Since(start)}, r.host.Jitter)
		n.Discovery.ErrorCode = "query_failed"
		if errors.Is(e, notification.ErrConflict) {
			n.RuntimeState.ScopeFailed(e)
		}
		if errors.Is(e, platform.ErrCredential) {
			n.RuntimeState.InvalidateCredential(n.Config.Enabled)
		}
		// Only typed transport codes cross diagnostics, never native stderr or credentials.
		var code interface{ NotificationCode() string }
		if errors.As(e, &code) {
			n.Discovery.ErrorCode = code.NotificationCode()
			n.ErrorCode = code.NotificationCode()
		}
		_ = r.host.Commit(n)
		return delay, false
	}
	n.Metrics.Calls++
	n.Metrics.DurationMS += time.Since(start).Milliseconds()
	n.Metrics.ResponseBytes += int64(size)
	if !verify {
		limits := r.host.Limits()
		n.Discovery.ErrorCode = ""
		n.Discovery.ExpiredSkipped = 0
		for _, i := range issues {
			created := time.UnixMilli(i.Created).UTC()
			if created.Before(n.Baseline) {
				continue
			}
			if created.After(now.Add(time.Minute)) {
				n.Discovery.ErrorCode = "future_timestamp"
				n.Discovery.Next = now.Add(time.Duration(n.Config.IntervalSeconds) * time.Second)
				continue
			}
			key := creationKey(n.Binding.Account.Instance, n.Repository, i.Number)
			if _, exists := n.Observed[key]; exists {
				continue
			}
			if notification.Expired(created, n.ExpiredBefore) {
				n.Discovery.ExpiredSkipped++
				continue
			}
			route := notification.Route{Target: n.Config.Target, Version: n.SubscriptionVersion}
			item := issueItem(n, i, route)
			if len(n.Observed) >= limits.Observed || len(n.Inbox) >= limits.Inbox {
				n.Status = "capacity"
				n.ErrorCode = "capacity"
				_ = r.host.Commit(n)
				return time.Second, false
			}
			n.Observed[key] = Observation{Created: created, Route: route}
			n.Inbox[key] = item
			data, _ := json.Marshal(n)
			if int64(len(data)) > limits.Bytes {
				delete(n.Observed, key)
				delete(n.Inbox, key)
				n.Status = "capacity"
				n.ErrorCode = "capacity"
				_ = r.host.Commit(n)
				return time.Second, false
			}
			n.Metrics.NewItems++
		}
		n.Discovery.Truncated = truncated
		n.Discovery.Verify = true
		if truncated {
			n.Discovery.ErrorCode = "discovery_incomplete"
			n.ErrorCode = "discovery_incomplete"
			if n.Discovery.Limit < MaxDiscoveryLimit {
				n.Discovery.Limit = min(MaxDiscoveryLimit, n.Discovery.Limit*2)
				n.Discovery.Next = now
			} else {
				n.Status = "capacity"
				n.ErrorCode = "discovery_incomplete"
			}
		} else if n.Discovery.ErrorCode != "future_timestamp" {
			n.Discovery.LastFull = now
			n.Discovery.ErrorCode = ""
			n.ErrorCode = ""
			n.Discovery.Next = now.Add(time.Duration(n.Config.IntervalSeconds) * time.Second)
		}
	}
	if n.Status != "capacity" {
		n.ConsecutiveFailures = 0
		n.RetryAt = time.Time{}
		n.Status = "running"
		if n.Discovery.LastFull.IsZero() {
			n.Status = "initializing"
		}
	}
	if r.host.Commit(n) != nil {
		return time.Second, false
	}
	r.verified = verify
	return 0, false
}
func sourceRef(s State, i Issue, key string) platform.SourceRef {
	object := platform.ObjectReference{Type: "issue", ID: platform.ExternalID{Namespace: "scion:issue", Value: fmt.Sprint(i.Number)}}
	return platform.SourceRef{Provider: "scion", Channel: "workitems", Instance: s.Binding.Account.Instance, SubscriptionID: s.SubscriptionID, ItemKey: key, Project: s.Repository.Name, Object: "issue", URL: issueURL(s.Binding.Account.Instance, s.Repository.Name, i.Number), Reference: platform.NotificationReference{Version: 1, RecipientID: s.Binding.Account.ID, ProjectID: platform.ExternalID{Namespace: "scion:repository-composite", Value: repositoryID(s.Repository)}, Target: object, Object: object, Scope: "created"}}
}
func issueItem(s State, i Issue, route notification.Route) notification.Item {
	key := creationKey(s.Binding.Account.Instance, s.Repository, i.Number)
	body, truncated := notification.BoundedBody(i.Body, 8<<10)
	title, _ := notification.BoundedBody(i.Title, 1024)
	author, _ := notification.BoundedBody(i.Author, 512)
	text := fmt.Sprintf("Scion · %s · 新建 Issue #%d\n%s\n作者：%s · 状态：open · 创建于 %s\n%s\n%s", i.Repo, i.Number, title, author, time.UnixMilli(i.Created).UTC().Format(time.RFC3339Nano), body, issueURL(s.Binding.Account.Instance, i.Repo, i.Number))
	if truncated {
		text += "\n[正文摘要已截断；完整内容请查看原链接]"
	}
	item := notification.Item{Key: key, DeliveryID: route.DeliveryID(notification.DeliveryID(s.Metadata().Key, key)), CreatedAt: time.UnixMilli(i.Created).UTC(), Order: fmt.Sprintf("%020d", i.Number), Text: text, Source: sourceRef(s, i, key), Route: route}
	return notification.WithContent(item, &notification.Content{Structured: true, Text: text, Action: "created", Truncated: truncated, Sender: &notification.Participant{Name: author}, Context: &notification.Context{Name: fmt.Sprintf("%s · Issue #%d · %s", i.Repo, i.Number, title), Type: "issue", URL: item.Source.URL}})
}
