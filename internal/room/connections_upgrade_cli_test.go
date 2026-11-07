package room

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/connection"
)

func TestConnectionsUpgradeRequiresStoppedServiceAndPreservesReferences(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	c := platformTestSave(t, m, platformTestRequest(m))
	file, _ := m.connections.Load()
	file.Version = 3
	if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.connectionsPathForTest())
	var out, diagnostic bytes.Buffer
	args := []string{"upgrade", "--rooms-dir", m.store.dir}
	if err := runConnectionsUpgrade(args, &out, &diagnostic); err == nil {
		t.Fatal("upgraded a running service")
	}
	after, _ := os.ReadFile(m.connectionsPathForTest())
	if !bytes.Equal(before, after) {
		t.Fatal("lock failure changed snapshot")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if err := runConnectionsUpgrade(args, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.connectionsPathForTest())
	if err != nil {
		t.Fatal(err)
	}
	var upgraded connection.Snapshot
	if json.Unmarshal(data, &upgraded) != nil || upgraded.Version != connection.OAuthSnapshotVersion || len(upgraded.Connections) != 1 || upgraded.Connections[0].ID != c.ID || upgraded.OAuthCredentials[0].AccessToken != file.OAuthCredentials[0].AccessToken {
		t.Fatal("upgrade lost existing authorization")
	}
	if strings.Contains(out.String(), file.OAuthCredentials[0].AccessToken) {
		t.Fatal("upgrade exposed token")
	}
}
