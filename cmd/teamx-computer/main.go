// teamx-computer is an optional Linux AIO container helper. Normal TeamX does
// not start, discover, or require this program.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-aie/teamx/internal/computer/aio/runtime"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "session" {
		if len(os.Args) != 7 {
			return fmt.Errorf("invalid internal session invocation")
		}
		index, err := strconv.Atoi(os.Args[3])
		if err != nil {
			return err
		}
		epoch, err := strconv.ParseUint(os.Args[4], 10, 64)
		if err != nil {
			return err
		}
		if len(os.Args[2]) != 32 || index < 0 || index >= 256 || epoch == 0 {
			return fmt.Errorf("invalid internal resource")
		}
		return runtime.RunSession(ctx, runtime.Resource{ID: os.Args[2], Index: index, Epoch: epoch}, os.Args[5], os.Args[6])
	}
	listen := flag.String("listen", "127.0.0.1:18090", "private management listener")
	root := flag.String("state-root", "/var/lib/teamx-computer", "persistent volume")
	runDir := flag.String("run-root", "/run/teamx-computer", "volatile screen sessions")
	tokenPath := flag.String("token-file", "/run/secrets/computer-token", "root-private management credential file")
	toolsPath := flag.String("tools", "/opt/teamx-computer/tools.lock.json", "pinned native catalog")
	versionsPath := flag.String("versions", "/opt/teamx-computer/dependencies.lock.json", "pinned build versions")
	max := flag.Int("max-screens", 2, "maximum concurrent screens (1..256)")
	flag.Parse()
	info, err := os.Lstat(*tokenPath)
	if err != nil {
		return err
	}
	if !credentialOwnedByRoot(info) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return fmt.Errorf("management credential must be a bounded root-owned private regular file")
	}
	raw, err := os.ReadFile(*tokenPath)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 {
		return fmt.Errorf("management credential must contain at least 32 characters")
	}
	contract, policy, err := runtime.LoadCatalog(*toolsPath, *versionsPath)
	if err != nil {
		return err
	}
	driver, err := runtime.NewProcessDriver(*root, *runDir, contract, policy)
	if err != nil {
		return err
	}
	engine, err := runtime.Open(ctx, *root+"/runtime", *max, contract, driver)
	if err != nil {
		return err
	}
	handler, err := runtime.Handler(engine, token)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err = <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
	}
	// Close the listener first so shutdown cannot race new screen admission.
	shutdownCtx, stop := context.WithTimeout(context.Background(), 40*time.Second)
	defer stop()
	if err = server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	return engine.Close(shutdownCtx)
}
