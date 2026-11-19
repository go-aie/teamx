package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

func TestHistoryImageWaitAllowsAppendConsumeReclaimAndOtherRoom(t *testing.T) {
	h, s := unifiedReaderFixture(t, 3)
	ref := receiveHistoryItem(t, s, historyImageItem(s, "picture", "saved ![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/a.png)"))
	old := []inputstream.Location{}
	for i := range 2 {
		receipt, err := s.storage.Receive(storedLark(s.storage.id, fmt.Sprint(i), "oc_reclaim", "回收测试", "omt", "old saved text"), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		old = append(old, inputstream.Location{Partition: receipt.Partition, Seq: receipt.Seq})
	}
	s = nextHistoryTurn(t, h, s)
	s.images = true
	bID := h.memberID("B", "generation-B")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: bID, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 3}}); err != nil {
		t.Fatal(err)
	}
	bStorage, err := s.service.inputStorage(bID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newInputSession(h.ctx, s.service, bStorage, "session-B", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	other, otherReader := unifiedReaderFixture(t, 3)
	entered, resume := make(chan struct{}), make(chan struct{})
	s.service.materialAttachments = &materialAttachments{download: func(ctx context.Context, _ attachmentRequest, _ *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		close(entered)
		select {
		case <-resume:
			return []byte("pixels"), "image/png", &attachmentAccess{}, nil
		case <-ctx.Done():
			return nil, "", nil, ctx.Err()
		}
	}}
	result := make(chan error, 1)
	go func() {
		data, _ := json.Marshal(historyReadRequest{Stream: ref, Limit: 1})
		_, err := s.historyCall(h.ctx, "image-wait", "history_read", data)
		result <- err
	}()
	select {
	case <-entered:
	case <-h.ctx.Done():
		t.Fatal("image preparation did not start")
	}
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	done := make(chan error, 3)
	go func() {
		_, err := s.storage.Receive(historyImageItem(s, "concurrent", "new append must not enter the fixed History page"), time.Now())
		done <- err
	}()
	go func() {
		err := s.storage.run(h.ctx, "bounded-reclaim", func(context.Context) error {
			if _, err := s.storage.repository.ExcludeCandidates("lark", old, "verified exclusion"); err != nil {
				return err
			}
			facts := inputstream.ReleaseSet{Revision: "verified", Records: map[uint64]inputstream.ReleaseFacts{}}
			for _, l := range old {
				facts.Records[l.Seq] = inputstream.ReleaseFacts{Revision: "verified", SourceConfirmed: true, BusinessReleased: true, ReceiptReleased: true}
			}
			facts, err := s.storage.repository.ConsumptionRelease("lark", facts)
			if err != nil {
				return err
			}
			plan, err := s.storage.repository.PrepareReclaim("lark", facts, time.Now())
			if err != nil {
				return err
			}
			if plan.Through != 2 {
				return fmt.Errorf("reclaim did not reach both candidates: %d", plan.Through)
			}
			_, err = s.storage.repository.CommitReclaim("lark", plan, facts)
			return err
		})
		done <- err
	}()
	go func() {
		_, err := other.addr.Call[ReadPage](other.ctx, ReadMessages{RoomIdentity: other.id, Caller: agentCallerIdentity(otherReader.storage.id), Mode: ReadLatest, Latest: 2})
		done <- err
	}()
	// A member serializes reads for one execution, but another member can
	// formally consume while this member waits on a remote image.
	args, _ := json.Marshal(map[string]string{"stream_ref": roomStreamRef(s.storage.id)})
	_, _, release, busy := s.readStream(h.ctx, "same-member", args)
	if release != nil {
		release()
	}
	if !errors.Is(busy, ErrBusy) {
		t.Fatal("same-member lease was bypassed", busy)
	}
	readInputStream(t, b, "parallel-consume", roomStreamRef(bID), 3, true)
	for range 3 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("image download held a common lock or mutation slot")
		}
	}
	close(resume)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	state, err := s.inputState(h.ctx)
	if err != nil || len(state.Attempts) != 0 || h.list(t).Members[0].ReadThrough != 0 || h.list(t).Members[1].ReadThrough != 3 {
		t.Fatal("History created a formal page", state, err)
	}
	// Replaying the completed observation still uses its captured pre-append body.
	_, raw := historyRead(t, s, "image-wait", ref, 1)
	if strings.Contains(string(raw), "new append") {
		t.Fatal("concurrent append entered cached observation")
	}
}
