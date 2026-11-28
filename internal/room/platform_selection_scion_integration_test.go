//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/scion"
)

func TestScionRepositorySelectionCLI(t *testing.T) {
	m, svc, req, _, _ := scionRepositorySelectionFixture(t)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, MemberState{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID})
	run := func(args []string) platformSelectionView {
		t.Helper()
		var out bytes.Buffer
		if err := runPlatformSelectionCLI(t.Context(), args, &out, &out, env, client); err != nil {
			t.Fatal(err, out.String())
		}
		var v platformSelectionView
		if json.Unmarshal(out.Bytes(), &v) != nil {
			t.Fatal(out.String())
		}
		return v
	}
	v := run([]string{"start", "--provider", "scion", "--kind", "repositories", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--notify", "inbox"})
	v = run([]string{"update", "--operation-id", req.OperationID, "--version", strconv.FormatUint(v.Version, 10), "--repository", "Owner/repo", "--repository-created-at-ms", "100"})
	if v.Draft.Repository == nil || v.Draft.Repository.Name != "Owner/repo" || v.Draft.Repository.Created != 100 {
		t.Fatal(v)
	}
	q := selectionQuery(req, true)
	q.Version = v.Version
	if done := selectionCall(t, m, "submit", q, true); done.Status != "completed" {
		t.Fatal(done)
	}
}

func scionRepositorySelectionFixture(t *testing.T) (*roomManager, *service, platformSelectionRequest, scionRepositoriesRequest, string) {
	t.Helper()
	m, _, svc, c, id, dir := scionNotificationHarness(t)
	branch := `
if args[:3]==['--json','repo','ls']:
 open(base+'/repository-started','w').write('1')
 if mode=='block-repositories':
  while not os.path.exists(base+'/release'):time.sleep(.01)
 limit=int(args[args.index('--limit')+1]);prefix=args[-1]
 repos=[{'schema':'scion.cli.repo/v1','name':'Owner/repo','created_at_ms':100,'description':'Issue tracker'}]
 repos += [{'schema':'scion.cli.repo/v1','name':'Owner/z'+str(i).zfill(3),'created_at_ms':200+i,'description':''} for i in range(100)]
 repos=[r for r in repos if r['name'].startswith(prefix)]
 print(json.dumps({'schema':'scion.cli.repos/v1','repos':repos[:limit],'truncated':len(repos)>limit}));sys.exit()
if mode=='recreated' and args[:3]==['--json','repo','info']:
 print(json.dumps({'schema':'scion.cli.repo/v1','name':args[3],'created_at_ms':101}));sys.exit()
`
	native := strings.Replace(scionNotificationFixture, "if args[:3]==['--json','repo','info']:", branch+"\nif args[:3]==['--json','repo','info']:", 1)
	if err := os.WriteFile(filepath.Join(dir, "scn"), []byte(native), 0700); err != nil {
		t.Fatal(err)
	}
	member := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	pick, _ := newOpaqueID()
	selection := platformSelectionRequest{ServiceInfo: svc.info, Caller: member.Caller, OperationID: pick, SourceSeq: member.SourceSeq, Input: &platformSelectionInput{Provider: "scion", Kind: "repositories", PlatformID: c.ID, PlatformVersion: c.Version}}
	req := scionRepositoriesRequest{ServiceEpoch: m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, PlatformID: c.ID, PlatformVersion: c.Version, Target: platformMemberTarget(id)}
	return m, svc, selection, req, dir
}

func TestScionRepositorySelectionSharedDirectoryAndImmutableRecovery(t *testing.T) {
	m, _, selection, req, _ := scionRepositorySelectionFixture(t)
	w := managerHTTP(m, "/v1/platform/scion/repositories", req)
	var page scion.RepositoryPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Repositories) != 100 || !page.Truncated || page.NextPageToken != "200" {
		t.Fatal(w.Code, w.Body.String())
	}
	start := selectionCall(t, m, "start", selection, false)
	q := selectionQuery(selection, true)
	w = managerHTTP(m, "/v1/platforms/selection-interactions/candidates", q)
	var cardPage scion.RepositoryPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cardPage) != nil || !reflect.DeepEqual(page, cardPage) {
		t.Fatal(w.Code, w.Body.String())
	}
	q.PageToken = "200"
	w = managerHTTP(m, "/v1/platforms/selection-interactions/candidates", q)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cardPage) != nil || len(cardPage.Repositories) != 101 || cardPage.Truncated {
		t.Fatal(w.Code, w.Body.String())
	}
	q.Query = "ISSUE"
	q.PageToken = ""
	w = managerHTTP(m, "/v1/platforms/selection-interactions/candidates", q)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cardPage) != nil || len(cardPage.Repositories) != 1 || cardPage.Repositories[0].Name != "Owner/repo" || !cardPage.Truncated {
		t.Fatal(w.Code, w.Body.String())
	}
	q.Query = ""
	q.Version = start.Version
	q.Draft = &platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: "Owner/repo", Created: 100}}
	edit := selectionCall(t, m, "update", q, true)
	q.Version = edit.Version
	q.Draft = nil
	done := selectionCall(t, m, "submit", q, true)
	if done.Status != "completed" || done.ResultVersion != 1 || done.Draft.Repository.Name != "Owner/repo" {
		t.Fatal(done)
	}
	again := selectionCall(t, m, "submit", q, true)
	if !reflect.DeepEqual(done, again) {
		t.Fatal("replay changed result")
	}
	disk, err := m.readPlatformSelectionLocked(done.ID)
	if err != nil || disk.Status != "completed" || *disk.Draft.Repository != *done.Draft.Repository {
		t.Fatal(disk, err)
	}
	// The shared result path wakes the Agent, but selection does not apply settings.
	m.mu.Lock()
	states := m.notifications.StatesLocked(req.RoomIdentity)
	m.mu.Unlock()
	if len(states) != 0 {
		t.Fatal("selection created subscription")
	}
	q.Draft = &platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: "Owner/other", Created: 100}}
	q.Version = done.Version
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/update", q); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bad := range []scionRepositoriesRequest{
		func() scionRepositoriesRequest { v := req; v.ServiceEpoch = "old"; return v }(),
		func() scionRepositoriesRequest { v := req; v.PlatformVersion++; return v }(),
		func() scionRepositoriesRequest { v := req; v.Target.MembershipID = "other"; return v }(),
		func() scionRepositoriesRequest { v := req; v.PageToken = "2001"; return v }(),
	} {
		if w := managerHTTP(m, "/v1/platform/scion/repositories", bad); w.Code == 200 {
			t.Fatal("invalid request accepted", bad)
		}
	}
}

