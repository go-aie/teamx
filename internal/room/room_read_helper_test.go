package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Native HTTP helper for legacy Room primitive/proof tests; never a CLI entry.
func readRoomPagesForTest(ctx context.Context, client cliClient, req apiRequest, stdout io.Writer, asJSON bool) error {
	for {
		var page ReadPage
		if err := client.request(ctx, "/v1/read", &req, &page); err != nil {
			return fmt.Errorf("read incomplete: %w", err)
		}
		var output bytes.Buffer
		if asJSON {
			var view any = page
			if req.Caller.Kind == "agent" {
				view = projectRoomRead(page)
			}
			if err := json.NewEncoder(&output).Encode(view); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(&output, "Room %s instance=%s snapshot=%d from=%d through=%d has_more=%t unread=%d\n", page.RoomID, page.RoomInstance, page.SnapshotSeq, page.FromExclusive, page.ScannedThrough, page.HasMore, page.UnreadCount)
			for _, m := range page.Messages {
				text := m.Text
				var event *agentRoomEvent
				if req.Caller.Kind == "agent" {
					view := projectRoomMessage(m)
					text, event = view.Text, view.Event
				}
				fmt.Fprintf(&output, "[%d] %s/%s: %s\n", m.Seq, m.Author.Kind, m.Author.ID, text)
				if event != nil {
					data, err := json.Marshal(event)
					if err != nil {
						return err
					}
					fmt.Fprintf(&output, "  event: %s\n", data)
				}
			}
		}
		if err := writeOutput(stdout, output.Bytes()); err != nil {
			return fmt.Errorf("page output incomplete; this page was not confirmed: %w", err)
		}
		if page.ReadReceipt != "" {
			confirm := req
			confirm.ReadReceipt = page.ReadReceipt
			var result ReadConfirmation
			if err := client.request(ctx, "/v1/read/confirm", &confirm, &result); err != nil {
				return fmt.Errorf("page output complete; confirmation failed or uncertain (receipt=%s through=%d); read incomplete: %w", page.ReadReceipt, page.ScannedThrough, err)
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.NextAfter <= page.FromExclusive {
			return fmt.Errorf("read incomplete: non-advancing page")
		}
		next, snapshot := page.NextAfter, page.SnapshotSeq
		req.After, req.SnapshotSeq = &next, &snapshot
		if req.Mode == ReadUnread {
			req.Limit = page.Remaining
		} else {
			req.Mode, req.Latest = ReadRange, 0
		}
	}
}
