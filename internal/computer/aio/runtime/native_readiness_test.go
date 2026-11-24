package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

func TestNativeCatalogWaitsForReadOnlyDiscoveryAndNeverRetriesActions(t *testing.T) {
	definition := json.RawMessage(`{"name":"native","inputSchema":{"type":"object"}}`)
	contract, _ := computer.NewContract("aio", nil, []computer.Tool{{Name: "native", NativeName: "native", Definition: definition}})
	var lists, actions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "tools/list" {
			actions.Add(1)
			http.Error(w, "action must not be retried", 503)
			return
		}
		if lists.Add(1) == 1 {
			http.Error(w, "starting", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []json.RawMessage{definition}}})
	}))
	defer server.Close()
	c := &nativeClient{url: server.URL, http: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got, err := awaitNativeCatalog(ctx, c, contract); err != nil || got.Digest != contract.Digest {
		t.Fatal(got, err)
	}
	if lists.Load() != 2 || actions.Load() != 0 {
		t.Fatal("catalog readiness executed a tool")
	}
	if _, err := c.rpc(ctx, "tools/call", map[string]any{"name": "native"}); err == nil {
		t.Fatal("failed action accepted")
	}
	if actions.Load() != 1 {
		t.Fatal("native action was replayed")
	}
}

func TestNativeCatalogRejectsContractDriftAndBoundsUnavailableStartup(t *testing.T) {
	definition := json.RawMessage(`{"name":"native","inputSchema":{"type":"object"}}`)
	contract, _ := computer.NewContract("aio", nil, []computer.Tool{{Name: "native", NativeName: "native", Definition: definition}})
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "schema_drift"}[drift], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !drift {
					http.Error(w, "starting", 503)
					return
				}
				var req struct {
					ID uint64 `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []json.RawMessage{json.RawMessage(`{"name":"native","inputSchema":{"type":"string"}}`)}}})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := awaitNativeCatalog(ctx, &nativeClient{url: server.URL, http: server.Client()}, contract)
			if drift && !computer.Is(err, computer.ContractMismatch) || !drift && !computer.Is(err, computer.Unavailable) {
				t.Fatal(err)
			}
		})
	}
}
