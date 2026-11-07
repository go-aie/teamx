package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func platformTestRequest(m *roomManager) platformConnectionRequest {
	return platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("a", 32), Name: "Company GitLab", Provider: "gitlab", URL: "https://gitlab.invalid/"}
}
func platformTestSave(t *testing.T, m *roomManager, req platformConnectionRequest) platformConnection {
	t.Helper()
	response := managerHTTP(m, "/v1/platform/connections/save", req)
	var c platformConnection
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &c) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test-platform-secret") {
		t.Fatal("API exposed PAT")
	}
	if req.Version == 0 && req.Provider == "gitlab" {
		return authorizeTestConnection(t, m, c, 101, "test-platform-secret")
	}
	return c
}
func platformTestList(t *testing.T, m *roomManager) []platformConnection {
	t.Helper()
	response := managerHTTP(m, "/v1/platform/connections?service_epoch="+m.epoch+"&caller_id=tester", nil)
	var list struct {
		Connections []platformConnection `json:"connections"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test-platform-secret") {
		t.Fatal("list exposed PAT")
	}
	return list.Connections
}

func runAppHandler(t *testing.T, m *roomManager, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "TEAMX_WEB_TRANSPORT=stdio")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	decoder, encoder := json.NewDecoder(output), json.NewEncoder(input)
	for {
		var call struct {
			ID   uint64          `json:"id"`
			Path string          `json:"path"`
			Body json.RawMessage `json:"body"`
		}
		if err = decoder.Decode(&call); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		method := "GET"
		if len(call.Body) > 0 && string(call.Body) != "null" {
			method = "POST"
		}
		r := httptest.NewRequest(method, call.Path, bytes.NewReader(call.Body))
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if err = encoder.Encode(struct {
			ID     uint64          `json:"id"`
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		}{call.ID, w.Code, w.Body.Bytes()}); err != nil {
			input.Close()
			_ = cmd.Wait()
			t.Fatalf("app/handler response: %v\n%s", err, diagnostics.String())
		}
	}
	if err = cmd.Wait(); err != nil {
		t.Fatalf("app/handler: %v\n%s", err, diagnostics.String())
	}
}
