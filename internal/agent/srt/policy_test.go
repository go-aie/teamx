package srt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-aie/teamx/agent"
)

func policyFixture(t *testing.T) (*Factory, agent.ExecutionRequest) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"private", "release", "workspace", "state", "state/home", "state/agents"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f := New(Config{PrivateRoots: []string{filepath.Join(root, "private")}, ReadOnly: []Artifact{{Path: filepath.Join(root, "release")}}, Endpoints: []string{"https://model.example", "https://callback.example:8443"}}).ForSpace("user", "space").(*Factory)
	r := agent.ExecutionRequest{BinaryPath: "", Args: []string{"/release/dsh.mjs", "--profile", "acp"}, Workspace: filepath.Join(root, "workspace"), StateDir: filepath.Join(root, "state"),
		Binding:   agent.ExecutionBinding{UserID: "user", SpaceID: "space", Generation: "state", MemberIdentity: agent.MemberIdentity{RoomIdentity: agent.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "member"}},
		Endpoints: []string{"https://model.example/v1", "https://callback.example:8443/v1/mcp"}, EnvironmentKeys: []string{"OPENAI_API_KEY"}}
	return f, r
}

func TestPolicyBindsExactScopeWithoutCoreWriteAccess(t *testing.T) {
	f, r := policyFixture(t)
	p, err := f.policy(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Network.AllowedDomains, []string{"callback.example:8443", "model.example:443"}) || !p.Network.StrictAllowlist || p.Network.AllowAllUnixSockets || p.Network.AllowLocalBinding || p.AllowNestedUserNamespaces || p.EnableWeakerNetworkIsolation || p.EnableWeakerNestedSandbox {
		t.Fatal("weak effective policy", p)
	}
	if !reflect.DeepEqual(p.Filesystem.DenyRead, []string{"/"}) || slices.Contains(p.Filesystem.AllowWrite, r.StateDir) || slices.Contains(p.Filesystem.AllowRead, r.StateDir) {
		t.Fatal("core checkpoint exposed", p)
	}
	for _, name := range []string{"AGENTS.md", "cordis.patch.yml"} {
		if !slices.Contains(p.Filesystem.DenyWrite, filepath.Join(r.StateDir, "home", name)) {
			t.Fatal("configuration writable")
		}
	}
	before, _ := os.ReadDir(r.StateDir)
	if _, err := f.policy(r); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(r.StateDir)
	if len(before) != len(after) {
		t.Fatal("policy inspection created execution resources")
	}
	for _, mutation := range []func(*agent.ExecutionRequest){
		func(r *agent.ExecutionRequest) { r.Binding.UserID = "other" },
		func(r *agent.ExecutionRequest) { r.Binding.SpaceID = "other" },
		func(r *agent.ExecutionRequest) { r.Binding.Generation = "old" },
		func(r *agent.ExecutionRequest) { r.Binding.MembershipID = "" },
		func(r *agent.ExecutionRequest) { r.Endpoints = []string{"https://unapproved.example"} },
		func(r *agent.ExecutionRequest) { r.Endpoints = []string{"http://127.0.0.1:8088"} },
		func(r *agent.ExecutionRequest) { r.Workspace = filepath.Dir(r.StateDir) },
	} {
		changed := r
		mutation(&changed)
		if _, err := f.policy(changed); err == nil {
			t.Fatal("invalid execution admitted", changed.Binding)
		}
	}
}

func TestPolicyRejectsPreExposedPrivateInode(t *testing.T) {
	f, r := policyFixture(t)
	private := filepath.Join(f.config.PrivateRoots[0], "secret")
	if err := os.WriteFile(private, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(private, filepath.Join(r.Workspace, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.policy(r); err == nil || !strings.Contains(err.Error(), "hardlink") {
		t.Fatal("pre-existing hardlink admitted", err)
	}
	if err := os.Remove(filepath.Join(r.Workspace, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(r.Workspace, "symlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.policy(r); err != nil {
		t.Fatal("symlink was followed by host admission scan", err)
	}
}

func TestSupervisorControlAndEnvironmentArePrivate(t *testing.T) {
	f, r := policyFixture(t)
	r.Environment = []string{"OPENAI_API_KEY=synthetic-model-key", "OTHER_SPACE_KEY=never", "TEAMX_ADMIN_TOKEN=never", "NODE_OPTIONS=never", "HTTP_PROXY=never", "HOME=/host/home", "TEAMX_EXECUTION_TOKEN=synthetic-delegation", "DSH_PERMISSION_MODE=workspace-write"}
	command, err := f.launch(r, false)
	if err != nil {
		t.Fatal(err)
	}
	defer command.Files[0].Close()
	env := strings.Join(command.Environment, "\n")
	for _, forbidden := range []string{"never", "/host/home", "HTTP_PROXY=", "NODE_OPTIONS="} {
		if strings.Contains(env, forbidden) {
			t.Fatal("ambient authority inherited", env)
		}
	}
	if !strings.Contains(env, "OPENAI_API_KEY=synthetic-model-key") || !strings.Contains(env, "TEAMX_EXECUTION_TOKEN=synthetic-delegation") {
		t.Fatal("explicit bindings lost")
	}
	var control struct {
		Policy policy
		Args   []string
	}
	if err := json.NewDecoder(command.Files[0]).Decode(&control); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(control)
	if strings.Contains(string(encoded), "synthetic-model-key") || strings.Contains(strings.Join(command.Args, " "), "synthetic-model-key") {
		t.Fatal("secret in policy/argv")
	}
	if !reflect.DeepEqual(control.Args, r.Args) || !command.Supervised {
		t.Fatal("lost exact arguments or supervision")
	}
	if _, err := os.Stat(command.Files[0].Name()); !os.IsNotExist(err) {
		t.Fatal("control file remains addressable", err)
	}
	for _, name := range []string{"LD_PRELOAD", "HTTP_PROXY", "NO_PROXY", "NODE_USE_ENV_PROXY", "DSH_TEAMX_EXECUTION_PROXY"} {
		r.EnvironmentKeys = []string{name}
		if _, err := f.launch(r, false); err == nil {
			t.Fatal("credential name changed the interpreter or transport", name)
		}
	}
}

func TestDependencyDigestCoversFilesAndRejectsEscapingLinks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "runtime.mjs")
	if err := os.WriteFile(file, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := DigestPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := DigestPath(root)
	if err != nil || first == second {
		t.Fatal("dependency bytes not fingerprinted", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := DigestPath(root); err == nil {
		t.Fatal("unsealed dependency link admitted")
	}
}

func TestUnavailableSRTNeverFallsBackToNative(t *testing.T) {
	f := New(Config{})
	if err := f.Check(context.Background()); err == nil {
		t.Fatal("missing SRT accepted")
	}
	if p, err := f.Start(t.Context(), agent.ExecutionRequest{}, func(error) {}); p != nil || err == nil {
		t.Fatal("unavailable SRT started an execution", p, err)
	}
}
