package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T) (*service, cliClient) {
	t.Helper()
	svc, err := newFixtureService(t.TempDir(), "design-review", serviceConfig{mergeWindow: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(svc)
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	})
	return svc, cliClient{endpoint: server.URL, http: &http.Client{Timeout: 3 * time.Second}}
}

func addFixtureMemberCLI(t *testing.T, svc *service, client cliClient, agent string) apiRequest {
	t.Helper()
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: agent, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}
	var added MemberInfo
	if err := client.request(context.Background(), "/v1/member/add", &req, &added); err != nil {
		t.Fatal(err)
	}
	return apiRequest{ServiceInfo: svc.info, Caller: agentCaller(agent, added.MembershipID)}
}

type failedOutput struct{ short bool }

func (w failedOutput) Write(b []byte) (int, error) {
	if w.short {
		return len(b) - 1, nil
	}
	return 0, errors.New("broken pipe")
}

func TestCLIOutputFailureDoesNotConfirm(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprint(short), func(t *testing.T) {
			svc, client := testService(t)
			a := addFixtureMemberCLI(t, svc, client, "A")
			req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "one", Text: "body"}
			var posted PostReceipt
			if err := client.request(context.Background(), "/v1/send", &req, &posted); err != nil {
				t.Fatal(err)
			}
			a.Mode, a.Limit = ReadUnread, 20
			if err := readCLI(context.Background(), client, a, failedOutput{short}, true); err == nil || !strings.Contains(err.Error(), "not confirmed") {
				t.Fatal(err)
			}
			members, err := svc.members(context.Background())
			if err != nil || members.Members[0].ReadThrough != 0 {
				t.Fatal(members, err)
			}
			var page ReadPage
			if err := client.request(context.Background(), "/v1/read", &a, &page); err == nil {
				t.Fatal("failed output should leave the receipt pending")
			}
		})
	}
}

func TestCLIConfirmationFailureAndLostReply(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			svc, client := testService(t)
			a := addFixtureMemberCLI(t, svc, client, "A")
			req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "one", Text: "body"}
			var posted PostReceipt
			if err := client.request(context.Background(), "/v1/send", &req, &posted); err != nil {
				t.Fatal(err)
			}
			var confirmCalls atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/read/confirm" {
					svc.ServeHTTP(w, r)
					return
				}
				confirmCalls.Add(1)
				if commit {
					recorder := httptest.NewRecorder()
					svc.ServeHTTP(recorder, r)
					if recorder.Code != http.StatusOK {
						t.Errorf("confirmation: %s", recorder.Body.String())
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				writeAPIError(w, errors.New("injected confirmation failure"))
			}))
			defer proxy.Close()
			broken := client
			broken.endpoint = proxy.URL
			a.Mode, a.Limit = ReadUnread, 20
			var output bytes.Buffer
			err := readCLI(context.Background(), broken, a, &output, true)
			if err == nil || !strings.Contains(err.Error(), "confirmation failed or uncertain") || !strings.Contains(output.String(), "body") || confirmCalls.Load() != 1 {
				t.Fatal(output.String(), err, confirmCalls.Load())
			}
			members, err := svc.members(context.Background())
			want := uint64(0)
			if commit {
				want = 1
			}
			if err != nil || members.Members[0].ReadThrough != want {
				t.Fatal(members, err)
			}
		})
	}
}

func TestCLIUnreadPeekPaginatesWithoutConsumption(t *testing.T) {
	svc, client := testService(t)
	a := addFixtureMemberCLI(t, svc, client, "A")
	for i := range 105 {
		req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: fmt.Sprint(i), Text: "body"}
		var posted PostReceipt
		if err := client.request(context.Background(), "/v1/send", &req, &posted); err != nil {
			t.Fatal(err)
		}
	}
	a.Mode, a.Limit, a.Peek = ReadUnread, 105, true
	var output bytes.Buffer
	if err := readCLI(context.Background(), client, a, &output, true); err != nil {
		t.Fatal(err)
	}
	pages := decodePages(t, output.Bytes())
	if len(pages) != 2 || len(pages[0].Messages) != 100 || len(pages[1].Messages) != 5 {
		t.Fatal(pages)
	}
	status, err := svc.members(context.Background())
	if err != nil || status.Members[0].ReadThrough != 0 || status.Members[0].UnreadCount != 105 {
		t.Fatal(status, err)
	}
	a.Peek = false
	output.Reset()
	if err := readCLI(context.Background(), client, a, &output, true); err != nil {
		t.Fatal(err)
	}
	status, err = svc.members(context.Background())
	if err != nil || status.Members[0].ReadThrough != 105 {
		t.Fatal(status, err)
	}
}