func TestScionRepositorySelectionNestedNamespace(t *testing.T) {
	m, _, selection, _, dir := scionRepositorySelectionFixture(t)
	const name = "OpenCSG/csghub/csghub-portal"
	path := filepath.Join(dir, "scn")
	native, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, bytes.ReplaceAll(native, []byte("Owner/repo"), []byte(name)), 0700); err != nil {
		t.Fatal(err)
	}
	start := selectionCall(t, m, "start", selection, false)
	q := selectionQuery(selection, true)
	for _, query := range []string{"OpenCSG", "OpenCSG/csghub/"} {
		q.Query = query
		w := managerHTTP(m, "/v1/platforms/selection-interactions/candidates", q)
		var page scion.RepositoryPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Repositories) != 1 || page.Repositories[0].Name != name {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	q.Query = ""
	q.Version = start.Version
	q.Draft = &platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: name, Created: 100}}
	edit := selectionCall(t, m, "update", q, true)
	q.Version, q.Draft = edit.Version, nil
	done := selectionCall(t, m, "submit", q, true)
	if done.Status != "completed" || done.Draft.Repository == nil || done.Draft.Repository.Name != name {
		t.Fatal(done)
	}
}

func TestScionRepositorySelectionRejectsRecreatedAndChangedDrafts(t *testing.T) {
	for _, fault := range []string{"recreated", "draft", "account"} {
		t.Run(fault, func(t *testing.T) {
			m, _, selection, _, dir := scionRepositorySelectionFixture(t)
			start := selectionCall(t, m, "start", selection, false)
			q := selectionQuery(selection, true)
			q.Version = start.Version
			q.Draft = &platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: "Owner/repo", Created: 100}}
			edit := selectionCall(t, m, "update", q, true)
			q.Version = edit.Version
			q.Draft = nil
			if fault == "recreated" {
				if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("recreated"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if fault == "draft" {
				newer := q
				newer.Draft = &platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: "Owner/other", Created: 100}}
				selectionCall(t, m, "update", newer, true)
			} else {
				m.mu.Lock()
				file, err := m.platforms.Load()
				if err == nil {
					for i := range file.Platforms {
						if file.Platforms[i].ID == selection.Input.PlatformID {
							file.Platforms[i].Version++
						}
					}
					err = m.store.save(m.platformsPathForTest(), file, 2<<20)
				}
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", q); w.Code != 409 {
				t.Fatal(fault, w.Code, w.Body.String())
			}
			disk, err := m.readPlatformSelectionLocked(selection.OperationID)
			if err != nil || disk.Status != "waiting" || disk.ResultVersion != 0 {
				t.Fatal(disk, err)
			}
		})
	}
}

func TestScionRepositoryCandidatesRecheckDraftAfterNativeRead(t *testing.T) {
	m, _, selection, _, dir := scionRepositorySelectionFixture(t)
	start := selectionCall(t, m, "start", selection, false)
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("block-repositories"), 0600); err != nil {
		t.Fatal(err)
	}
	q := selectionQuery(selection, true)
	done := make(chan int, 1)
	go func(read platformSelectionRequest) {
		done <- managerHTTP(m, "/v1/platforms/selection-interactions/candidates", read).Code
	}(q)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "repository-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native read did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	q.Version = start.Version
	q.Draft = &platformSelectionDraft{Repository: &scion.RepositoryEvidence{Name: "Owner/other", Created: 100}}
	selectionCall(t, m, "update", q, true)
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 409 {
			t.Fatal(code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not finish")
	}
}
