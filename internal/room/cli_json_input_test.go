package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var jsonInputCommands = []struct{ command, op, payload string }{
	{"actions", "execute", `{"object_ref":"ref_o","unit_ref":"unit_u","unit_version":3,"turn_number":2,"target_ref":"ref_t","id":"stable","policy_token":"token","authority":{"room_seq":18,"reason":"准确依据"},"operation":"object.comment","input":{"body":"中文\n\"quotes\" \\ $HOME $(literal) '"}}`},
	{"policy", "update", `{"change":{"id":"style","revision":2,"version":1,"kind":"preference","status":"active","text":"中文\n\"quotes\" \\ $HOME $(literal) '","source_seq":18,"turn_number":2},"mode":"explicit_long_term","basis":"明确长期指令"}`},
	{"matters", "update", `{"object_ref":"ref_o","unit_ref":"unit_u","unit_version":3,"turn_number":2,"status":"in_progress","summary":"中文\n\"quotes\" \\ $HOME $(literal) '"}`},
	{"matters", "notify", `{"object_ref":"ref_o","unit_ref":"unit_u","unit_version":3,"turn_number":2,"text":"@operator 请确认\n准确问题","proposed":{"operation":"object.comment","body":"准确正文"}}`},
	{"matters", "reply", `{"object_ref":"ref_o","unit_ref":"unit_u","unit_version":3,"turn_number":2,"question_ref":"question_q","source_seq":18,"basis":"准确答复"}`},
	{"matters", "instruct", `{"object_ref":"ref_o","unit_ref":"unit_u","unit_version":3,"turn_number":2,"source_seq":18,"intent":{"operation":"object.comment","body":"准确正文"},"basis":"准确指令","expires_at":"2026-11-07T00:00:00Z"}`},
	{"matters", "instruction-status", `{"object_ref":"ref_o","unit_ref":"unit_u","unit_version":3,"turn_number":2,"id":"instruction_i","version":1,"status":"revoked","source_seq":18,"evidence":"用户撤销"}`},
}

func jsonInputArgs(t *testing.T, command, op, payload, mode string) ([]string, io.Reader) {
	t.Helper()
	name := referencePayloadFlag(command, op)
	args := []string{command, op, "--" + name}
	switch mode {
	case "inline":
		return append(args, payload), strings.NewReader("")
	case "file":
		path := filepath.Join(t.TempDir(), "请求 with spaces.json")
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		args[2] += "-file"
		return append(args, path), strings.NewReader("")
	case "stdin":
		args[2] += "-file"
		return append(args, "-"), strings.NewReader(payload)
	default:
		t.Fatal("unknown input mode", mode)
		return nil, nil
	}
}

func jsonInputEnv(endpoint string) func(string) string {
	values := map[string]string{"TEAMX_ENDPOINT": endpoint, "TEAMX_ROOM_ID": "room", "TEAMX_ROOM_INSTANCE": "instance", "TEAMX_SERVICE_EPOCH": "epoch", "TEAMX_AGENT_ID": "agent", "TEAMX_MEMBERSHIP_ID": "membership"}
	return func(key string) string { return values[key] }
}

func TestAgentJSONInputProducesIdenticalRequests(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- append([]byte(r.URL.Path+"\n"), data...)
		_, _ = io.WriteString(w, `{"status":"captured"}`)
	}))
	defer server.Close()
	for _, tc := range jsonInputCommands {
		t.Run(tc.command+"/"+tc.op, func(t *testing.T) {
			var expected []byte
			for _, mode := range []string{"inline", "file", "stdin"} {
				// Preserve the old acceptance of large whitespace/escape-heavy
				// source JSON: limits apply to the serialized HTTP envelope.
				payload := "\n" + strings.Repeat(" ", 128<<10) + tc.payload + "\n"
				args, stdin := jsonInputArgs(t, tc.command, tc.op, payload, mode)
				var stdout bytes.Buffer
				if err := runCLIWithInput(context.Background(), args, stdin, &stdout, io.Discard, jsonInputEnv(server.URL)); err != nil {
					t.Fatal(mode, err)
				}
				got := <-requests
				if expected == nil {
					expected = got
				}
				if !bytes.Equal(got, expected) || stdout.String() != "{\"status\":\"captured\"}\n" {
					t.Fatalf("%s changed request or output: %s %s", mode, got, &stdout)
				}
			}
		})
	}
}

