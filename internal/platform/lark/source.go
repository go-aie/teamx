package lark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type QueryScope struct {
	Binding           Binding             `json:"binding"`
	Profile           platform.CLIProfile `json:"profile"`
	ConnectionVersion uint64              `json:"connection_version"`
}
type Source struct {
	Reader func(QueryScope) (MessageReader, error)
}

func (*Source) ID() notification.SourceID { return MessagesSourceID }
func (*Source) Schema() int               { return 1 }
func (*Source) Decode(b []byte, o platform.RoomIdentity) (notification.State, error) {
	var s State
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Validate(o) != nil {
		return nil, notification.ErrInvalid
	}
	return s, nil
}
func (s *Source) NewRunner(h notification.StepHost) notification.Runner {
	return &runner{h: h, source: s}
}

type runner struct {
	h            notification.StepHost
	source       *Source
	slicePages   int
	sliceBytes   int64
	sliceStarted time.Time
}

func hash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func MessageKey(b Binding, m Message) string {
	return hash([]string{b.Account.Instance, b.Account.ID.Namespace, m.ChatID, m.ID})
}
func RevisionKey(b Binding, m Message) string {
	// CLI/API formatting and display-name changes are not native revisions.
	content := any(m.Body.Content)
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(m.Body.Content))
	decoder.UseNumber()
	if decoder.Decode(&decoded) == nil && decoder.Decode(new(any)) == io.EOF {
		content = decoded
	}
	mentions := make([]any, 0, len(m.Mentions))
	for _, at := range m.Mentions {
		mentions = append(mentions, []string{at.Key, at.ID, at.IDType, at.TenantKey})
	}
	return hash([]any{MessageKey(b, m), m.Updated, m.Deleted, m.Type, content, mentions})
}

