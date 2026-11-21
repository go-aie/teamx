package room

import (
	"errors"
	"path/filepath"
	"slices"
	"sync"

	"github.com/go-aie/teamx/internal/inputstream"
)

// Capture a native publication and its retention boundary under Store.mu.
// Callers release the pin after all reads using the captured root settle.
func (store *Store) pinRoomPublication(id MemberIdentity, bound *inputstream.Snapshot, metadata bool) (RoomSnapshot, *roomLogCache, func(), error) {
	store.mu.Lock()
	var room RoomSnapshot
	err := store.check(false)
	if err == nil {
		err = readSnapshot(filepath.Join(store.dir, "room.json"), &room, maxRoomFileBytes)
	}
	if err == nil && room.RoomIdentity != id.RoomIdentity {
		err = ErrIdentity
	}
	if err == nil && !slices.ContainsFunc(room.Members, func(m MemberState) bool {
		return m.AgentID == id.AgentID && m.MembershipID == id.MembershipID
	}) {
		err = ErrIdentity
	}
	if err == nil && bound != nil && (bound.Partition != roomPartitionRef(room.RoomIdentity) || bound.Reclaimed != room.ClearedThrough || bound.Through > room.LatestSeq) {
		err = ErrReadExpired
	}
	cache := store.roomInputCache
	if err == nil && metadata {
		index := store.roomHistoryIndex
		if index == nil || index.key != roomHistoryKey(room) {
			err = inputstream.ErrCorrupt
		} else {
			room.Messages = index.records
		}
	}
	if err == nil {
		if store.roomHistoryPins == nil {
			store.roomHistoryPins = map[uint64]int{}
		}
		store.roomHistoryPins[room.ClearedThrough]++
	}
	store.mu.Unlock()
	if err != nil {
		return room, nil, nil, err
	}
	release := sync.OnceFunc(func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.roomHistoryPins[room.ClearedThrough]--
		if store.roomHistoryPins[room.ClearedThrough] == 0 {
			delete(store.roomHistoryPins, room.ClearedThrough)
		}
	})
	return room, cache, release, nil
}

// Decode the pinned immutable root outside Store.mu. Embedded messages are
// already captured; segmented messages share the existing immutable log cache.
func (s *Store) readPinnedRoom(room RoomSnapshot, cache *roomLogCache) (RoomSnapshot, error) {
	if room.InputLog != nil {
		if len(room.Messages) != 0 {
			return room, inputstream.ErrCorrupt
		}
		_, err := s.readRoomLogView(&room, cache)
		if err != nil {
			return room, errors.Join(inputstream.ErrCorrupt, err)
		}
	}
	if err := room.validate(); err != nil {
		return room, errors.Join(inputstream.ErrCorrupt, err)
	}
	return room, nil
}

// Caller holds Store.mu. Shared by both native storage formats.
func (s *Store) roomRetentionProtected(through uint64) error {
	for retained, count := range s.roomHistoryPins {
		if count > 0 && through > retained {
			return ErrBusy
		}
	}
	return nil
}
