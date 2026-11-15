//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func toolConfigurationCLI(t *testing.T, h *larkHarness, endpoint string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, productionFixtureCLI(t), append([]string{"platforms"}, args...)...)
	env := cliToolEnv(endpoint, h.svc, h.members[0])
	cmd.Env = os.Environ()
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		cmd.Env = append(cmd.Env, key+"="+env(key))
	}
	return cmd.CombinedOutput()
}

func TestToolConfigurationNativeCLIIndependentPlatforms(t *testing.T) {
	for _, order := range []string{"lark-first", "gitlab-first", "overlap"} {
		t.Run(order, func(t *testing.T) {
			h := larkToolConfigurationHarness(t)
			g := configurationGitLab(t, h)
			source := larkMemberToolRequest(t, h, 0)
			server := httptest.NewServer(h.m)
			defer server.Close()
			// Both real CLI reads observe zero, even when the other platform is saved first.
			for _, p := range []managedPlatform{h.c, g} {
				raw, err := toolConfigurationCLI(t, h, server.URL, "member", "get", "--platform-id", p.ID, "--platform-version", fmt.Sprint(p.Version))
				var view platformMemberView
				if err != nil || json.Unmarshal(raw, &view) != nil || view.BindingRevision != 0 {
					t.Fatal(err, string(raw))
				}
			}
			larkID, _ := newOpaqueID()
			gitlabID, _ := newOpaqueID()
			args := func(p managedPlatform, id string) []string {
				a := []string{"member", "tool", "--platform-id", p.ID, "--platform-version", fmt.Sprint(p.Version), "--operation-id", id, "--source-seq", strconv.FormatUint(source.SourceSeq, 10), "--enabled", "true"}
				if order != "lark-first" {
					a = append(a, "--binding-revision", "0")
				}
				return a
			}
			var saves atomic.Int32
			h.m.mu.Lock()
			real := h.m.store.ops.rename
			h.m.store.ops.rename = func(from, to string) error {
				if to == h.m.toolBindingsPath(h.room.RoomIdentity) {
					saves.Add(1)
				}
				return real(from, to)
			}
			h.m.mu.Unlock()
			run := func(p managedPlatform, id string) platformMemberOperation {
				t.Helper()
				raw, err := toolConfigurationCLI(t, h, server.URL, args(p, id)...)
				var op platformMemberOperation
				if err != nil || json.Unmarshal(raw, &op) != nil || op.Status != "completed" {
					t.Fatal(err, string(raw))
				}
				return op
			}
			var larkOp platformMemberOperation
			switch order {
			case "lark-first":
				larkOp = run(h.c, larkID)
				run(g, gitlabID)
			case "gitlab-first":
				run(g, gitlabID)
				larkOp = run(h.c, larkID)
			case "overlap":
				entered, release := make(chan struct{}), make(chan struct{})
				h.m.mu.Lock()
				h.m.bindingEvidencePreparers["lark"] = func(ctx context.Context, _ *toolBindingChange) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				h.m.mu.Unlock()
				type response struct {
					raw []byte
					err error
				}
				done := make(chan response, 1)
				go func() {
					raw, err := toolConfigurationCLI(t, h, server.URL, args(h.c, larkID)...)
					done <- response{raw, err}
				}()
				select {
				case <-entered:
				case <-time.After(10 * time.Second):
					t.Fatal("Lark preparation did not start")
				}
				run(g, gitlabID)
				close(release)
				r := <-done
				if r.err != nil || json.Unmarshal(r.raw, &larkOp) != nil || larkOp.Status != "completed" {
					t.Fatal(r.err, string(r.raw))
				}
			}
			if f := configurationBindings(t, h); len(f.Bindings) != 2 || f.Revision != 2 || saves.Load() != 2 {
				t.Fatal(f, saves.Load())
			}
			if order == "overlap" && (larkOp.Result.Version != 2 || larkOp.Capabilities.ToolRevision != 2 || larkOp.Authorization.Revision != 2) {
				t.Fatal(larkOp)
			}
			again := run(h.c, larkID)
			if !sameJSON(again, larkOp) || saves.Load() != 2 {
				t.Fatal("duplicate CLI replayed", again, saves.Load())
			}
			changed := args(h.c, larkID)
			changed = append(changed, "--action", "message.reply")
			if raw, err := toolConfigurationCLI(t, h, server.URL, changed...); err == nil || !bytes.Contains(raw, []byte("conflict")) {
				t.Fatal(err, string(raw))
			}
			if raw, err := toolConfigurationCLI(t, h, server.URL, append(args(g, gitlabID), "--tool-revision", "0")...); err == nil || !bytes.Contains(raw, []byte("tool-revision")) {
				t.Fatal("retired zero flag accepted", err, string(raw))
			}
			if saves.Load() != 2 {
				t.Fatal("rejected input wrote settings")
			}
		})
	}
}

func TestToolConfigurationCLIResponseLossDoesNotReplay(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	g := configurationGitLab(t, h)
	req := larkMemberToolRequest(t, h, 0)
	var lost atomic.Bool
	var saves atomic.Int32
	h.m.mu.Lock()
	real := h.m.store.ops.rename
	h.m.store.ops.rename = func(from, to string) error {
		if to == h.m.toolBindingsPath(h.room.RoomIdentity) {
			saves.Add(1)
		}
		return real(from, to)
	}
	h.m.mu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/platforms/member/tool" && lost.CompareAndSwap(false, true) {
			receipt := httptest.NewRecorder()
			h.m.ServeHTTP(receipt, r)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		h.m.ServeHTTP(w, r)
	}))
	defer server.Close()
	args := []string{"member", "tool", "--platform-id", g.ID, "--platform-version", fmt.Sprint(g.Version), "--operation-id", req.OperationID, "--source-seq", fmt.Sprint(req.SourceSeq), "--enabled", "true"}
	if raw, err := toolConfigurationCLI(t, h, server.URL, args...); err == nil {
		t.Fatal("response loss reported success", string(raw))
	}
	if saves.Load() != 1 {
		t.Fatal("CLI retried unknown result", saves.Load())
	}
	raw, err := toolConfigurationCLI(t, h, server.URL, "member", "status", "--operation-id", req.OperationID)
	if err != nil || !bytes.Contains(raw, []byte(`"status":"completed"`)) && !bytes.Contains(raw, []byte(`"status": "completed"`)) {
		t.Fatal(err, string(raw))
	}
	raw, err = toolConfigurationCLI(t, h, server.URL, args...)
	var op platformMemberOperation
	if err != nil || json.Unmarshal(raw, &op) != nil || op.Status != "completed" || saves.Load() != 1 {
		t.Fatal(err, string(raw), saves.Load())
	}
}
