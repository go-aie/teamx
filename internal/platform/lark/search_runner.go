package lark

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type SearchReader interface {
	MessageReader
	SearchMessages(context.Context, platform.AccountIdentity, MessageSearchRequest) (MessageSearchPage, error)
	BatchMessages(context.Context, platform.AccountIdentity, []string) (MessageBatch, error)
	GroupMembership(context.Context, platform.AccountIdentity, string, string) (MembershipPage, error)
}

type searchRunner struct {
	h      notification.StepHost
	source *SearchSource
	pages  int
	bytes  int64
	start  time.Time
}

func (r *searchRunner) save(s State) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if int64(len(raw)) > r.h.Limits().Bytes {
		return notification.ErrCapacity
	}
	return r.h.Commit(s)
}

func (r *searchRunner) commit(s State) (time.Duration, bool) {
	err := r.save(s)
	if errors.Is(err, notification.ErrCapacity) {
		// Keep the persisted page and all admission progress on capacity failure.
		state, _, e := r.h.Read(context.Background())
		if e != nil {
			return time.Second, false
		}
		n := state.(State)
		n.Status, n.ErrorCode = "capacity", "capacity"
		if r.save(n) != nil {
			return time.Second, false
		}
		return time.Second, true
	}
	if err != nil {
		return time.Second, false
	}
	return time.Millisecond, false
}
func (r *searchRunner) failed(s State, i int, err error, now time.Time) (time.Duration, bool) {
	code := ErrorCode(err)
	if errors.Is(err, notification.ErrCapacity) {
		s.Status, s.ErrorCode = "capacity", "capacity"
		if r.save(s) != nil {
			return time.Second, false
		}
		return time.Second, true
	}
	if code == "identity_mismatch" || code == "credential_rejected" || code == "credential_unavailable" || code == "cli_version_mismatch" {
		s.RuntimeState.Failure(now, notification.Failure{CredentialRejected: true}, r.h.Jitter)
		s.ErrorCode = code
		if r.save(s) != nil {
			return time.Second, false
		}
		return time.Second, true
	}
	b := &s.Search.Branches[i]
	b.Failures = min(30, b.Failures+1)
	b.ErrorCode = code
	b.RetryAt = now.Add(notification.Backoff(b.Failures, r.h.Jitter, 0))
	s.Metrics.Failures++
	// Branch-local permission/rate/network failures must not pause other queries.
	s.Status = "running"
	return r.commit(s)
}

