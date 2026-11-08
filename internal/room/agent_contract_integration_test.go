//go:build integration || e2e

package room

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentTypedActionsRestoreExactTarget(t *testing.T) {
	parallelFixtureTest(t)
	for _, discussion := range []bool{false, true} {
		for _, authority := range []string{"rule", "instruction", "human_reply"} {
			t.Run(fmt.Sprintf("discussion=%t/%s", discussion, authority), func(t *testing.T) {
				f := managedActionTest(t, discussion)
				q := agentActionFixture(f.action)
				if authority != "rule" {
					post, err := f.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, ClientMessageID: "exact-instruction", Text: "只在这个准确目标回复 exact reply"})
					if err != nil {
						t.Fatal(err)
					}
					q.Authority = agentActionAuthority{Type: "human_reply", RoomSeq: post.Seq, Reason: "当前真实用户明确指向准确目标与正文"}
					if authority == "instruction" {
						u, err := f.svc.store.saveFollowupInstruction(f.id, followupInstructionChange{followupCommand: q.followupCommand, SourceSeq: post.Seq, Intent: followupIntent{Operation: q.Operation, Body: q.Input.Body}, Basis: q.Authority.Reason})
						if err != nil {
							t.Fatal(err)
						}
						q.Revision = u.Revision
						q.Authority = agentActionAuthority{Type: "instruction", ID: u.Instructions[0].Ref, Version: 1, Reason: "落实已保存的单次指令"}
					}
				}
				server := httptest.NewServer(f.m)
				defer server.Close()
				cli := contractCLI(t, server.URL, f.svc, f.id)
				args := entryChange(t, "actions", "execute", q)
				args[2] = "--request"
				var got referenceActionReceipt
				if err := cli(args, &got); err != nil || got.Status != "succeeded" || got.Attempts != 1 || got.Result == nil {
					t.Fatal(got, err)
				}
				u, err := f.svc.store.readFollowup(f.id, q.ObjectRef, q.UnitRef)
				if err != nil || len(u.Actions) != 1 || got.TargetRef != u.TargetRef || !sameNativeTarget(got.Result.Target, u.Actions[0].Target) || got.Result.Target.Discussion.Valid() != discussion {
					t.Fatal(u, got, err)
				}
				if authority == "instruction" && u.Instructions[0].Status != "fulfilled" {
					t.Fatal("single-use instruction was not settled", u)
				}
				// Lost-response recovery uses the original observed version and evidence.
				if err := cli(args, &got); err != nil || got.Status != "succeeded" || f.posts.Load() != 1 {
					t.Fatal(got, err, f.posts.Load())
				}
				changed := q
				changed.Input.Body = "different"
				bad := entryChange(t, "actions", "execute", changed)
				bad[2] = "--request"
				if err := cli(bad, nil); err == nil || f.posts.Load() != 1 {
					t.Fatal("same ID changed body accepted", err)
				}
				assertActionCallsSettled(t, f.m)
			})
		}
	}
}

func TestAgentTypedActionUnknownRecovery(t *testing.T) {
	parallelFixtureTest(t)
	f := managedActionTest(t, true)
	managedReferenceHTTP(t, f.m, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			f.posts.Add(1)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"invalid":"remote result"}`)
			return
		}
		f.serve(w, r)
	})
	server := httptest.NewServer(f.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, f.svc, f.id)
	q := agentActionFixture(f.action)
	args := func(q agentActionRequest) []string {
		a := entryChange(t, "actions", "execute", q)
		a[2] = "--request"
		return a
	}
	var got referenceActionReceipt
	if err := cli(args(q), &got); err != nil || got.Status != "unknown" || got.Attempts != 1 {
		t.Fatal(got, err)
	}
	if err := cli(args(q), &got); err != nil || got.Status != "unknown" || f.posts.Load() != 1 {
		t.Fatal("unknown replay sent again", got, err)
	}
	q.ID = "replacement"
	q.Revision = got.UnitVersion
	if err := cli(args(q), nil); err == nil || f.posts.Load() != 1 {
		t.Fatal("unknown replaced by new ID", err)
	}
	var view agentFollowupRead
	if err := cli([]string{"matters", "read", "--object", q.ObjectRef, "--unit", q.UnitRef, "--turn", "1", "--detail"}, &view); err != nil || len(view.Unit.Actions) != 1 || view.Unit.Actions[0].Status != "unknown" || view.Detail == nil || len(view.Detail.Actions) != 1 {
		t.Fatal(view, err)
	}
}
