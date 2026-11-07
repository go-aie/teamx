//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

var workspaceRealOutput = flag.String("teamx-workspace-real-output", "", "new absolute evidence directory; enables real GitLab/DSH Workspace acceptance")
var workspaceRealRepository = flag.String("teamx-workspace-repository", "", "existing HTTPS GitLab repository; acceptance makes no remote writes")
var workspaceBrowser = flag.Bool("teamx-workspace-browser", false, "wait for actual browser create/discussion/binding instead of DOM driver")

var workspacePreview = flag.Duration("teamx-workspace-preview", 0, "keep successful real server available for browser inspection")

func TestRealWorkspaceGitLabDSH(t *testing.T) {
	if *workspaceRealOutput == "" {
		t.Skip("use -teamx-workspace-real-output and -teamx-workspace-repository for real acceptance")
	}
	w, err := normalizeWorkspace(workspaceConfig{Repository: &workspaceRepository{URL: *workspaceRealRepository}}, false)
	if err != nil || !filepath.IsAbs(*workspaceRealOutput) {
		t.Fatal("new absolute output and valid HTTPS repository required")
	}
	if err := os.Mkdir(*workspaceRealOutput, 0700); err != nil {
		t.Fatal(err)
	}
	if *managementEnvFile != "" {
		t.Setenv("TEAMX_DSH_ENV_FILE", *managementEnvFile)
	}
	if *managementBaseURL != "" {
		t.Setenv("MODEL_BASE_URL", *managementBaseURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(*workspaceRealOutput, "rooms-root")}, output: *workspaceRealOutput, report: map[string]any{"scenario": "Workspace step 5", "status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339)}}
	gitlabSecret := ""
	defer func() {
		if err := d.stopReal(); err != nil {
			t.Error(err)
			d.report["status"] = "failed"
			d.report["cleanupError"] = "server cleanup failed"
		}
		data, err := json.MarshalIndent(d.report, "", "  ")
		for _, secret := range []string{d.secret, gitlabSecret} {
			if secret != "" {
				data = bytes.ReplaceAll(data, []byte(secret), []byte("[REDACTED]"))
			}
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(d.output, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	if err := d.setup(); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	u, _ := url.Parse(w.Repository.URL)
	project := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	platform := u.Scheme + "://" + u.Host
	var profiles map[string]dshProfile
	if err := jsonFile(d.profilePath, &profiles); err != nil {
		t.Fatal(err)
	}
	p := profiles["reviewer"]
	r := teamRuntime{Model: p.Model}
	r.DSH.BinaryPath = p.BinaryPath
	r.DSH.BinaryArgs = p.BinaryArgs
	r.DSH.Artifact = p.Artifact
	role := "You are a bounded Workspace acceptance member. Read each new message with the configured TeamX CLI. Follow its shell instructions exactly. Remain silent publicly. Never access credentials or environment dumps. Never push, commit, create MR, post notes, merge or close anything. Do not delegate."
	team := teamDefinition{ID: "sample", Name: "Workspace 验收", Description: "真实 GitLab/DSH；仅本地文件与远端读取", OpeningMessage: "先讨论，再通过 Workspace 显式绑定。", Runtime: &r, Members: []teamMember{{ID: "developer", Name: "Developer", Instructions: role}, {ID: "reviewer", Name: "Reviewer", Instructions: role}}}
	source := filepath.Join(d.output, "teams", "sample")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(team)
	if err := os.WriteFile(filepath.Join(source, "team.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	oauthFile, oauthConnection := selectedRealOAuthSnapshot(t, platform)
	oauthData, _ := json.Marshal(oauthFile)
	if err := os.WriteFile(filepath.Join(d.dir, "platform-connections.json"), oauthData, 0600); err != nil {
		t.Fatal(err)
	}
	gitlabSecret = oauthFile.OAuthCredentials[0].AccessToken
	epoch, err := d.startManaged(1, filepath.Dir(source))
	if err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	t.Log("actual Workspace server:", d.endpoint, "repository:", w.Repository.URL)
	d.report["endpoint"], d.report["repository"] = d.endpoint, w.Repository.URL
	// Browser mode uses the same production HTTP server and waits for externally
	// observed UI actions. The test does not claim browser evidence on its own.
	uiPhase := func(phase string) error {
		cmd := exec.CommandContext(ctx, "node", "web_workspace_http.mjs")
		cmd.Env = append(operatorEnv(""), "TEAMX_WEB_ENDPOINT="+d.endpoint, "TEAMX_WORKSPACE_UI_PHASE="+phase, "TEAMX_WORKSPACE_REPOSITORY="+w.Repository.URL, "TEAMX_WORKSPACE_GITLAB_URL="+platform, "TEAMX_WORKSPACE_PROJECT="+project, "TEAMX_WORKSPACE_CONNECTION_ID="+oauthConnection.ID)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("app HTTP driver failed: %s", output)
		}
		return nil
	}
	fail := func(err error) {
		if err != nil {
			d.report["error"] = err.Error()
			t.Fatal(err)
		}
	}
	if !*workspaceBrowser {
		fail(uiPhase("create"))
	}
	var a roomSummary
	fail(d.until(func() (bool, error) {
		list, err := d.managedCatalog()
		if err != nil {
			return false, err
		}
		if list.Busy {
			return false, nil
		}
		for _, room := range list.Rooms {
			if room.Name == "Workspace-A" && room.Service != nil {
				a = room.roomSummary
				d.info = *room.Service
				return true, nil
			}
		}
		return false, nil
	}))
	var original managedRoomConfig
	configPath := filepath.Join(d.dir, "rooms", a.RoomID, "room-config.json")
	fail(jsonFile(configPath, &original))
	if original.Workspace == nil || original.Workspace.Repository != nil {
		fail(errors.New("room creation unexpectedly bound repository"))
	}
	for _, member := range original.Members {
		_, err := d.waitAgent(member.AgentID, 1, "completed")
		fail(err)
		cwd := filepath.Join(d.dir, "rooms", a.RoomID, member.Workspace)
		fail(d.until(func() (bool, error) {
			data, err := os.ReadFile(filepath.Join(cwd, "plain-cwd.txt"))
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return strings.TrimSpace(string(data)) == cwd, err
		}))
	}
	d.report["plainDiscussionBeforeBinding"] = true
	oldIDs := map[string]string{}
	for _, member := range original.Members {
		var ref struct {
			ID string `json:"sessionId"`
		}
		fail(jsonFile(filepath.Join(d.dir, "rooms", a.RoomID, "agents", member.MembershipID, "session.json.dsh", "backend.json"), &ref))
		oldIDs[member.AgentID] = ref.ID
	}
	if !*workspaceBrowser {
		fail(uiPhase("bind"))
	} else {
		t.Log("plain discussion verified; apply GitLab binding in browser now")
	}
	var bound managedRoomConfig
	var operation workspaceOperation
	fail(d.until(func() (bool, error) {
		if err := jsonFile(configPath, &bound); err != nil {
			return false, err
		}
		if bound.Workspace == nil || bound.Workspace.Binding == nil {
			return false, nil
		}
		list, err := d.managedCatalog()
		if err != nil {
			return false, err
		}
		info := catalogService(list, a.RoomIdentity)
		if list.Busy || info == nil {
			return false, nil
		}
		d.info = *info
		ws, err := d.realWorkspaceStatus(epoch, a)
		if err != nil {
			return false, err
		}
		if ws.Operation == nil {
			return false, nil
		}
		operation = *ws.Operation
		if operation.Status == "failed" {
			return false, errors.New("binding failed: " + operation.ErrorCode)
		}
		return operation.Status == "completed", nil
	}))
	d.report["bindingTimingsMS"] = operation.Timings
	boundAt := time.Now()
	// Both actual DSH Sessions must report their cwd, origin and tool target.
	code := `import json,os,subprocess; from pathlib import Path; from urllib.parse import urlsplit; origin=subprocess.check_output(['git','remote','get-url','origin'],text=True).strip(); project=urlsplit(origin).path.lstrip('/').removesuffix('.git'); head=subprocess.check_output(['git','ls-remote','origin','HEAD'],text=True).split()[0]; Path('workspace-proof.json').write_text(json.dumps({'cwd':os.getcwd(),'origin':subprocess.check_output(['git','remote','get-url','origin'],text=True).strip(),'project':project,'head':head}))`
	message := "Read the new message with the TeamX CLI. Remain silent publicly. Use bash to execute the following Python code exactly (quote safely):\n" + code + "\nReply privately with WORKSPACE-READY."
	fail(d.command(nil, "send", "--id", "bound-proof", "--text", message))
	newIDs := map[string]string{}
	for _, member := range bound.Members {
		_, err := d.waitAgent(member.AgentID, 2, "completed")
		fail(err)
		var proof struct{ CWD, Origin, Project, Head string }
		cwd := filepath.Join(d.dir, "rooms", a.RoomID, member.Workspace)
		fail(jsonFile(filepath.Join(cwd, "workspace-proof.json"), &proof))
		if proof.CWD != cwd || proof.Origin != w.Repository.URL || proof.Project != project || len(proof.Head) != 40 {
			fail(errors.New("DSH cwd / clone / authenticated Git remote disagree"))
		}
		agentDir := filepath.Join(d.dir, "rooms", a.RoomID, "agents", member.MembershipID)
		var ref dshReference
		fail(jsonFile(filepath.Join(agentDir, "session.json"), &ref))
		var meta struct {
			ID        string `json:"sessionId"`
			Workspace string `json:"workspace"`
		}
		fail(jsonFile(filepath.Join(agentDir, ref.Directory, "backend.json"), &meta))
		if meta.ID == "" || meta.ID == oldIDs[member.AgentID] || meta.Workspace != cwd {
			fail(errors.New("Session not replaced into configured workspace"))
		}
		newIDs[member.AgentID] = meta.ID
		fail(dsh.CheckResume(filepath.Join(agentDir, "session.json.dsh")))
		oldCWD := filepath.Join(d.dir, "rooms", a.RoomID, original.Members[0].Workspace)
		for _, old := range original.Members {
			if old.MembershipID == member.MembershipID {
				oldCWD = filepath.Join(d.dir, "rooms", a.RoomID, old.Workspace)
			}
		}
		if _, err := os.Stat(filepath.Join(oldCWD, "plain-cwd.txt")); err != nil {
			fail(err)
		}
	}
	d.report["newSessionsAndProofMS"] = time.Since(boundAt).Milliseconds()
	d.report["originalSessionIDs"], d.report["boundSessionIDs"] = oldIDs, newIDs
	// Dirty branches and untracked files survive A→B→A and restart. Real
	// transport wrappers are unnecessary: the offline suite separately forbids
	// any platform/fetch/clone call on revisits.
	for _, member := range bound.Members {
		cwd := filepath.Join(d.dir, "rooms", a.RoomID, member.Workspace)
		_, err := runPreparation(ctx, cwd, nil, "git", "checkout", "-b", "teamx-workspace-acceptance")
		fail(err)
		fail(os.WriteFile(filepath.Join(cwd, "untracked-keep.txt"), []byte("keep\n"), 0600))
	}
	var b roomSummary
	fail(d.managementRequest("/v1/rooms/create", roomCreateRequest{ServiceEpoch: epoch, Caller: humanCaller, roomSelection: roomSelection{TeamID: "sample", CreateID: "b", Name: "Workspace-B"}}, &b))
	fail(d.selectManaged(epoch, b, "enter-b"))
	start := time.Now()
	fail(d.selectManaged(epoch, a, "return-a"))
	d.report["returnActivationMS"] = time.Since(start).Milliseconds()
	fail(d.stopReal())
	start = time.Now()
	epoch, err = d.startManaged(2, filepath.Join(d.output, "absent-teams"))
	fail(err)
	d.report["restartMS"] = time.Since(start).Milliseconds()
	list, err := d.managedCatalog()
	fail(err)
	info := catalogService(list, a.RoomIdentity)
	if info == nil {
		fail(errors.New("restart did not restore A"))
	}
	d.info = *info
	for _, member := range bound.Members {
		cwd := filepath.Join(d.dir, "rooms", a.RoomID, member.Workspace)
		branch, err := runPreparation(ctx, cwd, nil, "git", "branch", "--show-current")
		fail(err)
		if string(branch) != "teamx-workspace-acceptance" {
			fail(errors.New("revisit changed branch"))
		}
		data, err := os.ReadFile(filepath.Join(cwd, "untracked-keep.txt"))
		fail(err)
		if string(data) != "keep\n" {
			fail(errors.New("revisit lost files"))
		}
	}
	fail(d.command(nil, "send", "--id", "resume-proof", "--text", message))
	for _, member := range bound.Members {
		_, err := d.waitAgent(member.AgentID, 3, "completed")
		fail(err)
		agentDir := filepath.Join(d.dir, "rooms", a.RoomID, "agents", member.MembershipID)
		var ref dshReference
		fail(jsonFile(filepath.Join(agentDir, "session.json"), &ref))
		var meta struct {
			ID string `json:"sessionId"`
		}
		fail(jsonFile(filepath.Join(agentDir, ref.Directory, "backend.json"), &meta))
		if meta.ID != newIDs[member.AgentID] {
			fail(errors.New("restart changed bound SessionID"))
		}
	}

	// A real clone failure retains its stable target and private OAuth token reference.
	fail(d.selectManaged(epoch, b, "failure-b"))
	bConfigPath := filepath.Join(d.dir, "rooms", b.RoomID, "room-config.json")
	bBefore, err := os.ReadFile(bConfigPath)
	fail(err)
	failureID, err := newOpaqueID()
	fail(err)
	failureTarget := workspaceConfig{Repository: &workspaceRepository{URL: w.Repository.URL, Branch: "teamx-nonexistent-" + failureID}, Binding: &workspaceBinding{Provider: "gitlab", URL: platform, Project: project, ConnectionID: oauthConnection.ID}}
	var failed workspaceOperation
	fail(d.managementRequest("/v1/workspace/apply", workspaceApplyRequest{ServiceEpoch: epoch, RoomIdentity: b.RoomIdentity, Caller: humanCaller, OperationID: failureID, ExpectedVersion: 1, Target: failureTarget, ConnectionVersion: oauthConnection.Version}, &failed))
	waitFailure := func() error {
		return d.until(func() (bool, error) {
			ws, err := d.realWorkspaceStatus(epoch, b)
			if err != nil {
				return false, err
			}
			if ws.Operation == nil {
				return false, nil
			}
			failed = *ws.Operation
			if failed.Status == "completed" {
				return false, errors.New("missing branch unexpectedly cloned")
			}
			catalog, err := d.managedCatalog()
			return failed.Status == "failed" && !catalog.Busy, err
		})
	}
	fail(waitFailure())
	if failed.ErrorCode != "preparation_failed" || failed.Target.Binding.ConnectionID != oauthConnection.ID {
		fail(errors.New("failed binding did not preserve credential reference"))
	}
	marker := filepath.Join(d.dir, "rooms", b.RoomID, failed.Members[0].Workspace) + ".clone-pending"
	markerBefore, err := os.ReadFile(marker)
	fail(err)
	bAfter, err := os.ReadFile(bConfigPath)
	fail(err)
	if !bytes.Equal(bBefore, bAfter) {
		fail(errors.New("failed preparation changed effective config"))
	}
	fail(d.managementRequest("/v1/workspace/retry", map[string]any{"service_epoch": epoch, "room_id": b.RoomID, "room_instance": b.RoomInstance, "caller": humanCaller, "operation_id": failureID}, &failed))
	fail(waitFailure())
	markerAfter, err := os.ReadFile(marker)
	fail(err)
	if !bytes.Equal(markerBefore, markerAfter) {
		fail(errors.New("retry replaced failed clone evidence"))
	}
	fail(d.stopReal())
	epoch, err = d.startManaged(3, filepath.Join(d.output, "absent-teams"))
	fail(err)
	ws, err := d.realWorkspaceStatus(epoch, b)
	fail(err)
	if ws.Active != nil || ws.Operation == nil || ws.Operation.Status != "failed" {
		fail(errors.New("restart resumed failed workspace"))
	}
	d.report["realCloneFailurePreserved"], d.report["explicitRetryPreservedTarget"], d.report["failedRestartBlocked"] = true, true, true
	d.report["failureTimingsMS"] = failed.Timings

	// Check only public/configuration surfaces. Private ACP history may naturally
	// contain tool commands, but must not receive a raw OAuth token either.
	fail(filepath.WalkDir(d.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(d.dir, "credentials") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 16<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(gitlabSecret)) {
			return errors.New("raw OAuth token leaked into Room data")
		}
		return nil
	}))
	if *workspacePreview > 0 {
		t.Log("browser inspection endpoint:", d.endpoint)
		timer := time.NewTimer(*workspacePreview)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
}

func (d *realRoomDriver) realWorkspaceStatus(epoch string, room roomSummary) (workspaceResponse, error) {
	var result workspaceResponse
	q := url.Values{"service_epoch": {epoch}, "caller_id": {humanCaller.ID}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}}
	req, err := http.NewRequestWithContext(d.ctx, http.MethodGet, d.endpoint+"/v1/workspace?"+q.Encode(), nil)
	if err != nil {
		return result, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, fmt.Errorf("workspace status HTTP %d", res.StatusCode)
	}
	err = json.NewDecoder(res.Body).Decode(&result)
	return result, err
}