func (r *searchRunner) Step(ctx context.Context) (time.Duration, bool) {
	state, scope, err := r.h.Read(ctx)
	if err != nil {
		return time.Second, ctx.Err() != nil
	}
	s, ok := state.(State)
	if !ok || s.Search == nil || !s.Config.Enabled || s.Retired || s.QueryPaused() {
		return time.Second, true
	}
	s = s.Clone() // keep the pre-step snapshot for atomic failure/capacity paths
	now := r.h.Now()
	force := r.h.ConsumeForce() // provider and slice backoff remain authoritative
	if now.Before(s.RetryAt) {
		return s.RetryAt.Sub(now), false
	}
	var scopeData QueryScope
	if json.Unmarshal(scope.Data, &scopeData) != nil || scopeData.Binding != s.Binding || !ValidProfile(scopeData.Profile) || !ValidAccount(s.Binding.Account, scopeData.Profile) {
		return time.Second, true
	}
	limits := r.h.Limits()
	interval := time.Duration(s.Config.IntervalSeconds) * time.Second
	if !r.start.IsZero() && (r.pages >= max(1, limits.SlicePages) || r.bytes >= limits.SliceBytes || time.Since(r.start) >= limits.SliceTime) {
		r.pages, r.bytes, r.start = 0, 0, time.Time{}
		s.RetryAt = now.Add(interval)
		return r.commit(s)
	}
	requests := s.searchRequests()
	i, wait := -1, interval
	for offset := range requests {
		j := (s.Search.Next + offset) % len(requests)
		b := s.Search.Branches[j]
		if now.Before(b.RetryAt) && !(force && b.ErrorCode == "") {
			wait = min(wait, b.RetryAt.Sub(now))
			continue
		}
		i = j
		break
	}
	if i < 0 {
		return max(time.Millisecond, wait), false
	}
	s.Search.Next = (i + 1) % len(requests)
	b := &s.Search.Branches[i]
	q := requests[i].q
	if b.Scan == nil {
		start := s.searchStart(q)
		if b.Completed > 0 {
			start = max(s.Baseline.Unix(), b.Completed-searchOverlap)
		}
		end := min(now.Unix()-1, start+searchWindow)
		if end <= start {
			b.RetryAt = now.Add(interval)
			return r.commit(s)
		}
		s.pruneObserved()
		b.Scan = &SearchScan{Start: start, End: end, Tokens: map[string]bool{}}
		return r.commit(s) // freeze before any remote discovery
	}
	q.Start, q.End, q.PageToken = time.Unix(b.Scan.Start, 0).UTC(), time.Unix(b.Scan.End, 0).UTC(), b.Scan.Token
	var base MessageReader
	if r.source.Reader != nil {
		base, err = r.source.Reader(scopeData)
	} else {
		base, err = New(scopeData.Profile)
	}
	if err != nil {
		return r.failed(s, i, err, now)
	}
	reader, ok := base.(SearchReader)
	if !ok {
		return r.failed(s, i, failure("search_unsupported"), now)
	}
	queryCtx, cancel := notification.QueryContext(ctx, limits.SliceTime)
	defer cancel()
	if r.start.IsZero() {
		r.start = time.Now()
	}
	// Every step issues at most one bounded search, detail or relationship call.
	var response any
	called := false
	windowFinished := false
	p := b.Scan.Page
	switch {
	case p == nil:
		called = true
		var page MessageSearchPage
		page, err = reader.SearchMessages(queryCtx, s.Binding.Account, q)
		if err == nil {
			if page.Items == nil || len(page.Items) > 50 || !validPageToken(page.PageToken) || page.HasMore && (page.PageToken == "" || page.PageToken == q.PageToken || b.Scan.Tokens[hash(page.PageToken)]) {
				err = failure("invalid_response")
			} else {
				seen := map[string]bool{}
				for _, item := range page.Items {
					if !validSearchCandidate(item, q) || seen[item.ID] {
						err = failure("invalid_response")
					}
					seen[item.ID] = true
				}
			}
		}
		if err == nil {
			b.Scan.Page = &SearchPage{Items: page.Items, NextToken: page.PageToken, HasMore: page.HasMore, Details: map[string]Message{}}
			s.Search.Metrics.Pages++
			s.Search.Metrics.Hits += uint64(len(page.Items))
			response = page
		}
	case p.Done == len(p.Items):
		if p.HasMore {
			b.Scan.Tokens[hash(p.NextToken)] = true
			b.Scan.Token, b.Scan.Page = p.NextToken, nil
		} else {
			// A shortened window was capped by wall time when frozen. Slow
			// remote reads may finish seconds later; that does not turn it into
			// historical backlog or justify bypassing the configured interval.
			currentWindow := b.Scan.End-b.Scan.Start < searchWindow
			b.Completed = b.Scan.End
			b.Scan = nil
			windowFinished = true
			b.LastSuccess = now
			// Catch up bounded historical windows without an interval per window.
			b.RetryAt = time.Time{}
			if currentWindow || b.Completed >= now.Unix()-1 {
				b.RetryAt = now.Add(interval)
			}
			s.completeSearchChats()
			s.updateFull(now)
		}
	case !p.BatchRead:
		called = true
		ids := make([]string, 0, len(p.Items)-p.Done)
		for _, item := range p.Items[p.Done:] {
			if _, found := p.Details[item.ID]; !found {
				ids = append(ids, item.ID)
			}
		}
		var batch MessageBatch
		batch, err = reader.BatchMessages(queryCtx, s.Binding.Account, ids)
		if err == nil {
			missing := map[string]bool{}
			for _, id := range batch.MissingIDs {
				if !slices.Contains(ids, id) || missing[id] {
					err = failure("invalid_response")
				}
				missing[id] = true
			}
			seen := map[string]bool{}
			for _, m := range batch.Items {
				if !slices.Contains(ids, m.ID) || seen[m.ID] || missing[m.ID] {
					err = failure("invalid_response")
				}
				seen[m.ID] = true
				for _, item := range p.Items[p.Done:] {
					if item.ID == m.ID && !searchDetailMatches(item, m, q) {
						err = failure("invalid_response")
					}
				}
			}
			if len(seen)+len(missing) != len(ids) {
				err = failure("invalid_response")
			}
			if err == nil {
				for _, m := range batch.Items {
					p.Details[m.ID] = m
				}
				p.BatchRead = true
				response = batch
			}
		} else if ErrorCode(err) == "details_unavailable" && len(ids) <= 3 {
			// Only a small failed batch may fall back to individual reads, in
			// later scheduling steps. Other failures retain the batch for retry.
			p.BatchRead, err = true, nil
		}
		s.Search.Metrics.Batches++
	default:
		item := p.Items[p.Done]
		m, hasDetail := p.Details[item.ID]
		created, _ := strconv.ParseInt(m.Created, 10, 64)
		chatID := item.MetaData.ChatID
		chat, exists := s.Chats[chatID]
		_, previouslyAccepted := s.Accepted[MessageKey(s.Binding, m)]
		switch {
		case !hasDetail:
			missing := len(p.Items) - p.Done - len(p.Details)
			if missing > 3 {
				p.BatchRead = false
				err = failure("details_unavailable")
				break
			}
			called = true
			m, err = reader.Message(queryCtx, s.Binding.Account, item.ID)
			if err == nil && !searchDetailMatches(item, m, q) {
				err = failure("invalid_response")
			}
			if err == nil {
				p.Details[item.ID], response = m, m
			}
			s.Search.Metrics.Singles++
		case time.UnixMilli(created).After(q.End):
			// Search metadata truncates milliseconds and can include the rest
			// of End's second. Do not admit or observe it yet: the next overlap
			// must be able to rediscover this message inside its exact window.
			delete(p.Details, item.ID)
			p.Done++
			p.MemberToken = ""
			s.Search.Metrics.Excluded++
		case !previouslyAccepted && q.ChatType == "p2p" && (m.Sender.Type != "user" || m.Sender.IDType != "open_id"):
			// These native messages cannot match the subscription under any
			// relationship. In particular, an app P2P hit must not block later
			// personnel messages while trying to prove a personnel relationship.
			// User messages still require the full independent relationship proof.
			delete(p.Details, item.ID)
			p.Done++
			p.MemberToken = ""
			s.Search.Metrics.Excluded++
		case !exists || !chat.Chat.Valid() || chat.Chat.Mode == "p2p" && (chat.NeedsVerification || !chat.Chat.Relation.ValidFor(s.Binding.Account, chatID) || chat.VerifiedAt.IsZero()) || !chat.VerifiedAt.IsZero() && !now.Before(chat.VerifiedAt.Add(DirectoryInterval)):
			if !exists && len(s.Chats) >= MaxChats {
				err = notification.ErrCapacity
				break
			}
			called = true
			var metadata Chat
			metadata, err = notificationChat(queryCtx, reader, s.Binding.Account, chatID)
			if err == nil && (!metadata.Valid() || metadata.ID != chatID || exists && chat.Chat.TenantKey != "" && chat.Chat.TenantKey != metadata.TenantKey || (metadata.Mode == "p2p") != (q.ChatType == "p2p")) {
				err = failure("conversation_type_mismatch")
			}
			if err == nil {
				chat.Chat, chat.LastAttempt = metadata, now
				chat.NeedsVerification = metadata.Mode != "p2p"
				chat.VerifiedAt = time.Time{}
				if !chat.NeedsVerification {
					chat.VerifiedAt = now
				}
				s.Chats[chatID] = chat
				p.MemberToken = ""
				response = metadata
			}
			s.Search.Metrics.Verifications++
		case chat.NeedsVerification || !chat.Chat.Relation.ValidFor(s.Binding.Account, chatID):
			called = true
			var membership MembershipPage
			membership, err = reader.GroupMembership(queryCtx, s.Binding.Account, chatID, p.MemberToken)
			if err == nil && (!validPageToken(membership.PageToken) || membership.HasMore && (membership.PageToken == "" || membership.PageToken == p.MemberToken)) {
				err = failure("invalid_response")
			}
			if err == nil {
				if membership.Joined {
					chat.Chat.Relation = ConversationRelation{Account: s.Binding.Account, ChatID: chatID, Kind: "group"}
					chat.NeedsVerification, chat.VerifiedAt = false, now
					s.Chats[chatID] = chat
					p.MemberToken = ""
				} else if membership.HasMore {
					p.MemberToken = membership.PageToken
				} else {
					p.MemberToken = ""
					err = failure("conversation_unverified")
				}
				response = membership
			}
			s.Search.Metrics.Verifications++
		default:
			ms, _ := strconv.ParseInt(m.Created, 10, 64)
			floor := max(s.Baseline.Unix(), chat.Completed-searchOverlap)
			before := s.Metrics.NewItems
			if ms >= s.Baseline.UnixMilli() && ms/1000 >= floor && s.Config.Includes(chat.Chat) && (chat.Chat.Mode == "p2p") == (q.ChatType == "p2p") && (q.FromID == "" || m.Sender.Type == "user" && m.Sender.ID == q.FromID) {
				s.acceptMessage(m, chat)
			}
			s = s.Expire(limits.ExpireBefore).(State)
			// Expire may clone: refresh branch/page pointers before advancing.
			b = &s.Search.Branches[i]
			p = b.Scan.Page
			if len(s.Inbox) > limits.Inbox || len(s.Observed) > limits.Observed {
				return r.failed(state.(State), i, notification.ErrCapacity, now)
			}
			if s.Metrics.NewItems > before {
				s.Search.Metrics.Accepted++
			} else {
				s.Search.Metrics.Excluded++
			}
			chat.LastActivity = max(chat.LastActivity, ms)
			s.Chats[chatID] = chat
			delete(p.Details, item.ID)
			p.Done++
			p.MemberToken = ""
		}
	}
	if ctx.Err() != nil {
		return time.Second, true // leave the last committed boundary intact
	}
	if called {
		r.pages++
		s.Metrics.Calls++
	}
	if err != nil {
		if ErrorCode(err) == "cursor_expired" {
			if b.Scan.Page == nil {
				b.Scan.Token = "" // same frozen window, retained dedup
				b.Scan.Tokens = map[string]bool{}
			} else {
				b.Scan.Page.MemberToken = ""
			}
		}
		return r.failed(s, i, err, now)
	}
	if response != nil {
		raw, _ := json.Marshal(response)
		r.bytes += int64(len(raw))
		if int64(len(raw)) > limits.SliceBytes {
			return r.failed(state.(State), i, failure("output_limit"), now)
		}
		s.Metrics.ResponseBytes += int64(len(raw))
		s.LastCheck = now
	}
	b.Failures, b.ErrorCode = 0, ""
	if !windowFinished {
		b.RetryAt = time.Time{}
	}
	s.Status, s.ErrorCode = "running", ""
	return r.commit(s)
}

func (s *State) completeSearchChats() {
	for id, p := range s.Chats {
		if !s.Config.Includes(p.Chat) {
			continue
		}
		completed := int64(0)
		for _, b := range s.Search.Branches {
			if (p.Chat.Mode == "p2p") != (b.Kind == "private") {
				continue
			}
			if b.Completed == 0 {
				completed = 0
				break
			}
			if completed == 0 || b.Completed < completed {
				completed = b.Completed
			}
		}
		if completed > p.Completed {
			p.Completed, p.Scan = completed, nil
			s.Chats[id] = p
		}
	}
}
