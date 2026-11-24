package runtime

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

type displayTestDriver struct{ *fakeDriver }

func (d *displayTestDriver) OpenView(ctx context.Context, _ Resource) (io.ReadWriteCloser, error) {
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		select {
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
	}()
	return a, nil
}
func (d *displayTestDriver) ViewAsset(context.Context, string) ([]byte, error) {
	return []byte("native viewer"), nil
}
func displaySetup(t *testing.T) (*Engine, *fakeDriver, computer.Binding, computer.DisplayScreen, computer.Contract) {
	t.Helper()
	c := catalog(t, "test", "act", "see")
	d := &fakeDriver{catalog: c}
	e, err := Open(t.Context(), t.TempDir(), 4, c, &displayTestDriver{d})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	b := claim(t, e, "service-a")
	return e, d, b, ensure(t, e, b).(computer.DisplayScreen), c
}
func TestHumanControlIsPerScreenAndWaitsForRemoteExecution(t *testing.T) {
	e, d, b, s, c := displaySetup(t)
	other := b
	other.Member.AgentID = "other"
	other.Member.MembershipID = "other-member"
	second := ensure(t, e, other)
	d.started = make(chan struct{}, 1)
	d.release = make(chan struct{})
	running := make(chan error, 1)
	go func() { _, err := s.Call(t.Context(), call(c, "act", "first")); running <- err }()
	<-d.started
	req := computer.ControlRequest{Action: "hold", ID: strings.Repeat("a", 32), Client: strings.Repeat("b", 32)}
	if state, err := s.Control(t.Context(), req); err != nil || state.Phase != "waiting" {
		t.Fatal(state, err)
	}
	req.Action = "activate"
	if _, err := s.Control(t.Context(), req); !computer.Is(err, computer.Busy) {
		t.Fatal("admitted active remote execution", err)
	}
	close(d.release)
	if err := <-running; err != nil {
		t.Fatal(err)
	}
	d.started = nil
	d.release = nil
	if _, err := s.Call(t.Context(), call(c, "see", "late")); !computer.Is(err, computer.HumanControl) {
		t.Fatal("late agent call admitted", err)
	}
	if _, err := second.Call(t.Context(), call(c, "act", "parallel")); err != nil {
		t.Fatal("other screen blocked", err)
	}
	if state, err := s.Control(t.Context(), req); err != nil || state.Phase != "ready" {
		t.Fatal(state, err)
	}
	rival := req
	rival.Action = "hold"
	rival.ID = strings.Repeat("c", 32)
	rival.Client = strings.Repeat("d", 32)
	if _, err := s.Control(t.Context(), rival); !computer.Is(err, computer.HumanControl) {
		t.Fatal("second page admitted", err)
	}
	if _, err := s.View(t.Context(), computer.ViewRequest{ID: req.ID, Client: rival.Client}); err == nil {
		t.Fatal("forged client admitted")
	}
	stream, err := s.View(t.Context(), computer.ViewRequest{ID: req.ID, Client: req.Client})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	req.Action = "release"
	if _, err = s.Control(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control(t.Context(), req); err != nil {
		t.Fatal("release not idempotent", err)
	}
	if _, err = s.View(t.Context(), computer.ViewRequest{ID: req.ID, Client: req.Client}); err == nil {
		t.Fatal("late input crossed release")
	}
	if _, err = s.Call(t.Context(), call(c, "act", "after")); err != nil {
		t.Fatal("input did not settle", err)
	}
}
func TestHumanLeaseExpiryAndServiceHandoffFenceStreams(t *testing.T) {
	e, _, _, s, c := displaySetup(t)
	req := computer.ControlRequest{Action: "hold", ID: strings.Repeat("a", 32), Client: strings.Repeat("b", 32)}
	_, _ = s.Control(t.Context(), req)
	req.Action = "activate"
	_, _ = s.Control(t.Context(), req)
	stream, err := s.View(t.Context(), computer.ViewRequest{ID: req.ID, Client: req.Client})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	e.mu.Lock()
	e.controls[s.Info().ID].expires = time.Now().Add(-time.Second)
	e.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.mu.Lock()
		n := len(e.views[s.Info().ID])
		e.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expiry did not join input")
		}
		time.Sleep(time.Millisecond)
	}
	req.Action = "renew"
	state, err := s.Control(t.Context(), req)
	if err != nil || state.Phase != "expired" {
		t.Fatal(state, err)
	}
	req.Action = "activate"
	if _, err = s.Control(t.Context(), req); err == nil {
		t.Fatal("expired lease resurrected")
	}
	if _, err = s.Call(t.Context(), call(c, "see", "post-expiry")); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.View(t.Context(), computer.ViewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	_ = claim(t, e, "service-b")
	eof := make(chan error, 1)
	go func() { _, err := viewer.Read(make([]byte, 1)); eof <- err }()
	select {
	case err := <-eof:
		if err == nil {
			t.Fatal("old service viewer remained usable")
		}
	case <-time.After(time.Second):
		t.Fatal("old service stream was not revoked")
	}
}

