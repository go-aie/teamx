package runtime

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"path"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

const controlLifetime = 20 * time.Second

type humanControl struct {
	computer.ControlState
	expires time.Time
	retired map[string]bool
}
type viewStream struct {
	cancel context.CancelFunc
	done   chan struct{}
	input  bool
}

// Called with metadata locked. Expiry revokes input immediately; registered
// streams keep admission fenced until their parsers and upstream writers exit.
func (e *Engine) humanHeld(id string) bool {
	h := e.controls[id]
	if h != nil && time.Now().After(h.expires) && h.Phase != "released" {
		h.Phase = "expired"
		e.cancelViews(id, true)
	}
	if h != nil && (h.Phase == "waiting" || h.Phase == "ready" || h.Phase == "closing") {
		return true
	}
	for v := range e.views[id] {
		if v.input {
			return true
		}
	}
	return false
}
func (e *Engine) cancelViews(id string, inputOnly bool) []*viewStream {
	var streams []*viewStream
	for v := range e.views[id] {
		if !inputOnly || v.input {
			v.cancel()
			streams = append(streams, v)
		}
	}
	return streams
}
func joinViews(ctx context.Context, streams []*viewStream) error {
	for _, v := range streams {
		select {
		case <-v.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *screen) Control(ctx context.Context, req computer.ControlRequest) (computer.ControlState, error) {
	if !boundedControlID(req.ID) || !boundedControlID(req.Client) {
		return computer.ControlState{}, computer.Fail(computer.Invalid, "bounded human control identity required")
	}
	e := s.e
	e.mu.Lock()
	r, err := s.get()
	if err != nil {
		e.mu.Unlock()
		return computer.ControlState{}, err
	}
	id := r.Resource.ID
	_ = e.humanHeld(id)
	h := e.controls[id]
	if len(req.Context) > 256 {
		e.mu.Unlock()
		return computer.ControlState{}, computer.Fail(computer.Invalid, "control context exceeds bound")
	}
	if h != nil && h.Context != req.Context {
		if h.retired[req.Context] {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.Stale, "control context retired")
		}
		if e.humanHeld(id) {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.HumanControl, "previous control context has not released input")
		}
		if req.Action != "hold" && req.Action != "release" {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.Stale, "control context changed")
		}
		if len(h.retired) >= maxOperations {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.Capacity, "control context history full")
		}
		retired := h.retired
		if retired == nil {
			retired = map[string]bool{}
		}
		retired[h.Context] = true
		phase := "waiting"
		if req.Action == "release" {
			phase = "released"
		}
		h = &humanControl{ControlState: computer.ControlState{Context: req.Context, Sequence: req.Sequence, ID: req.ID, Client: req.Client, Phase: phase}, expires: time.Now().Add(controlLifetime), retired: retired}
		e.controls[id] = h
	}
	same := h != nil && h.ID == req.ID && h.Client == req.Client && h.Sequence == req.Sequence
	if !same && h != nil && req.Sequence < h.Sequence {
		e.mu.Unlock()
		return computer.ControlState{}, computer.Fail(computer.Stale, "human control generation superseded")
	}
	if req.Action == "release" && h == nil && !e.humanHeld(id) {
		// A release may arrive before a delayed hold. Retain its generation so
		// that hold cannot subsequently resurrect input or Agent exclusion.
		h = &humanControl{ControlState: computer.ControlState{Context: req.Context, Sequence: req.Sequence, ID: req.ID, Client: req.Client, Phase: "released"}}
		e.controls[id] = h
		same = true
	}
	if req.Action == "hold" && !same {
		if e.humanHeld(id) {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.HumanControl, "another page owns this screen")
		}
		if h != nil && req.Sequence <= h.Sequence {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.Stale, "human control generation already decided")
		}
		var retired map[string]bool
		if h != nil {
			retired = h.retired
		}
		h = &humanControl{ControlState: computer.ControlState{Context: req.Context, Sequence: req.Sequence, ID: req.ID, Client: req.Client, Phase: "waiting"}, expires: time.Now().Add(controlLifetime), retired: retired}
		e.controls[id] = h
		same = true
	}
	if !same {
		e.mu.Unlock()
		return computer.ControlState{}, computer.Fail(computer.Stale, "human control lease changed")
	}
	switch req.Action {
	case "hold", "renew":
		if h.Phase == "waiting" || h.Phase == "ready" {
			h.expires = time.Now().Add(controlLifetime)
		}
	case "activate":
		if h.Phase != "waiting" && h.Phase != "ready" {
			err = computer.Fail(computer.Stale, "human control lease ended")
			break
		}
		if r.Status != "running" || e.active[id] != nil {
			err = computer.Fail(computer.Busy, "screen execution has not settled")
			break
		}
		for _, op := range r.Operations {
			if !op.Settled {
				err = computer.Fail(computer.Unknown, "verify screen stop before manual inspection")
			}
		}
		if err == nil {
			h.Phase = "ready"
			h.expires = time.Now().Add(controlLifetime)
		}
	case "release":
		h.Phase = "closing"
		streams := e.cancelViews(id, true)
		e.mu.Unlock()
		if err = joinViews(ctx, streams); err != nil {
			return computer.ControlState{}, err
		}
		e.mu.Lock()
		// No new input can register while closing. A newer lease cannot replace it.
		if r.Status == "blocked" {
			e.mu.Unlock()
			return computer.ControlState{}, computer.Fail(computer.Unknown, "human input exit needs a verified screen stop")
		}
		h.Phase = "released"
	default:
		err = computer.Fail(computer.Invalid, "unknown human control action")
	}
	out := h.ControlState
	e.mu.Unlock()
	return out, err
}
func boundedControlID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validAsset(name string) bool {
	return fs.ValidPath(name) && !strings.ContainsAny(name, "%\\\x00\r\n") && path.Ext(name) == ".js" && (strings.HasPrefix(name, "core/") || strings.HasPrefix(name, "vendor/"))
}

