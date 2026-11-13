package room

import (
	"context"
	"errors"
	"time"

	"github.com/go-aie/actorx"
)

// Only an explicit Room busy rejection proves this attempt did not commit.
// Preserve the exact message ID/content or read receipt on every attempt. A
// lost reply, canceled call or unknown write outcome must return to the caller.
func callRoomWrite[R any](ctx context.Context, room actorx.Addr, msg any) (R, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := room.Call[R](ctx, msg)
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			return result, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}