func rfbServerHandshake(conn io.ReadWriter) error {
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	if _, err := rfbRead(conn, 12); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{1, 1}); err != nil {
		return err
	}
	if _, err := rfbRead(conn, 1); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return err
	}
	if _, err := rfbRead(conn, 1); err != nil {
		return err
	}
	header := make([]byte, 24)
	binary.BigEndian.PutUint16(header, 1280)
	binary.BigEndian.PutUint16(header[2:], 900)
	_, err := conn.Write(header)
	return err
}
func rfbClientHandshake(t *testing.T, conn io.ReadWriter) {
	t.Helper()
	if _, err := rfbRead(conn, 12); err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("RFB 003.008\n"))
	_, _ = rfbRead(conn, 2)
	_, _ = conn.Write([]byte{1})
	_, _ = rfbRead(conn, 4)
	_, _ = conn.Write([]byte{1})
	if _, err := rfbRead(conn, 24); err != nil {
		t.Fatal(err)
	}
}
func TestReadOnlyRFBCannotInjectInputAcrossFragments(t *testing.T) {
	for _, kind := range []byte{4, 5, 6, 251, 255} {
		t.Run(string(rune(kind)), func(t *testing.T) {
			client, local := net.Pipe()
			remote, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			got := make(chan []byte, 1)
			go func() {
				if rfbServerHandshake(server) != nil {
					got <- nil
					return
				}
				b, _ := io.ReadAll(server)
				got <- b
			}()
			proxyDone := make(chan error, 1)
			go func() {
				defer local.Close()
				defer remote.Close()
				proxyDone <- proxyRFB(t.Context(), local, remote, false)
			}()
			rfbClientHandshake(t, client)
			// This valid framebuffer request must still pass, one byte at a time.
			request := []byte{3, 0, 0, 0, 0, 0, 0, 1, 0, 1}
			for _, b := range request {
				if _, err := client.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = client.Write([]byte{kind})
			if err := <-proxyDone; err == nil {
				t.Fatal("read-only input accepted")
			}
			if b := <-got; string(b) != string(request) {
				t.Fatalf("input reached upstream: %x", b)
			}
		})
	}
}
func TestInteractiveRFBForwardsKeyboardPointerAndClipboard(t *testing.T) {
	client, local := net.Pipe()
	remote, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	messages := []byte{4, 1, 0, 0, 0, 0, 0, 65, 5, 1, 0, 10, 0, 20, 6, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'}
	got := make(chan []byte, 1)
	go func() {
		_ = rfbServerHandshake(server)
		b, _ := rfbRead(server, len(messages))
		got <- b
		server.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- proxyRFB(t.Context(), local, remote, true) }()
	rfbClientHandshake(t, client)
	if _, err := client.Write(messages); err != nil {
		t.Fatal(err)
	}
	if b := <-got; string(b) != string(messages) {
		t.Fatalf("native input changed: %x", b)
	}
	<-done
}

func TestHumanInputReleaseRequiresUpstreamFenceAcknowledgement(t *testing.T) {
	for _, ack := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "missing"}[ack], func(t *testing.T) {
			client, local := net.Pipe()
			remote, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			received := make(chan struct{})
			allowAck := make(chan struct{})
			go func() {
				defer server.Close()
				if rfbServerHandshake(server) != nil {
					return
				}
				// The fence follows all input messages on the same RFB stream.
				key, err := rfbRead(server, 8)
				if err != nil || key[0] != 4 {
					return
				}
				fence, err := rfbRead(server, 9)
				if err != nil || fence[0] != 248 {
					return
				}
				payload, err := rfbRead(server, int(fence[8]))
				if err != nil {
					return
				}
				close(received)
				<-allowAck
				if ack {
					binary.BigEndian.PutUint32(fence[4:], 1)
					_, _ = server.Write(append(fence, payload...))
					_, _ = io.Copy(io.Discard, server)
				}
			}()
			done := make(chan error, 1)
			go func() { done <- proxyRFB(t.Context(), local, remote, true) }()
			rfbClientHandshake(t, client)
			_, _ = client.Write([]byte{4, 1, 0, 0, 0, 0, 0, 65})
			client.Close()
			<-received
			select {
			case <-done:
				t.Fatal("input released before fence acknowledgement")
			default:
			}
			close(allowAck)
			err := <-done
			if errors.Is(err, errInputFence) == ack {
				t.Fatal("incorrect input stop proof", err)
			}
		})
	}
}