func nativeNamespace(a platform.AccountIdentity, kind string) string {
	return "lark:" + kind + ":" + strings.TrimPrefix(a.ID.Namespace, "lark:open_id:")
}
func sourceRef(s State, m Message, key string, chat Chat) platform.SourceRef {
	ns := nativeNamespace(s.Binding.Account, "message_id")
	r := platform.NotificationReference{Version: 2, RecipientID: s.Binding.Account.ID, AccountTenant: s.Binding.Account.Tenant, ChatID: platform.ExternalID{Namespace: nativeNamespace(s.Binding.Account, "chat_id"), Value: m.ChatID}, Target: platform.ObjectReference{Type: "message", ID: platform.ExternalID{Namespace: ns, Value: m.ID}}}
	if m.ParentID != "" {
		r.ParentID = platform.ExternalID{Namespace: ns, Value: m.ParentID}
	}
	if m.RootID != "" {
		r.RootID = platform.ExternalID{Namespace: ns, Value: m.RootID}
	}
	if m.ThreadID != "" {
		r.DiscussionID = platform.ExternalID{Namespace: nativeNamespace(s.Binding.Account, "thread_id"), Value: m.ThreadID}
	}
	ref := platform.SourceRef{Provider: "lark", Channel: "messages", Instance: s.Binding.Account.Instance, SubscriptionID: s.SubscriptionID, ItemKey: key, Object: "message", Reference: r}
	if chat.TenantKey != "" {
		ref.Tenant = platform.ExternalID{Namespace: "lark:tenant_key", Value: chat.TenantKey}
	}
	return ref
}
func acceptPage(s State, id string, page MessagePage, limits notification.Limits) (State, error) {
	n := s.Clone()
	p := n.Chats[id]
	if p.Scan == nil || len(page.Items) > 50 || page.HasMore && (page.PageToken == "" || page.PageToken == p.Scan.Token) || len(page.PageToken) > 8192 {
		return s, failure("invalid_response")
	}
	last := int64(0)
	seen := map[string]bool{}
	for _, m := range page.Items {
		ms, _ := strconv.ParseInt(m.Created, 10, 64)
		if !m.valid() || m.ChatID != id || ms/1000 < p.Scan.Start || ms/1000 > p.Scan.End || ms < last || seen[m.ID] {
			return s, failure("invalid_response")
		}
		last = ms
		p.LastActivity = max(p.LastActivity, ms)
		seen[m.ID] = true
		if ms < n.Baseline.UnixMilli() {
			continue
		}
		key := RevisionKey(n.Binding, m)
		if _, ok := n.Observed[key]; ok {
			continue
		}
		route := notification.Route{Target: n.Config.Target, Version: n.SubscriptionVersion}
		oldRoute, wasAccepted := n.Accepted[MessageKey(n.Binding, m)]
		accepted := wasAccepted || Matches(m, p.Chat, n.Binding.Account.ID.Value, n.Config)
		if wasAccepted {
			route = oldRoute
		}
		n.Observed[key] = Observation{Message: m, Accepted: accepted, Route: route}
		if !accepted {
			continue
		}
		n.Accepted[MessageKey(n.Binding, m)] = route
		text := "飞书 · " + p.Chat.Name + "\n" + m.Content()
		if wasAccepted {
			text = "飞书消息变更（关联原消息；不自动重做已完成动作）\n" + m.Content()
		}
		if len(text) > 15<<10 {
			text = text[:15<<10]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
			text += "\n[正文已截断；原始结构保留在来源，可通过 lark read 重读；附件未读取]"
		}
		text = strings.ReplaceAll(text, "\x00", "�")
		item := notification.Item{Key: key, DeliveryID: route.DeliveryID(notification.DeliveryID(n.Metadata().Key, key)), CreatedAt: time.UnixMilli(ms).UTC(), Order: m.ID + ":" + key, Text: text, Route: route, Source: sourceRef(n, m, key, p.Chat)}
		n.Inbox[key] = Entry{Message: m, Item: item}
		n.Metrics.NewItems++
	}
	if len(n.Observed) > limits.Observed || len(n.Inbox) > limits.Inbox {
		return s, notification.ErrCapacity
	}
	p.Scan.Pages++
	if page.HasMore {
		p.Scan.Token = page.PageToken
	} else {
		p.Completed = p.Scan.End
		p.Scan = nil
		p.LastCheck = n.LastCheck
	}
	n.Chats[id] = p
	return n, nil
}
func (r *runner) Step(ctx context.Context) (time.Duration, bool) {
	state, scope, err := r.h.Read(ctx)
	if err != nil {
		return time.Second, ctx.Err() != nil
	}
	s, ok := state.(State)
	if !ok {
		return time.Second, true
	}
	force := r.h.ConsumeForce()
	now := r.h.Now()
	interval := time.Duration(s.Config.IntervalSeconds) * time.Second
	if s.QueryPaused() {
		return time.Second, true
	}
	if !force && now.Before(s.RetryAt) {
		return s.RetryAt.Sub(now), false
	}
	var q QueryScope
	if json.Unmarshal(scope.Data, &q) != nil || q.Binding != s.Binding || !ValidProfile(q.Profile) || !ValidAccount(q.Binding.Account, q.Profile) {
		return time.Second, true
	}
	var reader MessageReader
	if r.source.Reader != nil {
		reader, err = r.source.Reader(q)
	} else {
		reader, err = New(q.Profile)
	}
	if err != nil {
		return r.failed(s, err, now)
	}
	limits := r.h.Limits()
	if !r.sliceStarted.IsZero() && (r.slicePages >= max(1, limits.SlicePages) || r.sliceBytes >= limits.SliceBytes || time.Since(r.sliceStarted) >= limits.SliceTime) {
		r.slicePages = 0
		r.sliceBytes = 0
		r.sliceStarted = time.Time{}
		s.RetryAt = now.Add(interval)
		if r.h.Commit(s) != nil {
			return time.Second, false
		}
		return interval, false
	}
	previousChat, previousPriority := s.NextChat, s.PriorityTurn
	id, wait := s.selectChat(now, force)
	if s.Config.AllChats() {
		if s.Directory == nil {
			s.Directory = &DirectoryProgress{}
		}
		d := s.Directory
		due := d.Scan != nil || !now.Before(d.RetryAt)
		if due && (!d.Yield || id == "") {
			s.NextChat, s.PriorityTurn = previousChat, previousPriority
			if d.Scan == nil {
				d.Scan = &DirectoryScan{Seen: map[string]bool{}}
				if r.h.Commit(s) != nil {
					return time.Second, false
				}
			}
			return r.directoryPage(ctx, s, reader, now)
		}
	}
	if id == "" {
		return wait, false
	}
	if r.sliceStarted.IsZero() {
		r.sliceStarted = time.Now()
	}
	p, verified := s.Chats[id]
	if s.Directory != nil {
		s.Directory.Yield = false
	}
	queryCtx, cancel := notification.QueryContext(ctx, limits.SliceTime)
	defer cancel()
	p.LastAttempt = now
	if !verified || p.NeedsVerification || !p.VerifiedAt.IsZero() && !now.Before(p.VerifiedAt.Add(DirectoryInterval)) {
		chat, e := reader.Chat(queryCtx, s.Binding.Account, id)
		if ctx.Err() != nil {
			return time.Second, true
		}
		if e != nil {
			s.Chats[id] = p
			return r.chatFailed(s, id, e, now)
		}
		if !chat.Valid() || chat.ID != id {
			return r.failed(s, failure("invalid_response"), now)
		}
		if verified && p.Chat.TenantKey != "" && p.Chat.TenantKey != chat.TenantKey {
			return r.chatFailed(s, id, failure("chat_unavailable"), now)
		}
		p.Chat = chat
		p.NeedsVerification = false
		p.VerifiedAt = now
		p.ErrorCode = ""
		p.RetryAt = time.Time{}
		s.Chats[id] = p
		// Keep this scheduling slot until its message page is read. With many
		// slow chats, rotating after metadata alone can let verification expire
		// before the next visit, repeatedly delaying the first message read.
		s.NextChat, s.PriorityTurn = previousChat, previousPriority
		s.Status = "running"
		s.ErrorCode = ""
		s.Metrics.Calls++
		r.slicePages++
		if r.h.Commit(s) != nil {
			return time.Second, false
		}
		return time.Millisecond, false
	}
	if p.Scan == nil {
		s.pruneObserved()
		start := max(s.Baseline.Unix(), p.Completed-300)
		end := now.Unix() - 1
		if end < start {
			return time.Second, false
		}
		p.Scan = &Scan{Start: start, End: end}
		s.Chats[id] = p
		if r.h.Commit(s) != nil {
			return time.Second, false
		}
	}
	page, e := reader.Messages(queryCtx, s.Binding.Account, MessagesRequest{ChatID: id, Start: p.Scan.Start, End: p.Scan.End, PageSize: 50, PageToken: p.Scan.Token})
	if ctx.Err() != nil {
		return time.Second, true
	}
	if e != nil {
		if ErrorCode(e) == "cursor_expired" {
			p.Scan.Token = ""
			p.Scan.Pages = 0
			s.Chats[id] = p
		}
		return r.chatFailed(s, id, e, now)
	}
	size, _ := json.Marshal(page)
	r.slicePages++
	r.sliceBytes += int64(len(size))
	if int64(len(size)) > limits.SliceBytes {
		return r.failed(s, failure("output_limit"), now)
	}
	s.LastCheck = now
	s.Metrics.Calls++
	s.Metrics.ResponseBytes += int64(len(size))
	s.Chats[id] = p
	n, e := acceptPage(s, id, page, limits)
	if e != nil {
		return r.failed(s, e, now)
	}
	n.Status = "running"
	n.ErrorCode = ""
	n.ConsecutiveFailures = 0
	n.RetryAt = time.Time{}
	p = n.Chats[id]
	p.ErrorCode = ""
	p.RetryAt = time.Time{}
	n.Chats[id] = p
	if p.Scan == nil {
		r.slicePages = 0
		r.sliceBytes = 0
		r.sliceStarted = time.Time{}
	}
	n.updateFull(now)
	if e = r.h.Commit(n); errors.Is(e, notification.ErrCapacity) {
		return r.failed(s, e, now)
	}
	return time.Millisecond, false
}

