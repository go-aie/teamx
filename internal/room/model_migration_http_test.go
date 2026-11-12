package room

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelMigrationConflictKeepsAuthenticatedManagementAvailable(t *testing.T) {
	c, _, _ := legacyMigrationHome(t)
	if err := runAccessInit([]string{"--config", c.path}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	c, err := readStartupConfig(c.path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrateModelConfiguration(c, func(int) error { return errors.New("interrupt") }); err == nil {
		t.Fatal("expected interruption")
	}
	original, _ := os.ReadFile(c.path)
	if err = os.WriteFile(c.path, append(original, []byte("\n# concurrent editor\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	var opened atomic.Int32
	factory := controlledActivationFactory(nil, nil)
	info, stop := startTestServer(t, []string{"--config", c.path}, serverOptions{create: func(dir, id string, config serviceConfig) (*service, error) {
		opened.Add(1)
		return factory(dir, id, config)
	}, probe: successfulModelProbe})
	defer stop()
	if info.Status != "migration_required" || info.Ready {
		t.Fatal("migration conflict entered business mode", info)
	}
	client := serverTestCLI(t, filepath.Dir(c.path), info.Endpoint)
	var progress map[string]any
	if err = client.request(context.Background(), "/v1/models", nil, &progress); err != nil {
		t.Fatal(err)
	}
	if progress["setup_required"] != true || progress["migration"] == nil {
		t.Fatal("migration progress unavailable")
	}
	response, err := http.Get(info.Endpoint + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("progress exposed without authentication")
	}
	if err = client.request(context.Background(), "/v1/rooms", nil, &progress); err == nil || !strings.HasPrefix(err.Error(), "busy:") {
		t.Fatal("business routes available during migration", err)
	}
	if opened.Load() != 0 {
		t.Fatal("migration conflict started a Room")
	}
	if err = os.WriteFile(c.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = client.request(context.Background(), "/v1/models/migration/retry", map[string]any{"caller": Caller{Author: Author{Kind: "human", ID: c.Access.OperatorID}}}, &progress); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var state map[string]any
		if client.request(context.Background(), "/v1/setup", nil, &state) == nil && state["setup_required"] == false {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("migration retry did not return to normal startup")
}