type unfencedDisplayDriver struct{ *fakeDriver }

func (d *unfencedDisplayDriver) OpenView(ctx context.Context, _ Resource) (io.ReadWriteCloser, error) {
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		if rfbServerHandshake(b) != nil {
			return
		}
		_, _ = rfbRead(b, 8)
		_, _ = rfbRead(b, 9) /* peer exits without acknowledging the fence */
	}()
	return a, nil
}
func (d *unfencedDisplayDriver) ViewAsset(context.Context, string) ([]byte, error) { return nil, nil }
func TestUnconfirmedHumanInputExitBlocksUntilVerifiedStop(t *testing.T) {
	c := catalog(t, "test", "act", "see")
	driver := &fakeDriver{catalog: c}
	e, err := Open(t.Context(), t.TempDir(), 2, c, &unfencedDisplayDriver{driver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	b := claim(t, e, "service-a")
	s := ensure(t, e, b).(computer.DisplayScreen)
	req := computer.ControlRequest{ID: strings.Repeat("a", 32), Client: strings.Repeat("b", 32), Action: "hold"}
	_, _ = s.Control(t.Context(), req)
	req.Action = "activate"
	_, _ = s.Control(t.Context(), req)
	stream, err := s.View(t.Context(), computer.ViewRequest{ID: req.ID, Client: req.Client})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	rfbClientHandshake(t, stream)
	_, _ = stream.Write([]byte{4, 1, 0, 0, 0, 0, 0, 65})
	req.Action = "release"
	if _, err = s.Control(t.Context(), req); !computer.Is(err, computer.Unknown) {
		t.Fatal("missing input fence was reported settled", err)
	}
	if _, err = s.Call(t.Context(), call(c, "act", "after-unconfirmed-input")); err == nil {
		t.Fatal("Agent acted without a stop proof")
	}
	if err = s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control(t.Context(), req); err != nil {
		t.Fatal("verified stop did not settle input", err)
	}
	restored := ensure(t, e, b)
	if _, err = restored.Call(t.Context(), call(c, "see", "observe-after-stop")); err != nil {
		t.Fatal(err)
	}
}

func TestHumanControlGenerationFencesLateHoldAndRelease(t *testing.T) {
	_, _, _, s, _ := displaySetup(t)
	first := computer.ControlRequest{Sequence: 1, Action: "release", ID: strings.Repeat("a", 32), Client: strings.Repeat("b", 32)}
	if state, err := s.Control(t.Context(), first); err != nil || state.Phase != "released" {
		t.Fatal(state, err)
	}
	first.Action = "hold"
	if state, err := s.Control(t.Context(), first); err != nil || state.Phase != "released" {
		t.Fatal("late hold resurrected released control", state, err)
	}
	second := computer.ControlRequest{Sequence: 2, Action: "hold", ID: strings.Repeat("c", 32), Client: strings.Repeat("d", 32)}
	if state, err := s.Control(t.Context(), second); err != nil || state.Phase != "waiting" {
		t.Fatal(state, err)
	}
	for _, action := range []string{"hold", "activate", "renew", "release"} {
		first.Action = action
		if _, err := s.Control(t.Context(), first); !computer.Is(err, computer.Stale) {
			t.Fatal("old control generation changed new ownership", action, err)
		}
	}
	second.Action = "activate"
	if state, err := s.Control(t.Context(), second); err != nil || state.Phase != "ready" {
		t.Fatal("new control lost ownership", state, err)
	}
}

func TestReopenedRoomUsesNewControlContextAndRejectsPriorContext(t *testing.T) {
	_, _, _, s, _ := displaySetup(t)
	old := computer.ControlRequest{Context: "room-service-before-close", Sequence: 7, Action: "hold", ID: strings.Repeat("a", 32), Client: strings.Repeat("b", 32)}
	if _, err := s.Control(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	old.Action = "release"
	if _, err := s.Control(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	current := computer.ControlRequest{Context: "reopened-room-service", Sequence: 1, Action: "hold", ID: strings.Repeat("c", 32), Client: strings.Repeat("d", 32)}
	if state, err := s.Control(t.Context(), current); err != nil || state.Phase != "waiting" {
		t.Fatal("new Actor counter could not reset in new context", state, err)
	}
	current.Action = "release"
	if _, err := s.Control(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	old.Action = "hold"
	old.Sequence = 100
	if _, err := s.Control(t.Context(), old); !computer.Is(err, computer.Stale) {
		t.Fatal("old Room context reacquired input", err)
	}
}
