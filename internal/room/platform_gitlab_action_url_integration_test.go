//go:build integration || e2e

package room

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

const gitlabLocatorWeb = "https://gitlab.invalid/g/p/-/merge_requests/42"

// No notification, Workspace, followup or saved target is created. Only the
// member binding and ordinary human image publications precede URL resolution.
func gitlabURLFixture(t *testing.T) *gitlabImageFixture {
	t.Helper()
	m, room, svc, connection, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], connection, "gitlab", 0)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: members[0].AgentID, MembershipID: members[0].MembershipID}
	m.bindPlatformReferences(svc)
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id}); err != nil {
		t.Fatal(err)
	}
	policy, err := svc.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	base := &managedActionFixture{m: m, room: room, svc: svc, connection: connection, member: members[0], id: id, req: apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)}, action: referenceActionRequest{ID: "url-action", Operation: platform.OperationObjectComment, PolicyToken: policy.PolicyToken}}
	f := configureGitlabImageFixture(t, base)
	installGitlabURLFixtureReads(t, f)
	return f
}

func installGitlabURLFixtureReads(t *testing.T, f *gitlabImageFixture) {
	t.Helper()
	m := f.m
	underlying := m.platformHTTP
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && (r.URL.Path == "/api/v4/projects/g/p" || strings.HasSuffix(r.URL.Path, "/discussions")) {
			if r.URL.Path == "/api/v4/projects/g/p" && r.URL.EscapedPath() != "/api/v4/projects/g%2Fp" {
				t.Error("project not escaped exactly once", r.URL.EscapedPath())
			}
			data := `{"id":123,"path_with_namespace":"g/p"}`
			if strings.HasSuffix(r.URL.Path, "/discussions") {
				data = `[{"id":"thread","individual_note":false,"notes":[` + strings.ReplaceAll(managedActionNote, "MergeRequest", f.objectType) + `]}]`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
		}
		return underlying.RoundTrip(r)
	})
}

func resolveGitlabURLCLI(t *testing.T, f *gitlabImageFixture, raw string) (agentGitlabTargetView, error) {
	t.Helper()
	var selections []toolSelection
	if err := f.cli([]string{"tools", "list"}, &selections); err != nil {
		t.Fatal(err)
	}
	entry := ""
	for _, s := range selections {
		if s.Provider == "gitlab" {
			entry = s.Name
		}
	}
	if entry == "" {
		t.Fatal("missing exact entry")
	}
	var target agentGitlabTargetView
	request := mustContractJSON(actionResolveRequest{Locator: platform.TargetLocator{URL: raw}, TurnNumber: 1})
	err := f.cli([]string{"actions", "resolve", "--tool", entry, "--request", string(request)}, &target)
	return target, err
}

