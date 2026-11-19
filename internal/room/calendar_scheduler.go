package room

import (
	"context"
	"time"
)

// The index is reconstructed from committed projections on each bounded tick.
// It contains one next wake per calendar; no task enumeration or file I/O runs
// here. Send does not wait for a calendar's disk or Room delivery.
func (c *CalendarService) schedule(ctx context.Context) {
	defer close(c.done)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			c.mu.Lock()
			handles := make([]*calendarHandle, 0, len(c.handles))
			for _, h := range c.handles {
				handles = append(handles, h)
			}
			c.mu.Unlock()
			for _, h := range handles {
				p := h.projection.Load()
				if h.retired.Load() || p == nil || p.frozen || p.next.IsZero() || p.next.After(now) {
					continue
				}
				_ = h.addr.Send(ctx, calendarWake{Boot: h.boot, Revision: p.state.Revision})
			}
		}
	}
}
