//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentJSONInputManagedActionReplayAndLimits(t *testing.T) {
	parallelFixtureTest(t)
	for _, discussion := range []bool{false, true} {
		t.Run(map[bool]string{false: "object", true: "discussion"}[discussion], func(t *testing.T) {
			f := managedActionTest(t, discussion)
			server := httptest.NewServer(f.m)
			defer server.Close()
			env := cliToolEnv(server.URL, f.svc, f.member)
			// Input mode equivalence and size boundaries are checked below the
			// manager. Retain both real rejection gates with no external post.
			for _, size := range []int{4097, 65 << 10} {
				bad := f.action
				bad.Input.Body = strings.Repeat("x", size)
				payload, _ := json.Marshal(agentActionFixture(bad))
				args, stdin := jsonInputArgs(t, "actions", "execute", string(payload), "stdin")
				message := "input.body: required nonempty text, at most 4096 bytes"
				if size > 64<<10 {
					message = "request body exceeds limit or could not be read"
				}
				var rejected bytes.Buffer
				if err := runCLIWithInput(t.Context(), args, stdin, &rejected, io.Discard, env); err == nil || !strings.Contains(err.Error(), message) || f.posts.Load() != 0 || rejected.Len() != 0 {
					t.Fatal(size, err, f.posts.Load(), rejected.String())
				}
			}

			payload, _ := json.Marshal(agentActionFixture(f.action))
			var expected []byte
			for attempt := range 2 {
				args, stdin := jsonInputArgs(t, "actions", "execute", string(payload), "stdin")
				var stdout bytes.Buffer
				if err := runCLIWithInput(t.Context(), args, stdin, &stdout, io.Discard, env); err != nil {
					t.Fatal(attempt, err)
				}
				if expected == nil {
					expected = bytes.Clone(stdout.Bytes())
				}
				var receipt referenceActionReceipt
				if json.Unmarshal(stdout.Bytes(), &receipt) != nil || receipt.Status != "succeeded" || receipt.Attempts != 1 || f.posts.Load() != 1 || !bytes.Equal(stdout.Bytes(), expected) {
					t.Fatal("changed replay/result", attempt, receipt, f.posts.Load())
				}
			}
		})
	}
}
