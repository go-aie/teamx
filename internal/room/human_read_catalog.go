package room

import (
	"errors"
	"os"
	"path/filepath"
)

// Counts are an HTTP projection, never persisted in rooms.json. A missing
// counts object distinguishes unavailable data from a ready, empty Room.
type humanReadSummary struct {
	Status string `json:"status"`
	*HumanReadCounts
}

// HumanReadCounts is embedded as an optional object so JSON omits every count
// on failure. The exported type also permits decoding the catalog in clients.
type HumanReadCounts struct {
	Revision       uint64 `json:"revision"`
	SnapshotSeq    uint64 `json:"snapshot_seq"`
	ClearedThrough uint64 `json:"cleared_through"`
	UnreadCount    int    `json:"unread_count"`
	MentionCount   int    `json:"mention_count"`
}

// Read the sidecar before the public snapshot so an ordinary concurrent append
// cannot make a receipt appear ahead of history. Retry that boundary mismatch
// at most once. No runtime, humanReadStore mutex, writes or extra history reads
// for individual indicators: the returned snapshot also supplies the preview.
func readHumanReadCatalog(dir string, identity RoomIdentity, operator, displayName string) (RoomSnapshot, error, humanReadSummary) {
	return readHumanReadCatalogFiles(dir, identity, operator, displayName, readSnapshot)
}

func readHumanReadCatalogFiles(dir string, identity RoomIdentity, operator, displayName string, read func(string, any, int64) error) (RoomSnapshot, error, humanReadSummary) {
	unavailable := humanReadSummary{Status: "unavailable"}
	for attempt := 0; ; attempt++ {
		var file humanReadFile
		fileErr := read(filepath.Join(dir, "human-read.json"), &file, maxPrivateFileBytes)
		var room RoomSnapshot
		if err := read(filepath.Join(dir, "room.json"), &room, maxRoomFileBytes); err != nil {
			return room, err, unavailable
		}
		if err := (&Store{dir: dir}).materializeRoomInput(&room); err != nil {
			if attempt == 0 && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return room, err, unavailable
		}
		if room.RoomIdentity != identity || room.Version != formatVersion || room.LatestSeq < room.ClearedThrough ||
			len(room.Messages) > maxMessages || uint64(len(room.Messages)) != room.LatestSeq-room.ClearedThrough {
			return room, ErrInvalid, unavailable
		}
		for i, message := range room.Messages {
			if message.Seq != room.ClearedThrough+uint64(i)+1 || len(message.Mentions) > 0 && !validMentions(message.Text, message.Mentions) {
				return room, ErrInvalid, unavailable
			}
		}
		if errors.Is(fileErr, os.ErrNotExist) {
			file = humanReadFile{RoomIdentity: identity, OperatorID: operator}
		} else if fileErr != nil {
			return room, nil, unavailable
		} else if err := validateHumanReadFile(file, room, operator); err != nil {
			if attempt == 0 && file.RoomIdentity == identity && file.ObservedLatestSeq > room.LatestSeq {
				continue
			}
			return room, nil, unavailable
		}
		state := projectHumanRead(room, file, displayName, false)
		counts := &HumanReadCounts{Revision: state.Revision, SnapshotSeq: state.SnapshotSeq, ClearedThrough: state.ClearedThrough,
			UnreadCount: state.UnreadCount, MentionCount: state.MentionCount}
		return room, nil, humanReadSummary{Status: "ready", HumanReadCounts: counts}
	}
}