type failingJSONInput struct{ reads int }

func (r *failingJSONInput) Read(p []byte) (int, error) {
	r.reads++
	return copy(p, `{}`), errors.New("input read failed")
}

func TestAgentJSONInputLocalFailuresDoNotSend(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, tc := range jsonInputCommands {
		t.Run(tc.command+"/"+tc.op, func(t *testing.T) {
			name := "--" + referencePayloadFlag(tc.command, tc.op)
			missing := filepath.Join(t.TempDir(), "missing.json")
			for _, bad := range []struct {
				args    []string
				message string
			}{
				{[]string{name, tc.payload, name + "-file", missing}, "mutually exclusive"},
				{[]string{name + "-file", "-", name, ""}, "mutually exclusive"},
				{[]string{name + "-file", "", name, tc.payload}, "mutually exclusive"},
				{[]string{name + "-file", ""}, "file path is required"},
				{[]string{name + "-file", missing}, "no such file"},
				{[]string{name + "-file", t.TempDir()}, "directory"},
				{[]string{name + "-file", "-"}, "input read failed"},
			} {
				stdin := &failingJSONInput{}
				var stdout bytes.Buffer
				args := append([]string{tc.command, tc.op}, bad.args...)
				err := runCLIWithInput(context.Background(), args, stdin, &stdout, io.Discard, jsonInputEnv(server.URL))
				if err == nil || !strings.Contains(err.Error(), bad.message) || !strings.Contains(err.Error(), name+"-file") || stdout.Len() != 0 {
					t.Fatal(args, err, &stdout)
				}
				if bad.message != "input read failed" && stdin.reads != 0 {
					t.Fatal("read stdin before resolving invalid flags", args)
				}
			}
			for _, payload := range []string{"", `{"unknown_field":true}`, `{"unit_version":`, `{} {}`, `[]`} {
				var expected string
				for _, mode := range []string{"inline", "file", "stdin"} {
					args, stdin := jsonInputArgs(t, tc.command, tc.op, payload, mode)
					var stdout bytes.Buffer
					err := runCLIWithInput(context.Background(), args, stdin, &stdout, io.Discard, jsonInputEnv(server.URL))
					if !errors.Is(err, ErrRequest) || stdout.Len() != 0 {
						t.Fatal(mode, payload, err, &stdout)
					}
					_, diagnostic, _ := strings.Cut(err.Error(), ": ")
					if mode == "inline" {
						expected = diagnostic
					}
					if diagnostic != expected {
						t.Fatal("input modes disagree", mode, diagnostic, expected)
					}
				}
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached HTTP", calls.Load())
	}
}

func TestAgentJSONInputDoesNotRetryHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	for _, mode := range []string{"inline", "file", "stdin"} {
		args, stdin := jsonInputArgs(t, "actions", "execute", jsonInputCommands[0].payload, mode)
		before := calls.Load()
		if err := runCLIWithInput(context.Background(), args, stdin, io.Discard, io.Discard, jsonInputEnv(server.URL)); err == nil || calls.Load() != before+1 {
			t.Fatal(mode, err, calls.Load())
		}
	}
}

func TestAgentJSONInputManagedActionReplayAndLimits(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		t.Run(map[bool]string{false: "object", true: "discussion"}[discussion], func(t *testing.T) {
			f := managedActionTest(t, discussion)
			server := httptest.NewServer(f.m)
			defer server.Close()
			env := cliToolEnv(server.URL, f.svc, f.member)
			for _, size := range []int{4097, 65 << 10} {
				bad := f.action
				bad.Input.Body = strings.Repeat("x", size)
				payload, _ := json.Marshal(bad)
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
			payload, _ := json.Marshal(f.action)
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

func TestAgentJSONInputHelpIsScoped(t *testing.T) {
	for key := range referenceCommandHelps {
		parts := strings.Split(key, "/")
		help := referenceHelpText(parts[0], parts[1])
		name := referencePayloadFlag(parts[0], parts[1])
		if name == "" {
			if strings.Contains(help, "-file") {
				t.Fatal("unrelated file input help", key, help)
			}
			continue
		}
		for _, text := range []string{"--" + name + "-file PATH", "- reads stdin", "Mutually exclusive", "same JSON", "limits still apply", "payload.json"} {
			if !strings.Contains(help, text) {
				t.Fatal(key, "missing", text, help)
			}
		}
	}
}