func TestGitlabActionURLManagedCLI(t *testing.T) {
	for _, route := range []string{"merge_requests", "issues", "work_items"} {
		for _, mode := range []string{"text", "single", "caption", "note-reply", "discussion-reply"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				f := gitlabURLFixture(t)
				f.route = route
				if route != "merge_requests" {
					f.objectType = "Issue"
				}
				before := historyTree(t, f.svc.store.dir)
				files, err := filepath.Glob(filepath.Join(f.svc.store.actionRoot(f.id), "targets", "*.json"))
				if err != nil || len(files) != 0 {
					t.Fatal("fixture already has targets", files, err)
				}
				raw := strings.Replace(gitlabLocatorWeb, "merge_requests", route, 1)
				if mode == "note-reply" {
					raw += "#note_99"
				}
				if mode == "discussion-reply" {
					raw += "#discussion_thread"
				}
				target, err := resolveGitlabURLCLI(t, f, raw)
				if err != nil || target.TargetRef == "" || !target.Resolution.Complete || target.Resolution.Target == nil || target.Resolution.Target.IID.Value != "42" || len(target.Actions) != 1 {
					t.Fatal(target, err)
				}
				f.action.TargetRef = target.TargetRef
				if strings.HasSuffix(mode, "reply") {
					f.action.Operation = platform.OperationDiscussionReply
				}
				body, images := "", f.refs[:1]
				if mode == "text" {
					body, images = "exact reply", nil
				}
				if mode == "caption" {
					body, images = "准确说明\n第二行", f.refs[:2]
				}
				f.setInput(t, body, images)
				again, err := resolveGitlabURLCLI(t, f, raw)
				if err != nil || again.TargetRef != target.TargetRef || f.posts.Load() != 0 || f.uploads.Load() != 0 {
					t.Fatal(again, err)
				}
				f.svc.store.mu.Lock()
				saved, err := f.svc.store.loadActionTargetLocked(f.id, target.TargetRef)
				f.svc.store.mu.Unlock()
				if err != nil || saved.Target.Message.Valid() != (mode == "note-reply") || saved.Access.Receiver != saved.Access.Account || saved.Resolution.DiscussionVerified != strings.HasSuffix(mode, "reply") {
					t.Fatal(saved, err)
				}
				var result agentGitlabReceipt
				if err := f.executeCLI(t, &result); err != nil || !result.Result.Verified || result.Status != "succeeded" || f.posts.Load() != 1 || f.uploads.Load() != int32(len(images)) {
					t.Fatal(result, err, f.posts.Load(), f.uploads.Load())
				}
				// Reopening durable storage and same-ID recovery need neither locator
				// nor credentials/remote reads, and must not re-upload or re-post.
				f.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
					t.Error("receipt recovery touched GitLab")
					return nil, fmt.Errorf("unexpected request")
				})
				if err := f.executeCLI(t, &result); err != nil || result.Status != "succeeded" {
					t.Fatal(result, err)
				}
				var detail actionQueryRecord
				if err := f.cli([]string{"actions", "read", "--id", f.action.ID}, &detail); err != nil || detail.Detail == nil {
					t.Fatal(detail, err)
				}
				after := historyTree(t, f.svc.store.dir)
				for path, hash := range before {
					if !strings.Contains(path, "/actions/") && after[path] != hash {
						t.Error("changed non-action state", path)
					}
				}
				for path := range after {
					if _, ok := before[path]; !ok && !strings.Contains(path, "/actions/") {
						t.Error("created non-action state", path)
					}
				}
				assertActionCallsSettled(t, f.m)
				closeService(t, f.svc)
				reopened, err := OpenStore(f.svc.store.dir, f.id.RoomID)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				loaded, err := reopened.loadActionTargetLocked(f.id, target.TargetRef)
				if err != nil || !reflect.DeepEqual(saved, loaded) {
					t.Fatal(loaded, err)
				}
				receipts, err := reopened.queryActions(f.id, f.action.ID, "", "")
				if err != nil || len(receipts) != 1 || receipts[0].Status != "succeeded" {
					t.Fatal(receipts, err)
				}
			})
		}
	}
}

