//go:build integration || e2e

package room

import (
	"bytes"
	"context"
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
			for _, size := range []int{4097, 65 << 10} {
				bad := f.action
				bad.Input.Body = strings.Repeat("x", size)
				payload, _ := json.Marshal(agentActionFixture(bad))
				var expected string
				for _, mode := range []string{"inline", "file", "stdin"} {
					args, stdin := jsonInputArgs(t, "actions", "execute", string(payload), mode)
					err := runCLIWithInput(context.Background(), args, stdin, io.Discard, io.Discard, env)
					if err == nil || f.posts.Load() != 0 {
						t.Fatal(mode, size, err, f.posts.Load())
					}
					if mode == "inline" {
						expected = err.Error()
					}
					if err.Error() != expected {
						t.Fatal("input modes changed size limit", mode, err, expected)
					}
				}
			}
			payload, _ := json.Marshal(agentActionFixture(f.action))
			var expected bytes.Buffer
			for _, mode := range []string{"inline", "file", "stdin"} {
				args, stdin := jsonInputArgs(t, "actions", "execute", string(payload), mode)
				var stdout bytes.Buffer
				if err := runCLIWithInput(context.Background(), args, stdin, &stdout, io.Discard, env); err != nil {
					t.Fatal(mode, err)
				}
				if mode == "inline" {
					expected.Write(stdout.Bytes())
				}
				var receipt referenceActionReceipt
				if json.Unmarshal(stdout.Bytes(), &receipt) != nil || receipt.Status != "succeeded" || receipt.Attempts != 1 || f.posts.Load() != 1 || !bytes.Equal(stdout.Bytes(), expected.Bytes()) {
					t.Fatal("changed replay/result", mode, receipt, f.posts.Load())
				}
			}
		})
	}
}
