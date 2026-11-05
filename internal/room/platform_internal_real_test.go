//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Read real saved sources using their selected connection. All Room mutations
// occur in a private temporary copy. Never open, replay or rotate a DSH Session.
func TestReferenceInternalRealSources(t *testing.T) {
	if os.Getenv("TEAMX_INTERNAL_REAL") != "1" {
		t.Skip("explicit current data root, Room, member and fresh output required")
	}
	root, output := os.Getenv("TEAMX_INTERNAL_DATA"), os.Getenv("TEAMX_REFERENCE_OUTPUT")
	roomID, membership := os.Getenv("TEAMX_INTERNAL_ROOM"), os.Getenv("TEAMX_INTERNAL_MEMBER")
	if !filepath.IsAbs(root) || !filepath.IsAbs(output) || !safeMembershipID(roomID) || !safeMembershipID(membership) {
		t.Fatal("absolute data/output paths and exact Room/member IDs required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"scenario": "C-current-sources", "status": "failed", "execution_path": "built-in GitLab HTTP; selected saved member binding", "remote_writes": 0, "model": "not involved", "live_recovery": "not attempted; requires a valid member turn", "copy_turn": "synthetic test turn; not a real DSH turn", "recorded_at": time.Now().UTC()}
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(output, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error("report save failed")
		}
		t.Log("retained report:", filepath.Join(output, "report.json"))
	}()
	copyRoot := t.TempDir() // Contains credentials; never retain in the report.
	original := map[string][32]byte{}
	copyFile := func(rel string) {
		t.Helper()
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxPrivateFileBytes {
			t.Fatal("missing, unsafe or oversized input snapshot:", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("cannot read input snapshot:", rel)
		}
		original[rel] = sha256.Sum256(data)
		dest := filepath.Join(copyRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			t.Fatal("cannot copy input snapshot")
		}
	}
	copyFile("rooms.json")
	copyFile("platform-connections.json")
	var index roomsIndex
	if err := readSnapshot(filepath.Join(copyRoot, "rooms.json"), &index, maxRoomsFileBytes); err != nil || index.Version != roomsIndexVersion {
		t.Fatal("current v3 catalog required")
	}
	for _, entry := range index.Rooms {
		for _, name := range []string{"room.json", "room-config.json", "tool-bindings.json"} {
			copyFile(filepath.Join("rooms", entry.RoomID, name))
		}
	}
	roomRel := filepath.Join("rooms", roomID)
	agentRel := filepath.Join(roomRel, "agents", membership)
	copyFile(filepath.Join(agentRel, "agent.json"))
	for _, rel := range []string{filepath.Join(agentRel, "followups"), filepath.Join(roomRel, "inboxes", membership)} {
		err := filepath.WalkDir(filepath.Join(root, rel), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				r, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				copyFile(r)
			}
			return nil
		})
		if err != nil {
			t.Fatal("cannot copy member state")
		}
	}
	// Always check the live files, including failure paths. No Session data is
	// copied, and no writes are made through the original directory's Store.
	defer func() {
		unchanged := true
		for rel, sum := range original {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil || sha256.Sum256(data) != sum {
				unchanged = false
				t.Error("live snapshot changed during acceptance:", rel)
			}
		}
		report["original_snapshots_unchanged"] = unchanged
	}()
	t.Setenv("TEAMX_GITLAB_BIN", filepath.Join(copyRoot, "missing-teamx-gitlab"))
	t.Setenv("TEAMX_GLAB_BIN", filepath.Join(copyRoot, "missing-glab"))
	m, err := newRoomManager(copyRoot, 16)
	if err != nil {
		t.Fatal("current catalog/bindings could not reopen:", err)
	}
	defer func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	}()
	svc, err := startService(m.roomDir(roomID), roomID, serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal("current Room/followups could not reopen:", err)
	}
	m.runtimes[svc.info.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.bindPlatformReferences(svc)
	state, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	var id MemberIdentity
	for _, member := range state.Members {
		if member.MembershipID == membership {
			id = MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: member.AgentID, MembershipID: membership}
		}
	}
	if id.AgentID == "" {
		t.Fatal("selected member not in current roster")
	}
	agent, err := svc.store.LoadAgent(id)
	if err != nil {
		t.Fatal("current agent snapshot failed validation:", err)
	}
	report["member"] = id
	if agent.Turn != nil {
		report["original_turn"] = map[string]any{"number": agent.Turn.Number, "status": agent.Turn.Status}
	}
	var mu sync.Mutex
	requests := []map[string]any{}
	m.platformHTTP = workspaceHTTPFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, fmt.Errorf("acceptance forbids remote writes")
		}
		res, err := http.DefaultTransport.RoundTrip(req)
		row := map[string]any{"method": req.Method, "path": req.URL.EscapedPath(), "query": req.URL.RawQuery}
		if res != nil {
			row["status"] = res.StatusCode
		}
		if err != nil {
			row["error_code"] = "transport_failed"
		}
		mu.Lock()
		requests = append(requests, row)
		mu.Unlock()
		return res, err
	})
	report["requests"] = &requests
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	files, err := filepath.Glob(filepath.Join(copyRoot, agentRel, "followups", "events", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("existing followup sources required")
	}
	rows := []map[string]any{}
	report["sources"] = &rows
	oldEvents := []followupEvent{}
	for _, path := range files {
		var old followupEvent
		if err := readSnapshot(path, &old, maxPrivateFileBytes); err != nil {
			t.Fatal(err)
		}
		if old.Resolution.ObjectVerified {
			continue
		}
		result, access, err := m.resolveGitlabFollowup(ctx, id, old.Source)
		if err != nil {
			t.Fatal("internal real source read failed; no raw transport error retained")
		}
		entry, err := svc.store.inboxEntryLocked(id, old.Seq)
		if err != nil || validateFollowupResolution(entry, result, access) != nil {
			t.Fatal("real resolution evidence rejected")
		}
		rows = append(rows, map[string]any{"seq": old.Seq, "source_ref": old.SourceRef, "delivery_id": old.DeliveryID, "url": old.Source.URL, "before": old.Resolution, "read_only": result})
		oldEvents = append(oldEvents, old)
	}
	if len(oldEvents) == 0 {
		t.Fatal("no failed sources selected")
	}
	// This turn exists ONLY in the temporary copy, to exercise the production
	// CLI/HTTP/Room persistence path without replaying the failed live Session.
	agent.TurnNumber++
	turn := *agent.Turn
	turn.Number, turn.Status, turn.Error = agent.TurnNumber, "running", ""
	agent.Turn = &turn
	if err := svc.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	consumptionPath := filepath.Join(svc.store.dir, "inboxes", membership, "consumption.json")
	consumption, err := os.ReadFile(consumptionPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(m)
	defer server.Close()
	cli := contractCLI(t, server.URL, svc, id)
	objects := map[string]bool{}
	for i, old := range oldEvents {
		var restored followupEvent
		args := []string{"matters", "resolve", "--seq", strconv.FormatUint(old.Seq, 10), "--source", old.SourceRef, "--turn", strconv.FormatUint(agent.TurnNumber, 10)}
		if err := cli(args, &restored); err != nil {
			t.Fatal("copy explicit resolve failed:", err)
		}
		if restored.Seq != old.Seq || restored.SourceRef != old.SourceRef || restored.DeliveryID != old.DeliveryID || restored.Source != old.Source || !restored.Resolution.ObjectVerified || restored.ObjectRef == "" {
			t.Fatal("copy recovery changed source identity or failed to recover object")
		}
		rows[i]["copy_recovered"] = restored.Resolution
		rows[i]["object_ref"] = restored.ObjectRef
		objects[restored.ObjectRef] = true
		if i == 0 {
			var repeated followupEvent
			if err := cli(args, &repeated); err != nil || repeated.ObjectRef != restored.ObjectRef {
				t.Fatal("repeated copy resolve changed identity", err)
			}
		}
		before := len(requests)
		cached, err := svc.resolveFollowupEvent(ctx, id, old.Seq, agent.TurnNumber, false, svc.referenceResolver)
		if err != nil || cached.ObjectRef != restored.ObjectRef || len(requests) != before {
			t.Fatal("ordinary copy reread queried remote", err)
		}
	}
	after, err := os.ReadFile(consumptionPath)
	if err != nil || !bytes.Equal(consumption, after) {
		t.Fatal("copy recovery changed Inbox consumption")
	}
	page, err := svc.store.listFollowups(id, followupQuery{Limit: 25})
	if err != nil || page.Total != len(objects) {
		t.Fatal("copy recovery duplicated objects", err)
	}
	report["copy_object_count"], report["copy_consumption_unchanged"] = len(objects), true
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(filepath.Join(copyRoot, roomRel), roomID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, old := range oldEvents {
		link, err := reopened.readFollowupSource(id, old.Seq, old.SourceRef)
		if err != nil || link.ObjectRef == "" || link.Source != old.Source {
			t.Fatal("copy Store reopen lost recovery", err)
		}
	}
	for _, request := range requests {
		if request["method"] != "GET" || strings.Contains(fmt.Sprint(request["path"]), "__reference") {
			t.Fatal("unexpected request path")
		}
	}
	report["copy_reopen"], report["request_count"], report["status"] = "passed", len(requests), "passed"
}
