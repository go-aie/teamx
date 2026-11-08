package room

import (
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

var modelAttachmentLimits = gitlab.UploadLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20}

func modelAttachmentFixture(t *testing.T) (*eventHarness, *service, attachmentRequest, attachmentReader, []byte) {
	t.Helper()
	h, svc, id, query, data := attachmentFixture(t)
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 1, LastOfferedInboxSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1, OfferedUnreadSeq: 1, InboxSnapshotSeq: 1}}
	if err := svc.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	return h, svc, attachmentRequest{ServiceInfo: svc.info, Seq: 1, Path: query.Get("path")}, attachmentReader{MemberIdentity: id, TurnNumber: 1}, data
}
