package room

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestStaticAssemblySourcesAndCapabilitiesAgree(t *testing.T) {
	catalog := defaultPlatformCatalog()
	// A nil manager intentionally selects offline sources without runtime hooks.
	// Bind an inert host to check the production lifecycle declarations.
	adapters := defaultNotificationAdapters(&roomManager{})
	declared := 0
	for _, typ := range catalog.Types().Types() {
		for _, channel := range typ.NotificationChannels {
			id := notification.SourceID{Provider: typ.Provider, Channel: channel}
			adapter, ok := adapters[id]
			if !ok || adapter.Source == nil || adapter.Source.ID() != id || adapter.Admit == nil {
				t.Fatal("source declaration and lifecycle binding differ", id)
			}
			declared++
		}
	}
	if len(adapters) != declared {
		t.Fatal("undeclared source was registered")
	}
	codecs, err := inputStorageCodecs(adapters, catalog.NotificationCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, codec := range codecs {
		d := codec.Descriptor()
		typ, ok := catalog.Types().LookupType(d.Provider)
		if !ok || !reflect.DeepEqual(typ.NotificationChannels, d.Channels) {
			t.Fatal("source and codec declarations differ", d)
		}
	}
}

func TestOptionalInputCodecDoesNotHideSavedPartition(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	adapters := storageAdapters()
	withoutGitlabCodec := func(provider string) notification.Capabilities {
		caps := notificationCapabilities(provider)
		if provider == "gitlab" {
			caps.Input = nil
		}
		return caps
	}
	// A source without an input codec still participates in notifications. An
	// empty input repository has no GitLab partition and can open normally.
	codecs, err := inputStorageCodecs(adapters, withoutGitlabCodec)
	if err != nil || len(codecs) != 1 || codecs[0].Descriptor().Provider != "lark" {
		t.Fatal(codecs, err)
	}
	owner := inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}
	if _, err := inputstream.OpenRepository(h.store.dir, owner, codecs, inputPersistence{h.store}); err != nil {
		t.Fatal("optional codec blocked an empty repository", err)
	}
	p, err := h.store.openPlatformInputStorage(id, adapters)
	if err != nil {
		t.Fatal(err)
	}
	item := storedGitLab(id, "saved", "events", "7", "42", "saved body must remain protected")
	if _, err := p.Receive(item, time.Now()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(h.store.dir, "inboxes", id.MembershipID, "gitlab", "partition.json")
	before, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputstream.OpenRepository(h.store.dir, owner, codecs, inputPersistence{h.store}); !errors.Is(err, inputstream.ErrVersion) {
		t.Fatal("saved provider partition was silently skipped", err)
	}
	after, err := os.ReadFile(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("missing codec modified saved data", err)
	}
	all, err := inputStorageCodecs(adapters, notificationCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := inputstream.OpenRepository(h.store.dir, owner, all, inputPersistence{h.store})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot("gitlab")
	if err != nil || snapshot.Through != 1 {
		t.Fatal("restored codec lost saved data", snapshot, err)
	}
}
