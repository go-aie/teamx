//go:build darwin || linux

package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Background retention and the next upgrade serialize on the same installation
// lock. Waiting never stops the service, and cancellation leaves it untouched.
func acquireServiceMaintenance(ctx context.Context, root string, wait bool, output io.Writer) (*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var announced bool
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := lockDirectoryFile(root, ".service.lock")
		if !wait || !errors.Is(err, ErrLocked) {
			return lock, err
		}
		if !announced {
			fmt.Fprintln(output, "正在等待其他服务维护任务完成；服务继续运行。")
			announced = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