// Two round-robin slots followed by one backlog/recent-activity slot. Rotate
// after each message page; metadata verification retains its slot for that page.
// An unbounded hot chat still cannot pin a Room.
func (s *State) selectChat(now time.Time, force bool) (string, time.Duration) {
	ids := s.ChatIDs()
	wait := time.Duration(s.Config.IntervalSeconds) * time.Second
	if s.Config.AllChats() && s.Directory != nil && s.Directory.Scan == nil && s.Directory.RetryAt.After(now) {
		wait = min(wait, s.Directory.RetryAt.Sub(now))
	}
	if len(ids) == 0 {
		return "", max(time.Millisecond, wait)
	}
	start := s.NextChat % len(ids)
	selected := -1
	due := func(id string) bool {
		p, ok := s.Chats[id]
		if now.Before(p.RetryAt) {
			wait = min(wait, p.RetryAt.Sub(now))
			return false
		}
		next := p.LastCheck.Add(time.Duration(s.Config.IntervalSeconds) * time.Second)
		if !ok || p.NeedsVerification || p.Scan != nil || force || p.LastCheck.IsZero() || !now.Before(next) {
			return true
		}
		wait = min(wait, next.Sub(now))
		return false
	}
	for i := range ids {
		j := (start + i) % len(ids)
		if due(ids[j]) {
			selected = j
			break
		}
	}
	if selected < 0 {
		return "", max(time.Millisecond, wait)
	}
	if s.PriorityTurn == 2 {
		best := s.Chats[ids[selected]]
		for j, id := range ids {
			p := s.Chats[id]
			if due(id) && (p.Scan != nil && best.Scan == nil || (p.Scan != nil) == (best.Scan != nil) && p.LastActivity > best.LastActivity) {
				selected = j
				best = p
			}
		}
	}
	s.PriorityTurn = (s.PriorityTurn + 1) % 3
	if s.PriorityTurn != 0 {
		s.NextChat = (selected + 1) % len(ids)
	}
	return ids[selected], 0
}
func validChatError(code string) bool {
	return code == "" || code == "permission_denied" || code == "chat_unavailable"
}
func (r *runner) chatFailed(s State, id string, err error, now time.Time) (time.Duration, bool) {
	code := ErrorCode(err)
	if code == "permission_denied" || code == "chat_unavailable" {
		p := s.Chats[id]
		if p.Chat.ID == "" {
			p.Chat.ID = id
		}
		p.ErrorCode = code
		p.RetryAt = now.Add(DirectoryInterval)
		p.NeedsVerification = true
		p.LastAttempt = now
		s.Chats[id] = p
		s.Metrics.Calls++
		s.Metrics.Failures++
		s.updateFull(now)
		if r.h.Commit(s) != nil {
			return time.Second, false
		}
		return time.Millisecond, false
	}
	return r.failed(s, err, now)
}
func (r *runner) directoryPage(ctx context.Context, s State, reader MessageReader, now time.Time) (time.Duration, bool) {
	directory, ok := reader.(DirectoryReader)
	if !ok {
		return r.failed(s, failure("directory_unsupported"), now)
	}
	if r.sliceStarted.IsZero() {
		r.sliceStarted = time.Now()
	}
	queryCtx, cancel := notification.QueryContext(ctx, r.h.Limits().SliceTime)
	defer cancel()
	page, err := directory.Chats(queryCtx, s.Binding.Account, ChatsRequest{PageSize: 50, PageToken: s.Directory.Scan.Token})
	if ctx.Err() != nil {
		return time.Second, true
	}
	if err != nil {
		if ErrorCode(err) == "cursor_expired" {
			s.Directory.Scan = &DirectoryScan{Seen: map[string]bool{}}
		}
		return r.failed(s, err, now)
	}
	size, _ := json.Marshal(page)
	r.slicePages++
	r.sliceBytes += int64(len(size))
	if int64(len(size)) > r.h.Limits().SliceBytes {
		return r.failed(s, failure("output_limit"), now)
	}
	n, err := acceptDirectory(s, page, now)
	if err != nil {
		return r.failed(s, err, now)
	}
	n.Metrics.Calls++
	n.Metrics.ResponseBytes += int64(len(size))
	n.Status = "running"
	n.ErrorCode = ""
	n.ConsecutiveFailures = 0
	n.RetryAt = time.Time{}
	if err = r.h.Commit(n); errors.Is(err, notification.ErrCapacity) {
		return r.failed(s, err, now)
	}
	return time.Millisecond, false
}
func (r *runner) failed(s State, err error, now time.Time) (time.Duration, bool) {
	if errors.Is(err, notification.ErrCapacity) {
		s.Status = "capacity"
		s.ErrorCode = "capacity"
		if r.h.Commit(s) != nil {
			return time.Second, false
		}
		return time.Second, true
	}
	code := ErrorCode(err)
	pause := code == "identity_mismatch" || code == "credential_rejected" || code == "credential_unavailable" || code == "permission_denied" || code == "cli_version_mismatch"
	d := s.RuntimeState.Failure(now, notification.Failure{Called: true, CredentialRejected: pause}, r.h.Jitter)
	s.ErrorCode = code
	if r.h.Commit(s) != nil {
		return time.Second, false
	}
	return d, pause
}
