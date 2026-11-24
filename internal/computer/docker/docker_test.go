package docker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeDocker struct {
	state                         State
	volumes                       map[string]map[string]string
	container                     *containerInfo
	creates, starts, stops, pulls int
	key                           string
	fail                          string
	daemon                        string
	classic                       bool
	args                          [][]string
}

func fakeClient(t *testing.T) (*Client, *fakeDocker, State) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "unix:///tmp/teamx-docker-test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	s := NewState(strings.Repeat("a", 64), strings.Repeat("b", 32), "computer-"+strings.Repeat("c", 32), Limits{4, 6})
	f := &fakeDocker{state: s, volumes: map[string]map[string]string{}, daemon: "original-engine"}
	return &Client{Run: f.run}, f, s
}
func encoded(v any) Result { b, _ := json.Marshal(v); return Result{Out: b} }
func (f *fakeDocker) run(ctx context.Context, host string, a []string, input io.Reader) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	f.args = append(f.args, append([]string{}, a...))
	var result Result
	switch strings.Join(a[:min(2, len(a))], " ") {
	case "info --format":
		result = encoded(engineInfo{ID: f.daemon, OSType: "linux", Architecture: "aarch64", NCPU: 8, MemTotal: 8 << 30})
	case "image inspect":
		if f.classic {
			result = encoded([]any{map[string]any{"Id": "sha256:" + strings.Repeat("9", 64), "Os": "linux", "Architecture": "amd64", "RepoDigests": []string{strings.SplitN(f.state.Image.Reference, ":", 2)[0] + "@" + f.state.Image.ID}}})
		} else {
			result = encoded([]map[string]string{{"Id": f.state.Image.ID, "Os": "linux", "Architecture": "amd64"}})
		}
	case "pull --platform":
		f.pulls++
	case "volume inspect":
		name := a[2]
		labels, ok := f.volumes[name]
		if !ok {
			return Result{Code: 1, Err: []byte("no such volume")}, nil
		}
		result = encoded([]any{map[string]any{"Name": name, "Driver": "local", "Labels": labels}})
	case "volume create":
		name := a[len(a)-1]
		f.volumes[name] = map[string]string{ownerLabel: f.state.Owner, creationLabel: f.state.Creation}
		result = Result{Out: []byte(name)}
	case "container inspect":
		if f.container == nil {
			return Result{Code: 1, Err: []byte("No such container")}, nil
		}
		result = encoded([]containerInfo{*f.container})
	default:
		switch a[0] {
		case "create":
			f.creates++
			v := &containerInfo{ID: strings.Repeat("d", 64), Image: f.state.Image.ID}
			if f.classic {
				v.Image = "sha256:" + strings.Repeat("9", 64)
			}
			v.Config.Labels = map[string]string{ownerLabel: f.state.Owner, creationLabel: f.state.Creation}
			v.Config.Entrypoint = []string{"/usr/local/bin/teamx-computer"}
			v.Config.Cmd = []string{"-listen", "0.0.0.0:18090", "-token-file", "/run/computer-token", "-max-screens", "256"}
			init := true
			v.HostConfig.Init = &init
			v.HostConfig.RestartPolicy.Name = "no"
			v.HostConfig.Memory = 6 << 30
			v.HostConfig.NanoCpus = 4e9
			v.HostConfig.ShmSize = 1 << 30
			v.HostConfig.SecurityOpt = []string{"no-new-privileges"}
			v.HostConfig.NetworkMode = "default"
			// Decode through JSON to avoid unrelated anonymous struct coupling.
			raw, _ := json.Marshal(map[string]any{"Mounts": []any{map[string]any{"Type": "volume", "Name": f.state.Name + "-state", "Destination": "/var/lib/teamx-computer", "RW": true}, map[string]any{"Type": "volume", "Name": f.state.Name + "-files", "Destination": "/workspace", "RW": true}}, "HostConfig": map[string]any{"PortBindings": map[string]any{"18090/tcp": []any{map[string]string{"HostIP": "127.0.0.1", "HostPort": ""}}}}, "NetworkSettings": map[string]any{"Ports": map[string]any{"18090/tcp": []any{map[string]string{"HostIP": "127.0.0.1", "HostPort": "18123"}}}}})
			_ = json.Unmarshal(raw, v)
			f.container = v
			result = Result{Out: []byte(v.ID)}
		case "cp":
			tr := tar.NewReader(input)
			h, err := tr.Next()
			if err != nil {
				return Result{}, err
			}
			if h.Mode != 0600 || h.Uid != 0 || h.Gid != 0 || h.Name != "computer-token" {
				return Result{}, errors.New("unsafe token archive")
			}
			b, err := io.ReadAll(tr)
			if err != nil {
				return Result{}, err
			}
			f.key = string(b)
		case "start":
			f.starts++
			f.container.State.Running = true
			f.container.State.Pid = 123
		case "stop":
			f.stops++
			f.container.State.Running = false
			f.container.State.Pid = 0
		default:
			return Result{}, errors.New("unexpected fake Docker operation: " + strings.Join(a, " "))
		}
	}
	if f.fail == a[0] {
		f.fail = ""
		return Result{}, errors.New("lost response")
	}
	return result, nil
}
func prepare(t *testing.T, c *Client, s *State) {
	t.Helper()
	for range 16 {
		done, err := c.Step(context.Background(), s, strings.Repeat("secret", 8))
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("preparation never completed")
}
func TestManagedContainerLifecyclePreservesVolumesAndPrivateKey(t *testing.T) {
	c, f, s := fakeClient(t)
	prepare(t, c, &s)
	if f.creates != 1 || f.starts != 1 || s.Endpoint != "http://127.0.0.1:18123" || !s.TokenInstalled || f.key != strings.Repeat("secret", 8) {
		t.Fatalf("incomplete lifecycle: %+v", s)
	}
	for _, args := range f.args {
		if strings.Contains(strings.Join(args, " "), f.key) {
			t.Fatal("key in command args")
		}
	}
	if err := c.Stop(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Stopped || f.stops != 1 || len(f.volumes) != 2 {
		t.Fatal("stop discarded persistence")
	}
	prepare(t, c, &s)
	if s.Stopped || f.creates != 1 || f.starts != 2 {
		t.Fatal("re-enable did not reuse original container")
	}
}
func TestLostCreateAndStartResponsesReconcileOriginalResources(t *testing.T) {
	for _, operation := range []string{"volume", "create", "cp", "start"} {
		t.Run(operation, func(t *testing.T) {
			c, f, s := fakeClient(t)
			f.fail = operation
			for range 16 {
				done, err := c.Step(context.Background(), &s, strings.Repeat("secret", 8))
				if err != nil {
					continue
				}
				if done {
					break
				}
			}
			if s.Endpoint == "" || f.creates != 1 || f.starts != 1 {
				t.Fatal("lost response duplicated resources")
			}
		})
	}
}
func TestForeignOrChangedResourcesAreNeverStartedOrStopped(t *testing.T) {
	for _, mutation := range []string{"label", "id", "volume", "image", "mount", "daemon", "deleted_container", "deleted_volume"} {
		t.Run(mutation, func(t *testing.T) {
			c, f, s := fakeClient(t)
			prepare(t, c, &s)
			switch mutation {
			case "label":
				f.container.Config.Labels[ownerLabel] = "someone-else"
			case "id":
				f.container.ID = strings.Repeat("e", 64)
			case "volume":
				f.volumes[s.Name+"-state"][ownerLabel] = "someone-else"
			case "image":
				f.container.Image = "sha256:" + strings.Repeat("f", 64)
			case "mount":
				f.container.Mounts[0].Name = "other-data"
			case "daemon":
				f.daemon = "replacement-engine"
			case "deleted_container":
				f.container = nil
			case "deleted_volume":
				delete(f.volumes, s.Name+"-files")
			}
			if _, err := c.Step(context.Background(), &s, "unused"); err == nil {
				t.Fatal("changed resource accepted")
			}
			if err := c.Stop(context.Background(), &s); err == nil {
				t.Fatal("changed resource stopped")
			}
			if f.creates != 1 || f.stops != 0 {
				t.Fatal("foreign resource mutated")
			}
		})
	}
}
func TestEnvironmentCheckIsReadOnlyAndRejectsRemoteDocker(t *testing.T) {
	c, f, _ := fakeClient(t)
	if e, err := c.Check(context.Background()); err != nil || !e.Emulated {
		t.Fatal(e, err)
	}
	if f.creates+f.starts+f.pulls != 0 || len(f.volumes) != 0 {
		t.Fatal("check changed Docker")
	}
	t.Setenv("DOCKER_HOST", "ssh://remote")
	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("remote daemon accepted")
	}
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("TCP daemon accepted")
	}
}

func TestRegistryDigestPinsClassicAndContainerdImageStores(t *testing.T) {
	c, f, s := fakeClient(t)
	f.classic = true
	prepare(t, c, &s)
	if s.ResolvedImage == s.Image.ID || s.ResolvedImage != f.container.Image {
		t.Fatal("classic config ID confused with registry manifest digest")
	}
	if err := c.Stop(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
}