// Display drivers expose only a fixed local screen connection and static files.
// No caller can choose a port, command or arbitrary filesystem path.
type displayDriver interface {
	OpenView(context.Context, Resource) (io.ReadWriteCloser, error)
	ViewAsset(context.Context, string) ([]byte, error)
}

func (s *screen) ViewAsset(ctx context.Context, name string) ([]byte, error) {
	if !validAsset(name) {
		return nil, computer.Fail(computer.Invalid, "invalid viewer asset")
	}
	s.e.mu.Lock()
	_, err := s.get()
	s.e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	d, ok := s.e.driver.(displayDriver)
	if !ok {
		return nil, computer.Fail(computer.Unavailable, "provider display unavailable")
	}
	return d.ViewAsset(ctx, name)
}
func (s *screen) View(ctx context.Context, req computer.ViewRequest) (io.ReadWriteCloser, error) {
	e := s.e
	e.mu.Lock()
	r, err := s.get()
	if err == nil {
		err = e.admit(s.info.Binding)
	}
	input := req.ID != "" || req.Client != ""
	if err == nil && r.Status != "running" {
		err = computer.Fail(computer.Unavailable, "screen is not running")
	}
	if err == nil {
		_ = e.humanHeld(r.Resource.ID)
		h := e.controls[r.Resource.ID]
		if input && (h == nil || h.ID != req.ID || h.Client != req.Client || h.Phase != "ready") {
			err = computer.Fail(computer.HumanControl, "input lease is not active")
		}
		if len(e.views[r.Resource.ID]) >= 8 {
			err = computer.Fail(computer.Capacity, "screen viewer limit reached")
		}
	}
	d, ok := e.driver.(displayDriver)
	if err == nil && !ok {
		err = computer.Fail(computer.Unavailable, "provider display unavailable")
	}
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	resource := r.Resource
	streamCtx, cancel := context.WithCancel(ctx)
	v := &viewStream{cancel: cancel, done: make(chan struct{}), input: input}
	if e.views[resource.ID] == nil {
		e.views[resource.ID] = map[*viewStream]bool{}
	}
	e.views[resource.ID][v] = true
	e.mu.Unlock()
	// Registration precedes dialing. Release also joins connections still dialing.
	upstream, err := d.OpenView(streamCtx, resource)
	if err != nil {
		cancel()
		e.mu.Lock()
		delete(e.views[resource.ID], v)
		close(v.done)
		e.mu.Unlock()
		return nil, err
	}
	client, server := newViewPipe()
	go func() {
		defer func() {
			cancel()
			upstream.Close()
			server.Close()
			e.mu.Lock()
			if input {
				if h := e.controls[resource.ID]; h != nil && h.ID == req.ID && h.Client == req.Client && h.Phase == "ready" {
					h.Phase = "expired"
					e.cancelViews(resource.ID, true)
				}
			}
			delete(e.views[resource.ID], v)
			close(v.done)
			e.mu.Unlock()
		}()
		proxyDone := make(chan struct{})
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			select {
			case <-streamCtx.Done():
			case <-v.done:
			}
			server.Close()
			if !input {
				upstream.Close()
			} else {
				select {
				case <-proxyDone:
				case <-time.After(3 * time.Second):
					upstream.Close()
				}
			}
		}()
		// A monitor fences epoch, ownership, disable and expiry even without traffic.
		monitorDone := make(chan struct{})
		go func() {
			defer close(monitorDone)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-ticker.C:
					e.mu.Lock()
					current, getErr := s.get()
					if getErr == nil {
						getErr = e.admit(s.info.Binding)
					}
					allowed := getErr == nil && current.Status == "running"
					if input {
						_ = e.humanHeld(resource.ID)
						h := e.controls[resource.ID]
						allowed = allowed && h != nil && h.ID == req.ID && h.Client == req.Client && h.Phase == "ready"
					}
					e.mu.Unlock()
					if !allowed {
						cancel()
						return
					}
				}
			}
		}()
		proxyErr := proxyRFB(streamCtx, server, upstream, input)
		log.Printf("Computer display ended (input=%t): %v", input, proxyErr)
		close(proxyDone)
		if errors.Is(proxyErr, errInputFence) {
			e.mu.Lock()
			r.Status = "blocked"
			_ = e.save()
			e.mu.Unlock()
		}
		cancel()
		<-closed
		<-monitorDone
	}()
	return client, nil
}
