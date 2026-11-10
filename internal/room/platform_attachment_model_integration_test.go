//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestModelAttachmentRoomAndInboxShareDownloadWithoutConsumption(t *testing.T) {
	parallelFixtureTest(t)
	h, svc, req, reader, data := modelAttachmentFixture(t)
	beforeRoom, _ := svc.store.LoadRoom()
	beforeAgent, _ := svc.store.LoadAgent(reader.MemberIdentity)
	beforeInbox, err := svc.memberInboxStatus(context.Background(), reader.MemberIdentity)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-platform-secret" {
			t.Error("missing subscription credential")
		}
		h.m.mu.Lock()
		owned := false
		for call := range h.m.platformCalls {
			if call.owner != reader.RoomIdentity || call.platformID == "" {
				t.Error("attachment call escaped its Room or connection")
			}
			if call.tool != "gitlab-token" {
				owned = true
				if call.agentID != reader.AgentID || call.membershipID != reader.MembershipID {
					t.Error("unowned model attachment")
				}
			}
		}
		if !owned {
			t.Error("missing reader-owned attachment call")
		}
		h.m.mu.Unlock()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	for _, inbox := range []bool{false, true} {
		request := req
		if inbox {
			request.AgentID, request.MembershipID = reader.AgentID, reader.MembershipID
		}
		got, mime, access, err := h.m.downloadAttachment(context.Background(), request, &reader, modelAttachmentLimits)
		if err != nil || mime != "image/png" || !bytes.Equal(got, data) || access == nil {
			t.Fatal(mime, err)
		}
		h.m.mu.Lock()
		err = h.m.checkAttachmentAccessLocked(context.Background(), access)
		h.m.mu.Unlock()
		if err != nil {
			t.Fatal("evidence not recheckable before prompt", err)
		}
	}
	afterRoom, _ := svc.store.LoadRoom()
	afterAgent, _ := svc.store.LoadAgent(reader.MemberIdentity)
	afterInbox, err := svc.memberInboxStatus(context.Background(), reader.MemberIdentity)
	if err != nil || calls.Load() != 2 || !reflect.DeepEqual(beforeRoom, afterRoom) || !reflect.DeepEqual(beforeAgent, afterAgent) || !reflect.DeepEqual(beforeInbox, afterInbox) {
		t.Fatal("material read mutated protocol state", err)
	}
	if _, err := os.Stat(filepath.Join(svc.store.dir, "agents", reader.MembershipID, "followups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("download created business state", err)
	}
}

func TestModelAttachmentRejectsIdentityAndTurnScopeBeforeNetwork(t *testing.T) {
	parallelFixtureTest(t)
	h, svc, req, reader, data := modelAttachmentFixture(t)
	var calls atomic.Int32
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	for _, change := range []func(*attachmentRequest, *attachmentReader){
		func(r *attachmentRequest, _ *attachmentReader) { r.ServiceEpoch = "old" },
		func(r *attachmentRequest, _ *attachmentReader) { r.RoomInstance = "other" },
		func(_ *attachmentRequest, r *attachmentReader) { r.RoomID = "other" },
		func(_ *attachmentRequest, r *attachmentReader) { r.MembershipID = strings.Repeat("f", 32) },
		func(_ *attachmentRequest, r *attachmentReader) { r.AgentID = "other" },
		func(_ *attachmentRequest, r *attachmentReader) { r.TurnNumber = 2 },
		func(_ *attachmentRequest, r *attachmentReader) { r.TurnNumber = 0 },
		func(r *attachmentRequest, _ *attachmentReader) { r.Path = "https://evil.invalid" + r.Path },
		func(r *attachmentRequest, _ *attachmentReader) {
			r.Path = strings.Replace(r.Path, "/uploads/", "/-/project/8/uploads/", 1)
		},
		func(r *attachmentRequest, _ *attachmentReader) {
			r.AgentID = reader.AgentID
			r.MembershipID = strings.Repeat("f", 32)
		},
	} {
		r, id := req, reader
		change(&r, &id)
		if got, _, _, err := h.m.downloadAttachment(context.Background(), r, &id, modelAttachmentLimits); err == nil || len(got) != 0 {
			t.Fatal("invalid source/reader accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid read reached GitLab")
	}
	if _, _, _, err := h.m.downloadAttachment(context.Background(), req, &reader, gitlab.UploadLimits{}); err == nil {
		t.Fatal("model read admitted without explicit limits")
	}
	// Even saved, readable notifications outside this exact running snapshot
	// cannot be silently added to the model's material range.
	event := h.event(2, 0)
	event.Kind, event.NoteBody = "note", "![new]("+req.Path+")"
	item := gitlab.EventsMessage(h.state(), gitlab.EventsInboxEntry{DeliveryID: "later-image", Event: event})
	if err := svc.receiveInbox(context.Background(), inboxTarget(reader.MemberIdentity), item); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: req.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source}); err != nil {
		t.Fatal(err)
	}
	for _, inbox := range []bool{false, true} {
		r := req
		r.Seq = 2
		if inbox {
			r.AgentID, r.MembershipID = reader.AgentID, reader.MembershipID
		}
		if _, _, _, err := h.m.downloadAttachment(context.Background(), r, &reader, modelAttachmentLimits); err == nil {
			t.Fatal("expanded running snapshot")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("out-of-range read reached GitLab")
	}
}

