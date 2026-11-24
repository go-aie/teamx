package room

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/agent"
)

type boundExecutionFixture struct {
	agent.ExecutionFactory
	user, space string
}

func (f *boundExecutionFixture) ForSpace(user, space string) agent.ExecutionFactory {
	return &boundExecutionFixture{user: user, space: space}
}

func TestAgentExecutionSelectionRejectsBeforeCredentialsAndListener(t *testing.T) {
	for _, source := range []string{
		"[deployment]\nmode='multi-user'\n",
		"[deployment.execution]\nkind='missing'\n",
		"[deployment.execution]\nkind='srt'\nmanifest='/missing/manifest.json'\n",
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		listened := false
		err := runConfiguredServer(t.Context(), []string{"--home", home}, io.Discard, io.Discard, serverOptions{listen: func(string, string) (net.Listener, error) {
			listened = true
			return nil, errors.New("unexpected listener")
		}})
		if err == nil || listened {
			t.Fatal("unsupported execution opened business service", err)
		}
		for _, path := range []string{"auth", "model-credentials", "spaces.json", "users.json"} {
			if _, err := os.Stat(filepath.Join(home, path)); !os.IsNotExist(err) {
				t.Fatal("selection reached private initialization", path, err)
			}
		}
	}
	// Default native construction does not inspect even an invalid SRT path
	// from the ambient environment, and never acquires an execution resource.
	t.Setenv("TEAMX_SRT_CONFIG", "/missing/ambient-srt-config")
	c := startupConfig{}
	if err := selectAgentExecution(context.Background(), &c); err != nil || c.execution != nil {
		t.Fatal("native acquired SRT dependency", err)
	}
}

func TestSpaceAssemblyBindsExecutionAndModelProbeToItsOwnOwner(t *testing.T) {
	r := spaceRuntimeFixture(t)
	config := r.config
	config.execution = &boundExecutionFixture{}
	bound, err := newSpaceRuntime(r.startup, config, r.factories)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close(context.Background())
	factory, ok := bound.config.execution.(*boundExecutionFixture)
	if !ok || factory.user != r.OwnerUserID || factory.space != r.SpaceID || bound.startup.execution != factory || bound.startup.executionBinding.UserID != r.OwnerUserID || bound.startup.executionBinding.SpaceID != r.SpaceID {
		t.Fatal("execution borrowed another scope")
	}
}

func TestMemberExecutionBindingSurvivesLaunchConversion(t *testing.T) {
	dir, id := recoveryFixture(t)
	config := recoveryConfig()
	selected := &boundExecutionFixture{}
	config.execution = selected
	config.ownerUserID = "execution-owner"
	config.dshProfiles = map[string]dshProfile{"execution": {Instructions: "role"}}
	svc, err := newService(dir, id.RoomID, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	member := svc.newMember(id, RunnerConfig{Kind: "dsh", Profile: "execution"}, nil).(*MemberAgent)
	backend := member.Backend.(dshBackend)
	for _, generation := range []string{"session.json.dsh", "session.json.dsh-11111111111111111111111111111111"} {
		opts, err := backend.componentOptions(member.Options, sessionReference{Directory: generation})
		if err != nil {
			t.Fatal(err)
		}
		launch := launchOptions(opts)
		restored := componentOptions(launch)
		if restored.Execution != selected || restored.ExecutionBinding.UserID != config.ownerUserID || restored.ExecutionBinding.MemberIdentity != id || restored.ExecutionBinding.Generation != generation {
			t.Fatal("member/rotation lost execution binding", restored.ExecutionBinding)
		}
	}
}

func TestSettledExecutionDiagnosticsReleaseOnlySettledSpaceOwners(t *testing.T) {
	diagnostic := errors.New("original Session cannot resume")
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "unsettled-sibling"}[mixed], func(t *testing.T) {
			r := spaceRuntimeFixture(t)
			var err error
			r.homeLock, err = lockBundleHome(r.roots.Home)
			if err != nil {
				t.Fatal(err)
			}
			closeErr := error(&settledBackendClose{diagnostic})
			if mixed {
				closeErr = errors.Join(closeErr, context.DeadlineExceeded)
			}
			svc := &service{closed: true, closeError: closeErr}
			r.single = svc
			if err := r.Close(t.Context()); !errors.Is(err, diagnostic) {
				t.Fatal("lost original fault", err)
			}
			if mixed {
				if backendCleanupSettled(closeErr) || r.State() != "closing" || r.homeLock == nil {
					t.Fatal("unsettled owner released")
				}
				svc.closeError = nil
				if err := r.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if !backendCleanupSettled(closeErr) || r.State() != "stopped" || r.homeLock != nil {
				t.Fatal("settled owner retained")
			}
			lock, err := lockBundleHome(r.roots.Home)
			if err != nil {
				t.Fatal("settled root still locked", err)
			}
			unlockDirectory(lock)
		})
	}
}
