package room

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestSelectedCatalogReachesManagerStandaloneAndOfflineProjection(t *testing.T) {
	selected, err := platforms.NewCatalogWithTools([]platforms.Tool{{Executable: "missing-local-tool", Definition: tool.Definition{Name: "local"}}}, func() platforms.Definition {
		d := gitlabplatform.Definition()
		d.Notifications.Project = func(notification.Item) notification.View { return notification.View{Provider: "selected-projection"} }
		return d
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := openRoomManagerWithCatalog(t.TempDir(), 7, nil, selected)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	if m.platformCatalog != selected || len(m.toolDefinitions()) != 2 || len(m.notificationAdapters) != 2 {
		t.Fatal("manager replaced selected catalog")
	}
	svc, err := newConfiguredService(t.TempDir(), "standalone", serviceConfig{platformCatalog: selected, unifiedInputs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.close(context.Background())
	store, err := openStoreWithCatalog(t.TempDir(), "offline", nil, selected)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := notification.Item{}
	item.Source.Provider = "gitlab"
	for _, s := range []*Store{m.store, svc.store, store} {
		if s.catalog != selected || s.projectNotification(item).Provider != "selected-projection" || s.notificationCapabilities("lark").Input != nil {
			t.Fatal("store or projection substituted standard capabilities")
		}
	}
}

func TestEmptyCatalogDoesNotAcquireBuiltinCapabilities(t *testing.T) {
	selected, err := platforms.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	m, err := openRoomManagerWithCatalog(t.TempDir(), 7, nil, selected)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	if len(m.toolDefinitions()) != 0 || len(m.notificationAdapters) != 0 || len(m.platformCatalog.Definitions()) != 0 {
		t.Fatal("empty selection acquired built-ins")
	}
}

func TestInputUpgradeUsesSelectedCodecAndCanResumeAfterMissingCapability(t *testing.T) {
	dir, id := upgradeFixtureCopy(t)
	before, err := os.ReadFile(filepath.Join(dir, "room.json"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := platforms.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openStoreWithCatalog(dir, id.RoomID, &inputUpgradeOptions{}, empty)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrVersion) {
		t.Fatal("missing selected codec was not rejected", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "room.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("missing codec changed the authoritative Room", err)
	}
	store, err = openStoreWithCatalog(dir, id.RoomID, &inputUpgradeOptions{}, defaultPlatformCatalog())
	if err != nil {
		t.Fatal("compatible selection could not resume upgrade", err)
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil || state.InputLog == nil || state.RoomIdentity != id.RoomIdentity {
		t.Fatal(state, err)
	}
}
