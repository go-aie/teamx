package room

import (
	"context"
	"errors"
	"strings"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/mark3labs/mcp-go/mcp"
)

type inputCallKey struct{}
type inputSessionKey struct{}

func (s *inputSession) readStream(ctx context.Context, call string, data []byte) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
	q, err := inputstream.DecodeStreamRead(data)
	if err != nil {
		return nil, nil, nil, err
	}
	if call == "" {
		return nil, nil, nil, ErrIdentity
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = s.checkStreamAdmission(ctx, q.Stream); err != nil {
		return nil, nil, release, err
	}
	var request inputstream.ReadRequest
	if err = s.mutate(func(r *inputstream.Repository) error {
		var err error
		request, err = r.BindCall(s.execution, call, q.Stream, q.Limit)
		return err
	}); err != nil {
		return nil, nil, release, err
	}
	result, _, err := s.readPage(ctx, request, s.renderInputPage)
	if err != nil {
		return nil, nil, release, err
	}
	// A socket/stdout flush only hands off output. Native consumption waits
	// for the framework's proof of the durable Session result.
	return result, func(context.Context) error { return nil }, release, nil
}

// Admission is a local prerequisite. Saved readable bodies never require a
// live token, remote request or enabled platform tool to enter formal delivery.
func (s *inputSession) checkStreamAdmission(ctx context.Context, stream string) error {
	if s.unified && stream == roomStreamRef(s.storage.id) {
		return nil
	}
	p := s.storage
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	locations, err := p.repository.PendingStream(s.execution, stream)
	if err != nil {
		return err
	}
	for _, l := range locations {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := p.store.businessInputLocked(p.id, 0, &l)
		if err != nil {
			return err
		}
		ad, err := p.store.inboxAdmissionLocked(entry)
		if err != nil {
			return err
		}
		if !ad.Verified {
			return errors.New("Saved input is temporarily unavailable; retry this stream later")
		}
		if ad.Decision == notification.AdmissionExcluded {
			if err = p.result(p.repository.Skip(s.execution, l, "admission_excluded:"+ad.ReasonCode)); err != nil {
				return err
			}
		}
	}
	return nil
}

// receiveCall replays and settles the exact page before recording the separate
// framework receipt. A lost native/coordinator acknowledgment is retried here.
func (s *inputSession) receiveCall(ctx context.Context, call string) error {
	state, err := s.storage.repository.State(s.execution)
	if err != nil {
		return err
	}
	binding, ok := state.Calls[call]
	if !ok {
		return inputstream.ErrExpired
	}
	for _, attempt := range state.Attempts {
		if attempt.Request == binding.Request && attempt.Received {
			return nil
		}
	}
	release, err := s.acquire(ctx)
	if release != nil {
		defer release()
	}
	if err != nil {
		return err
	}
	_, confirm, err := s.readPage(ctx, binding.Request, s.renderInputPage)
	if err != nil {
		return err
	}
	if err = confirm(ctx); err != nil {
		return err
	}
	return s.mutate(func(r *inputstream.Repository) error { return r.Receive(s.execution, call) })
}

func (s *inputSession) rememberDirectory(v any) {
	if d, ok := v.(inputstream.Directory); ok {
		s.knownMu.Lock()
		defer s.knownMu.Unlock()
		if s.known == nil {
			s.known = map[string]bool{}
		}
		for _, stream := range d.Streams {
			s.known[stream.Ref] = !strings.Contains(stream.Hint, "标签已截断")
		}
	}
}
