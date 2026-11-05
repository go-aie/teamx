package room

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
)

func gitlabReferenceFixture() (platform.SourceRef, platform.AccessContext, platform.TargetResolution) {
	pid := platform.ExternalID{Namespace: "gitlab:rest:id", Value: "123"}
	user := platform.ExternalID{Namespace: "gitlab:rest:id", Value: "13"}
	obj := platform.ObjectReference{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}, IID: platform.ExternalID{Namespace: "gitlab:rest:iid", Value: "42"}}
	note, thread := platform.ExternalID{Namespace: "gitlab:rest:id", Value: "101"}, platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "thread"}
	source := platform.SourceRef{Provider: "gitlab", Instance: "https://gitlab.example.com", Channel: "todos", SubscriptionID: "subscription", ItemKey: "item", ProjectID: 123, Project: "g/p", Object: "display", URL: "https://gitlab.example.com/g/p/-/merge_requests/42#note_101",
		Reference: platform.NotificationReference{Version: 1, RecipientID: user, Scope: "account", ProjectID: pid, Target: obj, Object: obj, NoteID: note, DiscussionID: thread, Branch: "main", Ref: "refs/heads/main", RefType: "branch", SHA: "sha", BeforeSHA: "before"}}
	access := gitlabReferenceAccess(MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "room-generation"}, AgentID: "agent", MembershipID: "membership"},
		connection.Account{ConnectionID: "connection", Generation: "generation", URL: source.Instance, UserID: 13, Username: "bot", ConnectionVersion: 2, CredentialVersion: 1}, user)
	r := platform.TargetResolution{}
	return source, access, r
}
func TestReferenceCapabilitiesWithoutGitlabOrNotification(t *testing.T) {
	scope := platform.IdentityScope{Provider: "fixture", Instance: "region:one", Tenant: platform.ExternalID{Namespace: "tenant", Value: "org-alpha"}}
	target := platform.TargetIdentity{IdentityScope: scope, Object: platform.NativeIdentity{Type: "message", ID: platform.ExternalID{Namespace: "message", Value: "msg-alpha"}}}
	access := platform.AccessContext{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "membership", Account: platform.AccountIdentity{IdentityScope: scope, ID: platform.ExternalID{Namespace: "account", Value: "user-alpha"}}, ConnectionID: "connection", Generation: "generation", ConnectionVersion: 1, CredentialVersion: 1}
	attempts := 0
	m := &roomManager{referenceAdapters: map[string]referenceAdapterFactory{
		"fixture": func() platform.ReferenceCapabilities {
			return platform.ReferenceCapabilities{
				Read: func(_ context.Context, q platform.ReadTargetRequest, a platform.AccessContext) (platform.TargetPage, error) {
					if !q.Limits.Valid() || !a.Valid() || q.Cursor != nil && !q.Cursor.ValidFor(q.Target, a, q.Scope) {
						return platform.TargetPage{}, platform.ErrReference
					}
					return platform.TargetPage{Status: platform.ResolutionResolved, Target: q.Target, Account: a.Account, Contents: []platform.TargetContent{{Target: q.Target, Body: "hello", Complete: true}}, Complete: true}, nil
				},
				Actions: map[platform.Operation]platform.ActionCapability{platform.OperationObjectComment: {
					Spec: platform.ActionSpec{Operation: platform.OperationObjectComment, TargetKind: "object", InputSchema: json.RawMessage(`{"type":"object","required":["body"]}`), Retry: platform.RetryPolicy{MaxAttempts: 1}},
					Execute: func(_ context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
						attempts++
						return platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "unknown", RetryClass: "never"}, nil
					},
				}},
			}
		},
	}}
	m.initPlatformReferences() // preserves independently supplied static capabilities
	caps, err := m.referenceCapabilities("fixture")
	if err != nil || caps.Resolve != nil || !access.Valid() || !target.Valid() {
		t.Fatal(err)
	}
	page, err := caps.Read(context.Background(), platform.ReadTargetRequest{Target: target, Scope: "content", Limits: platform.ReadLimits{Items: 1, Bytes: 1024, BodyBytes: 1024}}, access)
	if err != nil || !page.Complete || len(page.Contents) != 1 {
		t.Fatal(page, err)
	}
	action := caps.Actions[platform.OperationObjectComment]
	result, err := action.Execute(context.Background(), platform.TargetActionRequest{Target: target, Operation: action.Spec.Operation, Input: json.RawMessage(`{"body":"reply"}`)}, access)
	if err != nil || result.Status != "unknown" || attempts != 1 || result.Account != access.Account {
		t.Fatal(result, attempts, err)
	}
	if _, ok := caps.Actions[platform.OperationDiscussionReply]; ok {
		t.Fatal("unsupported discussion reply advertised")
	}
	if _, err := m.referenceCapabilities("missing"); !errors.Is(err, platform.ErrReference) {
		t.Fatal(err)
	}
}