func decodePages(t *testing.T, data []byte) []ReadPage {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	var pages []ReadPage
	for {
		var page ReadPage
		err := dec.Decode(&page)
		if err == io.EOF {
			return pages
		}
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, page)
	}
}

func cleanRoomEnv() []string {
	result := []string{}
	for _, entry := range os.Environ() {
		if !isRoomToolEnv(entry) {
			result = append(result, entry)
		}
	}
	return result
}

func TestCLIAddDisconnectRetainsRegistration(t *testing.T) {
	svc, client := testService(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer onceRelease(release)()
	real := svc.store.ops.syncDir
	var once sync.Once
	svc.store.ops.syncDir = func(dir string) error {
		once.Do(func() { close(entered); <-release })
		return real(dir)
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: "A", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { var member MemberInfo; finished <- client.request(ctx, "/v1/member/add", &req, &member) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("add never committed")
	}
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("disconnected add returned success")
	}
	onceRelease(release)()
	deadline, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	for {
		list, err := svc.members(deadline)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Members) == 1 && list.Members[0].Status == "paused" {
			var retry MemberInfo
			if err := client.request(deadline, "/v1/member/add", &req, &retry); err != nil {
				t.Fatal(err)
			}
			if retry.MembershipID != list.Members[0].MembershipID {
				t.Fatal("retry created another membership")
			}
			return
		}
		select {
		case <-deadline.Done():
			t.Fatal("registration abandoned")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCLISendLostReplyRetainsRetryID(t *testing.T) {
	svc, client := testService(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/send" {
			svc.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		svc.ServeHTTP(recorder, r)
		if recorder.Code != http.StatusOK {
			t.Errorf("send: %s", recorder.Body.String())
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer proxy.Close()
	env := func(key string) string {
		if key == "TEAMX_ENDPOINT" {
			return proxy.URL
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	err := runCLI(context.Background(), []string{"send", "--id", "stable-retry", "--text", "body", "--json"}, &stdout, &stderr, env)
	if err == nil || !strings.Contains(err.Error(), "client_message_id=stable-retry") || stdout.Len() != 0 {
		t.Fatal(stdout.String(), err)
	}
	req, err := client.identity(context.Background(), "", env)
	if err != nil {
		t.Fatal(err)
	}
	req.ClientMessageID, req.Text = "stable-retry", "body"
	var receipt PostReceipt
	if err := client.request(context.Background(), "/v1/send", &req, &receipt); err != nil || receipt.Seq != 1 {
		t.Fatal(receipt, err)
	}
	list, err := svc.members(context.Background())
	if err != nil || list.LatestSeq != 1 {
		t.Fatal(list, err)
	}
}

func TestRemovedMigrationCommands(t *testing.T) {
	for _, command := range []string{"migrate", "migrate-workspace"} {
		var out, diagnostics bytes.Buffer
		if err := Run([]string{"rooms", command}, &out, &diagnostics); err == nil {
			t.Fatal("retired migration command accepted", command)
		}
	}
}

func TestServeRejectsUnboundedTurnBudget(t *testing.T) {
	for _, limit := range []string{"0", "-1"} {
		var stdout, stderr bytes.Buffer
		if err := serve([]string{"--config", writeStartupConfig(t, t.TempDir(), ""), "--max-turns", limit}, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("invalid turn limit accepted", limit, err)
		}
	}
}
