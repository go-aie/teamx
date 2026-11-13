package room

import (
	"context"
	"testing"
	"time"
)

// Real model controller, rotation, original connection store and Inbox; model
// execution and remote providers are controlled fixtures and perform no writes.
func TestPlatformAuthorizationSurvivesModelRotationAndPause(t *testing.T) {
	for _, provider := range []string{"gitlab", "lark"} {
		for _, scenario := range []string{"enabled", "paused", "cancelled", "replaced"} {
			pause := scenario == "paused"
			for _, settleDuringTurn := range []bool{false, true} {
				if (scenario == "cancelled" || scenario == "replaced") && settleDuringTurn {
					continue
				}
				if scenario == "cancelled" && provider == "lark" {
					continue
				}
				name := provider + "/" + scenario
				if settleDuringTurn {
					name += "/result-during-turn"
				} else {
					name += "/result-after-rotation"
				}
				t.Run(name, func(t *testing.T) {
					m, c, config := onlineModelManager(t)
					g := newGitlabLoginHarness(t)
					m.config.endpoint = g.h.m.config.endpoint
					file, err := g.h.m.platforms.Load()
					if err != nil {
						t.Fatal(err)
					}
					file.Platforms = nil
					if err = m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
						t.Fatal(err)
					}
					m.platformHTTP = g.h.m.platformHTTP
					g.h.m = m
					entered, finish := make(chan MemberIdentity, 4), make(chan struct{}, 4)
					m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
						entered <- id
						select {
						case <-finish:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}, nil)
					var room roomSummary
					if scenario == "replaced" {
						m.teams["takeout-dinner"] = teamDefinition{ID: "takeout-dinner", Name: "Fixture", OpeningMessage: "Fixture opening", Runtime: config.Runtime, Members: []teamMember{{ID: "assistant", Name: "Assistant", Instructions: "Fixture assistant"}}}
						room = managerCreate(t, m, "authorization")
					} else {
						room = createDirectConversation(t, m, directSelection("authorization"))
					}
					id := modelMember(t, m, room)
					svc := openRoomOK(t, m, room, "open-authorization")
					managedPost(t, m, svc, "connect-source")
					select {
					case got := <-entered:
						if got != id {
							t.Fatal(got)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("no current turn")
					}
					operationID, _ := newOpaqueID()
					req := platformConnectRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), OperationID: operationID, SourceSeq: 1, Input: &platformConnectInput{Provider: provider, Name: "Work"}}
					var settle func()
					if provider == "gitlab" {
						req.Input.Origin = g.h.server.URL
						req.Input.ApplicationID = g.req.ApplicationID
						req.Input.AccountUsername = "tester"
						op := connectHTTP(t, m, "start", req)
						auth, cookie := authorizeGitlabConnect(t, g, req, op)
						settle = func() {
							if w := g.callback(auth, cookie, nil); w.Code != 303 && !(scenario == "cancelled" && w.Code == 403) {
								t.Fatal(w.Code, w.Body.String())
							}
						}
					} else {
						_, auth := connectPending(t, m, svc, req)
						settle = func() { connectSucceed(t, m, auth) }
					}
					observe := connectObservation(req)
					mode := "inbox"
					observe.Notify = &mode
					connectHTTP(t, m, "notify", observe)
					change := modelChangeInput{RequestID: "model-during-authorization", Kind: "default", EntryID: "B", ExpectedVersion: c.service.snapshot().Version}
					if _, err = c.submit(change, humanOperator()); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					member := svc.target(id.MembershipID)
					waitMember(t, ctx, member, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "waiting" })
					pending := connectHTTP(t, m, "status", connectObservation(req))
					if connectTerminal(pending.Status) || pending.MemberIdentity != id {
						t.Fatal("model target changed authorization", pending)
					}
					if pause {
						if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "cancelled" {
						connectHTTP(t, m, "cancel", connectObservation(req))
					}
					if settleDuringTurn {
						settle()
					}
					finish <- struct{}{}
					waitMember(t, ctx, member, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "completed" })
					// Query the exact original operation after the new Session exists. Completion
					// must neither create a new connection nor require another human message.
					pending = connectHTTP(t, m, "status", connectObservation(req))
					if pending.MemberIdentity != id || (connectTerminal(pending.Status) != (settleDuringTurn || scenario == "cancelled")) {
						t.Fatal("rotation replaced original authorization", pending)
					}
					if scenario == "replaced" {
						snapshot, err := svc.store.LoadRoom()
						if err != nil {
							t.Fatal(err)
						}
						runner := snapshot.Members[0].Runner
						if _, err = svc.room.Call[MemberInfo](ctx, RemoveMember{id}); err != nil {
							t.Fatal(err)
						}
						replacement := id
						replacement.MembershipID = "replacement-membership"
						sink, err := svc.rt.Spawn(notificationSink{})
						if err != nil {
							t.Fatal(err)
						}
						if _, err = svc.room.Call[MemberInfo](ctx, AddMember{MemberIdentity: replacement, Runner: runner, Target: sink}); err != nil {
							t.Fatal(err)
						}
						settle()
						m.mu.Lock()
						original, err := m.readPlatformConnectLocked(req.OperationID)
						m.mu.Unlock()
						if err != nil || original.MemberIdentity != id || original.Status == "completed" || original.Connection.Status == "connected" {
							t.Fatal("replacement adopted authorization", original, err)
						}
						stolen := connectObservation(req)
						stolen.Caller.MembershipID = replacement.MembershipID
						if w := managerHTTP(m, "/v1/platforms/connect/status", stolen); w.Code == 200 {
							t.Fatal("replacement queried old operation")
						}
						records, err := m.platforms.Load()
						if err != nil || len(records.Platforms) != 0 {
							t.Fatal("late authorization saved replacement connection", err)
						}
						return
					}
					if !settleDuringTurn {
						settle()
					}
					done := connectHTTP(t, m, "status", connectObservation(req))
					expected := "completed"
					if scenario == "cancelled" {
						expected = "cancelled"
					}
					if done.Status != expected || done.MemberIdentity != id || done.ResultVersion != 1 {
						t.Fatal(done)
					}
					waitConnectDelivery(t, m, done.ID)
					if pause {
						status, err := member.Call[MemberStatus](ctx, InspectMember{id})
						if err != nil || !status.Paused || status.Active {
							t.Fatal("authorization revived paused member", status, err)
						}
						select {
						case <-entered:
							t.Fatal("paused member resumed")
						default:
						}
						if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
							t.Fatal(err)
						}
					}
					select {
					case got := <-entered:
						if got != id {
							t.Fatal("Inbox rebound member", got)
						}
					case <-ctx.Done():
						t.Fatal("no Inbox continuation after rotation")
					}
					page := consumeConnectTestInbox(t, svc, id, 2)
					if len(page.Entries) != 1 || page.Entries[0].TaskEvent.OperationID != done.ID {
						t.Fatal("wrong operation delivered", page)
					}
					finish <- struct{}{}
					waitMember(t, ctx, member, id, func(s MemberStatus) bool { return !s.Active && s.Inbox != nil && s.Inbox.ConsumedThrough == 1 })
					snapshot, err := svc.store.LoadRoom()
					if err != nil || len(snapshot.Messages) != 1 {
						t.Fatal("continuation needed new chat", err)
					}
					exchanges := int32(1)
					if scenario == "cancelled" {
						exchanges = 0
					}
					if provider == "gitlab" && g.posts.Load() != exchanges {
						t.Fatal("OAuth exchange replayed")
					}
				})
			}
		}
	}
}