func TestGitlabActionURLFailureAndCommitGuard(t *testing.T) {
	for _, mode := range []string{"instance", "anchor", "permission", "missing-note", "system-note", "cancel", "turn", "credential", "commit-binding", "commit-credential", "commit-turn"} {
		t.Run(mode, func(t *testing.T) {
			f := gitlabURLFixture(t)
			advanceTurn := func() error {
				a, err := f.svc.store.LoadAgent(f.id)
				if err != nil {
					return err
				}
				a.TurnNumber++
				a.Turn.Number = a.TurnNumber
				return f.svc.store.SaveAgent(a)
			}
			revokeCredential := func() error {
				f.m.mu.Lock()
				defer f.m.mu.Unlock()
				file, err := f.m.platforms.Load()
				if err != nil {
					return err
				}
				for _, c := range file.Platforms {
					if c.ID == f.connection.ID {
						_, _, err := f.m.platforms.LogoutOAuth(c.CredentialID)
						return err
					}
				}
				return ErrIdentity
			}
			disableBinding := func() error {
				f.m.mu.Lock()
				defer f.m.mu.Unlock()
				bindings, err := f.m.readToolBindingsLocked(f.id.RoomIdentity)
				if err != nil {
					return err
				}
				bindings.Bindings = nil
				return f.m.store.save(f.m.toolBindingsPath(f.id.RoomIdentity), bindings, 1<<20)
			}
			transport := f.m.platformHTTP
			f.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/discussions") && (mode == "missing-note" || mode == "system-note") {
					data := `[]`
					if mode == "system-note" {
						data = `[{"id":"thread","individual_note":true,"notes":[` + strings.Replace(managedActionNote, `"system":false`, `"system":true`, 1) + `]}]`
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
				}
				if mode == "permission" {
					return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				response, err := transport.RoundTrip(r)
				if r.URL.Path == "/api/v4/projects/123/merge_requests/42" {
					switch mode {
					case "cancel":
						f.m.mu.Lock()
						f.m.cancelPlatformCallsLocked(func(*platformCall) bool { return true })
						f.m.mu.Unlock()
					case "turn":
						if e := advanceTurn(); e != nil {
							t.Error(e)
						}
					}
				}
				return response, err
			})
			if strings.HasPrefix(mode, "commit-") {
				original := f.m.actionResolvers["gitlab"]
				f.m.actionResolvers["gitlab"] = func(ctx context.Context, id MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
					r, s, e := original(ctx, id, l)
					check := s.Check
					count := 0
					s.Check = func(ctx context.Context) error {
						if e := check(ctx); e != nil {
							return e
						}
						count++
						if count == 2 {
							if mode == "commit-turn" {
								return advanceTurn()
							}
							if mode == "commit-credential" {
								return revokeCredential()
							}
							return disableBinding()
						}
						return nil
					}
					return r, s, e
				}
			}
			if mode == "credential" {
				original := f.m.actionResolvers["gitlab"]
				f.m.actionResolvers["gitlab"] = func(ctx context.Context, id MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
					r, s, e := original(ctx, id, l)
					if err := revokeCredential(); err != nil {
						t.Error(err)
					}
					return r, s, e
				}
			}
			raw := gitlabLocatorWeb
			if mode == "instance" {
				raw = strings.Replace(raw, "gitlab.invalid", "other.invalid", 1)
			}
			if mode == "anchor" {
				raw += "#not-supported"
			}
			if mode == "missing-note" || mode == "system-note" {
				raw += "#note_99"
			}
			got, err := resolveGitlabURLCLI(t, f, raw)
			if err == nil && (got.TargetRef != "" || got.Resolution.Status == platform.ResolutionResolved || got.Resolution.ReasonCode == "") {
				t.Fatal(got, err)
			}
			if mode == "missing-note" || mode == "system-note" {
				if err != nil || got.Resolution.Status != platform.ResolutionPartial || len(got.Actions) != 0 {
					t.Fatal("partial comment lookup exposed an executable action", got, err)
				}
			}
			files, _ := filepath.Glob(filepath.Join(f.svc.store.actionRoot(f.id), "targets", "*.json"))
			if len(files) != 0 || f.posts.Load() != 0 || f.uploads.Load() != 0 {
				t.Fatal("failed resolve persisted/wrote", files)
			}
			assertActionCallsSettled(t, f.m)
		})
	}
}

func TestGitlabActionURLAccountAndMemberIsolation(t *testing.T) {
	f := gitlabURLFixture(t)
	view, err := resolveGitlabURLCLI(t, f, gitlabLocatorWeb)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.store.mu.Lock()
	target, err := f.svc.store.loadActionTargetLocked(f.id, view.TargetRef)
	f.svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	other := target.Access
	other.Account.ID.Value = "102"
	other.Receiver = other.Account
	r := target.Resolution
	r.Account = other.Account
	if _, err := f.svc.store.saveActionTarget(f.id, r, other); err != ErrConflict {
		t.Fatal("another account took over target", err)
	}
	changed := f.id
	changed.MembershipID = "another-member"
	if targetReference(changed, target.Target) == view.TargetRef {
		t.Fatal("membership collision")
	}
	changed = f.id
	changed.RoomInstance = "another-room"
	if targetReference(changed, target.Target) == view.TargetRef {
		t.Fatal("room collision")
	}
}
