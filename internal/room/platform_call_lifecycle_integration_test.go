//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestPlatformCallRevocationOAuthCallbackWaitsForEveryCall(t *testing.T) {
	parallelFixtureTest(t)
	g := newGitlabLoginHarness(t)
	m := g.h.m
	v, cookie := g.login(t)
	g.callback(v, cookie, nil)
	first := g.status(t, v, cookie)
	if first.Status != "succeeded" || len(first.Platforms) != 1 {
		t.Fatal("initial login failed", first)
	}
	c := first.Platforms[0]
	g.req.Version = c.Version
	g.req.Affected[0].Version = c.Version
	v, cookie = g.login(t)
	var calls []*platformCall
	var contexts []context.Context
	for range 2 { // Action and reference owners use the same platform predicate.
		ctx, cancel := context.WithCancel(t.Context())
		call := &platformCall{platformID: c.ID}
		m.mu.Lock()
		call.Call = m.platformCalls.Register(call, cancel)
		m.mu.Unlock()
		calls, contexts = append(calls, call), append(contexts, ctx)
		t.Cleanup(func() { m.finishPlatformCall(call) })
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- g.callback(v, cookie, nil) }()
	for _, ctx := range contexts {
		awaitEventSignal(t, ctx.Done(), "OAuth callback did not cancel old grant work")
	}
	m.mu.Lock()
	status, pending := m.gitlabAuthSessions[v.SessionID].Status, m.platformCalls.Len()
	m.mu.Unlock()
	if status != "settling" || pending != len(calls) {
		t.Fatal("OAuth callback skipped settlement", status, pending)
	}
	m.finishPlatformCall(calls[0])
	select {
	case <-done:
		t.Fatal("OAuth callback returned with an unsettled call")
	case <-time.After(20 * time.Millisecond):
	}
	m.finishPlatformCall(calls[1])
	select {
	case response := <-done:
		if response.Code != 303 {
			t.Fatal("OAuth callback failed", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth callback did not settle")
	}
	result := g.status(t, v, cookie)
	if result.Status != "succeeded" || len(result.Platforms) != 1 || result.Platforms[0].CredentialVersion != c.CredentialVersion+1 || g.posts.Load() != 2 {
		t.Fatal("reauthorization did not rotate the grant exactly once", result, g.posts.Load())
	}
	assertActionCallsSettled(t, m)
}

func TestPlatformCallRevocationPreservesUnrelatedScopes(t *testing.T) {
	for _, change := range []string{"binding", "member", "credential", "room"} {
		t.Run(change, func(t *testing.T) {
			parallelFixtureTest(t)
			m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
			member := members[0]
			bindCLITool(t, m, room.RoomIdentity, member, c, "gitlab", 0)
			id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			// Only metadata and receipts are synthetic. Exercise the real owner
			// predicates with private production state, without another HTTP model.
			type probe struct {
				name string
				call *platformCall
				ctx  context.Context
				want bool
			}
			var probes []probe
			for _, field := range []string{"target", "room", "instance", "agent", "membership", "platform", "tool"} {
				call := &platformCall{owner: room.RoomIdentity, agentID: id.AgentID, membershipID: id.MembershipID, platformID: c.ID, tool: "gitlab"}
				switch field {
				case "room":
					call.owner.RoomID += "-other"
				case "instance":
					call.owner.RoomInstance += "-other"
				case "agent":
					call.agentID = members[1].AgentID
				case "membership":
					call.membershipID = members[1].MembershipID
				case "platform":
					call.platformID += "-other"
				case "tool":
					call.tool = "local"
				}
				want := field == "target"
				switch change {
				case "member":
					want = field == "target" || field == "platform" || field == "tool"
				case "credential":
					want = field != "platform"
				case "room":
					want = field != "room" && field != "instance"
				}
				ctx, cancel := context.WithCancel(t.Context())
				m.mu.Lock()
				call.Call = m.platformCalls.Register(call, cancel)
				m.mu.Unlock()
				probes = append(probes, probe{field, call, ctx, want})
				t.Cleanup(func() { m.finishPlatformCall(call) })
			}
			done := make(chan error, 1)
			go func() {
				var err error
				switch change {
				case "binding":
					r := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, ExpectedBindingRevision: new(uint64(1)), Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "gitlab", PlatformID: c.ID}})
					if r.Code != 200 {
						err = errors.New(r.Body.String())
					}
				case "member":
					var release func()
					release, err = m.beginPlatformMemberChange(t.Context(), svc, id)
					if release != nil {
						release()
					}
				case "credential":
					_, err = testReauthorizePlatform(m, managedPlatformRequest{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{room.RoomIdentity}})
				case "room":
					err = m.shutdownRuntime(t.Context(), room.RoomIdentity, svc, nil)
				}
				done <- err
			}()
			awaitEventSignal(t, probes[0].ctx.Done(), "owner did not cancel target")
			// Synchronize with the complete cancellation predicate pass. Every
			// selected receipt must still be open until its work explicitly finishes.
			m.mu.Lock()
			pending := m.platformCalls.Len()
			m.mu.Unlock()
			if pending != len(probes) {
				t.Fatal("cancellation retired unsettled work", pending)
			}
			for _, p := range probes {
				if (p.ctx.Err() != nil) != p.want {
					t.Fatalf("%s: canceled=%v, want %v", p.name, p.ctx.Err(), p.want)
				}
				select {
				case <-p.call.Done():
					t.Fatal("owner completed a receipt it does not own", p.name)
				default:
				}
			}
			if change == "member" {
				m.mu.Lock()
				err := m.checkToolMemberLocked(t.Context(), toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
				m.mu.Unlock()
				if !errors.Is(err, ErrBusy) {
					t.Fatal("member admitted work during settlement", err)
				}
			}
			for _, p := range probes[1:] {
				if p.want {
					m.finishPlatformCall(p.call)
				}
			}
			// Hold just the exact target after the other selected work settles.
			// This bounded window supplements the canceled-context/receipt barriers.
			select {
			case err := <-done:
				t.Fatal("owner returned before target settlement", err)
			case <-time.After(20 * time.Millisecond):
			}
			m.finishPlatformCall(probes[0].call)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owner did not join settled work")
			}
			for _, p := range probes {
				if !p.want && p.ctx.Err() != nil {
					t.Fatal("unrelated work canceled after settlement", p.name)
				}
				m.finishPlatformCall(p.call)
			}
			assertActionCallsSettled(t, m)
		})
	}
}
