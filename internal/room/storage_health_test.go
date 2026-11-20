package room

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/persistence"
)

func TestInputOwnerNonSnapshotUncertaintyFreezesStore(t *testing.T) {
	for _, owner := range []string{"private", "shared"} {
		t.Run(owner, func(t *testing.T) {
			h, a, _, shared := sharedReaders(t)
			independent := openTestStore(t, t.TempDir())
			fault := errors.New("body reclamation result unknown")
			// Exercise each real owner's result boundary with a repository failure
			// which did not originate from the snapshot writer's own I/O.
			var err error
			if owner == "private" {
				err = a.storage.run(h.ctx, "uncertain_body", func(context.Context) error {
					return a.storage.result(errors.Join(inputstream.ErrCommitUnknown, fault))
				})
			} else {
				_, err = sharedOwnerValue(h.ctx, shared, func() (struct{}, error) {
					return struct{}{}, errors.Join(inputstream.ErrCommitUnknown, fault)
				})
			}
			if !errors.Is(err, inputstream.ErrCommitUnknown) || !errors.Is(err, fault) {
				t.Fatal(err)
			}
			if err := h.store.SaveRoom(historyRoom(0, "")); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("snapshot escaped input fence", err)
			}
			if err := h.store.removeSnapshot(filepath.Join(h.store.dir, "absent.json")); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("removal escaped input fence", err)
			}
			if err := h.store.check(false); err != nil {
				t.Fatal("input uncertainty changed Store read admission", err)
			}
			if _, err := h.store.LoadRoom(); err != nil {
				t.Fatal("committed Room could not be read", err)
			}
			if err := independent.SaveRoom(sampleRoom()); err != nil {
				t.Fatal("another Store was frozen", err)
			}
		})
	}
}

func TestStoreCloseSettlesOwnerBeforeSealingWriter(t *testing.T) {
	h, p, _, _ := platformReaderFixture(t)
	entered, block, release := snapshotBarrier(t)
	path := filepath.Join(h.store.dir, "accepted-operation.json")
	operation := make(chan error, 1)
	go func() {
		operation <- p.run(h.ctx, "accepted_multiwrite", func(context.Context) error {
			block()
			return h.store.save(path, "settled", 1024)
		})
	}()
	waitSnapshotBoundary(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- h.store.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.store.inputOwners.mu.Lock()
		closing := h.store.inputOwners.closed
		h.store.inputOwners.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Store did not seal owner admission")
		}
		time.Sleep(time.Millisecond)
	}
	if err := h.store.check(true); err != nil {
		t.Fatal("writer sealed before accepted owner settled", err)
	}
	if h.store.directoryReleased() {
		t.Fatal("directory released before accepted owner settled")
	}
	if other, err := OpenStore(h.store.dir, h.id.RoomID); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("directory reopened before settlement", err)
	}
	release()
	if err := waitSnapshotResult(t, operation); err != nil {
		t.Fatal("accepted owner's final write rejected", err)
	}
	if err := waitSnapshotResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if !h.store.directoryReleased() {
		t.Fatal("settled Store retained directory")
	}
	reopened := openTestStore(t, h.store.dir)
	var value string
	if err := readSnapshot(path, &value, 1024); err != nil || value != "settled" {
		t.Fatal(value, err)
	}
	if err := reopened.save(path, "reopened", 1024); err != nil {
		t.Fatal(err)
	}
}

func TestPersistenceErrorsRetainRoomIdentity(t *testing.T) {
	for _, pair := range [][2]error{{ErrInvalid, persistence.ErrInvalid}, {ErrCommitUnknown, persistence.ErrCommitUnknown}, {ErrStoreClosed, persistence.ErrClosed}} {
		if !errors.Is(pair[0], pair[1]) || !errors.Is(pair[1], pair[0]) || pair[0].Error() != pair[1].Error() {
			t.Fatal("changed error identity", pair)
		}
	}
}