func TestModelAttachmentDoesNotPromotePlainOrCodeReferences(t *testing.T) {
	parallelFixtureTest(t)
	h, svc, req, reader, data := modelAttachmentFixture(t)
	event := h.event(2, 0)
	event.Kind, event.NoteBody = "note", "`![code]("+req.Path+")`\n\n[ordinary]("+req.Path+")\n\n"+req.Path
	item := gitlab.EventsMessage(h.state(), gitlab.EventsInboxEntry{DeliveryID: "code-image", Event: event})
	if _, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: req.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source}); err != nil {
		t.Fatal(err)
	}
	agent, _ := svc.store.LoadAgent(reader.MemberIdentity)
	agent.LastOfferedUnreadSeq, agent.Turn.SnapshotSeq, agent.Turn.OfferedUnreadSeq = 2, 2, 2
	if err := svc.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	req.Seq = 2
	var calls atomic.Int32
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	if _, _, _, err := h.m.downloadAttachment(context.Background(), req, &reader, modelAttachmentLimits); err == nil || calls.Load() != 0 {
		t.Fatal("code/plain reference triggered model fetch", err)
	}
	// Manual attachment preview keeps the existing saved-origin contract.
	if got, _, _, err := h.m.downloadAttachment(context.Background(), req, nil, gitlab.UploadLimits{}); err != nil || !bytes.Equal(got, data) || calls.Load() != 1 {
		t.Fatal("manual preview changed", err)
	}
}

func TestModelAttachmentCredentialRotationJoinsBeforeReplacement(t *testing.T) {
	parallelFixtureTest(t)
	h, _, req, reader, data := modelAttachmentFixture(t)
	connection := platformTestList(t, h.m)[0]
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	download := make(chan error, 1)
	go func() {
		got, _, _, err := h.m.downloadAttachment(context.Background(), req, &reader, modelAttachmentLimits)
		if len(got) != 0 {
			err = errors.New("rotated credential image returned")
		}
		download <- err
	}()
	awaitEventSignal(t, started, "download not started")
	update := platformTestRequest(h.m)
	update.Version, update.ReferencedRooms = connection.Version, []RoomIdentity{req.RoomIdentity}
	changed := make(chan error, 1)
	go func() { _, err := testReauthorizePlatform(h.m, update); changed <- err }()
	awaitEventSignal(t, canceled, "credential rotation did not cancel download")
	h.m.mu.Lock()
	file, err := h.m.platforms.Load()
	h.m.mu.Unlock()
	if err != nil || file.Platforms[0].CredentialVersion != connection.CredentialVersion {
		t.Fatal("credential replaced before download settled", err)
	}
	select {
	case err := <-changed:
		t.Fatal("rotation failed to join download", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-download; err == nil {
		t.Fatal("rotated download succeeded")
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
}

func TestModelAttachmentLateTurnResultAndPreparedEvidenceAreRejected(t *testing.T) {
	parallelFixtureTest(t)
	h, svc, req, reader, data := modelAttachmentFixture(t)
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	_, _, access, err := h.m.downloadAttachment(context.Background(), req, &reader, modelAttachmentLimits)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := svc.store.LoadAgent(reader.MemberIdentity)
	agent.Turn.Status = "completed"
	if err := svc.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	err = h.m.checkAttachmentAccessLocked(context.Background(), access)
	h.m.mu.Unlock()
	if err == nil {
		t.Fatal("completed turn retained prepared image access")
	}
	agent.Turn.Status = "running"
	if err := svc.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		agent.Turn.Status = "completed"
		if err := svc.store.SaveAgent(agent); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	if got, _, _, err := h.m.downloadAttachment(context.Background(), req, &reader, modelAttachmentLimits); err == nil || len(got) != 0 {
		t.Fatal("late image escaped invalid turn")
	}
}

func TestModelAttachmentMemberChangeCancelsAndWaits(t *testing.T) {
	parallelFixtureTest(t)
	h, svc, req, reader, data := modelAttachmentFixture(t)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	download := make(chan error, 1)
	go func() {
		got, _, _, err := h.m.downloadAttachment(context.Background(), req, &reader, modelAttachmentLimits)
		if len(got) != 0 {
			err = errors.New("canceled model image returned")
		}
		download <- err
	}()
	awaitEventSignal(t, started, "download not started")
	changed := make(chan error, 1)
	go func() {
		finish, err := h.m.beginPlatformMemberChange(context.Background(), svc, reader.MemberIdentity)
		if finish != nil {
			finish()
		}
		changed <- err
	}()
	awaitEventSignal(t, canceled, "member change did not cancel actual reader")
	select {
	case err := <-changed:
		t.Fatal("member change failed to join download", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-download; err == nil {
		t.Fatal("canceled late response accepted")
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentSubscriptionReplacementCancelsAndWaits(t *testing.T) {
	parallelFixtureTest(t)
	h, _, req, reader, data := modelAttachmentFixture(t)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})
	download := make(chan error, 1)
	go func() {
		got, _, _, err := h.m.downloadAttachment(context.Background(), req, &reader, modelAttachmentLimits)
		if len(got) != 0 {
			err = errors.New("retired image returned")
		}
		download <- err
	}()
	awaitEventSignal(t, started, "download not started")
	state := h.state()
	cfg := state.Config
	cfg.Project = "remote/other"
	changed := make(chan error, 1)
	go func() {
		_, err := h.m.configureEventsSubscription(context.Background(), state.Metadata().Key, state.SubscriptionVersion, cfg)
		changed <- err
	}()
	awaitEventSignal(t, canceled, "retirement did not cancel download")
	select {
	case err := <-changed:
		t.Fatal("replacement failed to join attachment", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-download; err == nil {
		t.Fatal("retired source returned image")
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
}
