package room

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

// An interrupted migration never starts Room recovery. Authentication and this
// exact progress/retry surface remain available without business capabilities.
func serveModelMigrationBlocked(ctx context.Context, listener net.Listener, c startupConfig, access *serverAccess, ready func(any) error) error {
	var mu sync.Mutex
	var workers sync.WaitGroup
	running := false
	saved := make(chan struct{}, 1)
	access.setupRequired = true
	handler := access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/" {
			http.Redirect(w, r, "/access", http.StatusSeeOther)
			return
		}
		if r.Method == "GET" && (r.URL.Path == "/v1/setup" || r.URL.Path == "/v1/models") {
			mu.Lock()
			defer mu.Unlock()
			var migration modelMigration
			_ = readSnapshot(filepath.Join(filepath.Dir(c.path), "model-migration.json"), &migration, maxRoomsFileBytes)
			_ = json.NewEncoder(w).Encode(map[string]any{"setup_required": true, "writable": false, "migration": map[string]any{"completed": migration.Completed, "total": len(migration.Files), "running": running}, "message": "旧模型迁移尚未完成，相关数据恢复与写入已阻止。请核对部署机上的 model-migration.json 与私有备份，处理冲突后重试迁移。"})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/models/migration/retry" {
			mu.Lock()
			defer mu.Unlock()
			if !running {
				running = true
				workers.Add(1)
				go func() {
					defer workers.Done()
					latest, err := readStartupConfig(c.path, true, false)
					if err == nil {
						_, err = migrateModelConfiguration(latest, nil)
					}
					mu.Lock()
					running = false
					mu.Unlock()
					if err == nil {
						select {
						case saved <- struct{}{}:
						default:
						}
					}
				}()
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "applying"})
			return
		}
		writeAPIError(w, ErrBusy)
	}))
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := ready(map[string]any{"endpoint": "http://" + listener.Addr().String(), "status": "migration_required", "ready": false}); err != nil {
		return err
	}
	var result error
	select {
	case <-ctx.Done():
	case <-saved:
		result = errModelSetupSaved
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	err := server.Shutdown(shutdown)
	workers.Wait()
	return errors.Join(result, err)
}
