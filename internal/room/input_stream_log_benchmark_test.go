package room

import (
	"path/filepath"
	"testing"
)

func BenchmarkRoomInputLogStateCommit(b *testing.B) {
	store, err := OpenStore(b.TempDir(), "design-review")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	state := historyRoom(10000, "只读历史上下文与限制")
	state.InputLog = &roomLogRoot{Version: 1}
	if err := store.SaveRoom(state); err != nil {
		b.Fatal(err)
	}
	state, err = store.LoadRoom()
	if err != nil {
		b.Fatal(err)
	}
	writes := 0
	original := store.ops.rename
	store.ops.rename = func(from, to string) error {
		if filepath.Ext(to) == ".jsonl" {
			writes++
		}
		return original(from, to)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		state.Revision++
		if err := store.SaveRoom(state); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if writes != 0 {
		b.Fatal("state commit rewrote Room history")
	}
}
func BenchmarkRoomInputLogReload(b *testing.B) {
	dir := b.TempDir()
	store, err := OpenStore(dir, "design-review")
	if err != nil {
		b.Fatal(err)
	}
	state := historyRoom(10000, "只读历史上下文与限制")
	state.InputLog = &roomLogRoot{Version: 1}
	if err := store.SaveRoom(state); err != nil {
		b.Fatal(err)
	}
	if err := store.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		store, err := OpenStore(dir, state.RoomID)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := store.LoadRoom(); err != nil {
			b.Fatal(err)
		}
		if err := store.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
