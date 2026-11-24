package aio_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
	"github.com/go-aie/teamx/internal/identity"
)

// Explicit real AIO acceptance. The launcher creates only disposable resources;
// ordinary go test never discovers Docker or contacts an installed Computer.
func TestRealRuntime(t *testing.T) {
	if os.Getenv("TEAMX_COMPUTER_REAL") != "1" {
		t.Skip("explicit real Runtime acceptance only")
	}
	raw, err := os.ReadFile(os.Getenv("TEAMX_COMPUTER_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := aio.New(os.Getenv("TEAMX_COMPUTER_ENDPOINT"), strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	output := os.Getenv("TEAMX_COMPUTER_EVIDENCE")
	type evidence struct {
		Info      computer.Info         `json:"info"`
		Screens   []computer.ScreenInfo `json:"screens"`
		Marker    string                `json:"marker"`
		Checks    []string              `json:"checks"`
		CrashCall computer.Call         `json:"crash_call"`
	}
	var report evidence
	save := func(name string) {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(output, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := computer.Owner{InstallationID: "acceptance-install", OwnerID: "fictional-owner", ComputerID: "shared-test"}
	phase := os.Getenv("TEAMX_COMPUTER_REAL_PHASE")
	lease, err := p.Claim(ctx, computer.Claim{Owner: owner, Service: "acceptance-" + phase, ExpectedLease: info.Lease})
	if err != nil {
		t.Fatal(err)
	}
	makeCall := func(tool string, args any) computer.Call {
		raw, _ := json.Marshal(args)
		return computer.Call{ID: computer.NewID(), Scope: computer.Scope{Session: "test-session", Turn: "test-turn"}, Tool: tool, Arguments: raw, ContractDigest: info.Contract.Digest}
	}
	invoke := func(s computer.Screen, tool string, args any) computer.Outcome {
		t.Helper()
		out, err := s.Call(ctx, makeCall(tool, args))
		if err != nil {
			t.Fatal(tool, err)
		}
		var result struct {
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(out.Result, &result) != nil || result.IsError {
			t.Fatalf("%s native error: %s", tool, out.Result)
		}
		return out
	}
	nativeText := func(out computer.Outcome) string {
		var doc struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(out.Result, &doc) != nil {
			t.Fatal("invalid native result")
		}
		var texts []string
		for _, b := range doc.Content {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	text := func(s computer.Screen) string { return nativeText(invoke(s, "browser_get_text", map[string]any{})) }
	indexFor := func(s computer.Screen, fragment string) int {
		t.Helper()
		elements := nativeText(invoke(s, "browser_get_clickable_elements", map[string]any{}))
		for _, line := range strings.Split(elements, "\n") {
			if strings.Contains(line, fragment) {
				match := regexp.MustCompile(`^\[(\d+)\]`).FindStringSubmatch(line)
				if len(match) == 2 {
					index, _ := strconv.Atoi(match[1])
					return index
				}
			}
		}
		t.Fatalf("element missing: %s in %s", fragment, elements)
		return 0
	}

	binding := func(agent string) computer.Binding {
		return computer.Binding{Owner: owner, Service: "acceptance-" + phase, Lease: lease, Member: computer.Member{RoomIdentity: identity.RoomIdentity{RoomID: "room", RoomInstance: "room-instance"}, AgentID: agent, MembershipID: "membership-" + agent}}
	}
	if phase == "exercise" {
		report.Info = info
		report.Marker = "marker-" + computer.NewID()
		var wg sync.WaitGroup
		created := make(chan computer.Screen, 6)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := p.EnsureScreen(ctx, binding("A"))
				if err != nil {
					t.Error(err)
					return
				}
				created <- s
			}()
		}
		wg.Wait()
		close(created)
		var a computer.Screen
		for screen := range created {
			if a != nil && screen.Info().ID != a.Info().ID {
				t.Fatal("duplicate screen")
			}
			a = screen
		}
		if a == nil {
			t.Fatal("no screen")
		}
		b, err := p.EnsureScreen(ctx, binding("B"))
		if err != nil {
			t.Fatal(err)
		}
		report.Screens = []computer.ScreenInfo{a.Info(), b.Info()}
		report.Checks = append(report.Checks, "same identity concurrent creation is unique")
		if _, err = p.EnsureScreen(ctx, binding("C")); !computer.Is(err, computer.Capacity) {
			t.Fatalf("capacity: %v", err)
		}
		screens := []computer.Screen{a, b}
		for i, s := range screens {
			label := []string{"A", "B"}[i]
			invoke(s, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/?screen=" + label})
			if !strings.Contains(text(s), "anonymous") {
				t.Fatal("fresh screen not anonymous")
			}
		}
		invoke(a, "browser_gui_execute_action", map[string]any{"action": map[string]any{"action_type": "CLICK", "x": 250, "y": 350}})
		// Native GUI and DOM operations concurrently exercise two distinct screens.
		var results [2]computer.Outcome
		for i, s := range screens {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tool := "browser_form_input_fill"
				args := map[string]any{"selector": "#marker", "value": report.Marker + "-B", "clear": true}
				if i == 0 {
					tool = "browser_gui_execute_action"
					args = map[string]any{"action": map[string]any{"action_type": "TYPING", "text": report.Marker + "-A", "use_clipboard": false}}
				}
				out, err := s.Call(ctx, makeCall(tool, args))
				if err != nil {
					t.Error(err)
				}
				results[i] = out
			}()
		}
		wg.Wait()
		for i, s := range screens {
			label := []string{"A", "B"}[i]
			peer := []string{"B", "A"}[i]
			page := text(s)
			deadline := time.Now().Add(10 * time.Second)
			for !strings.Contains(page, report.Marker+"-"+label) && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				page = text(s)
			}
			if !strings.Contains(page, report.Marker+"-"+label) || strings.Contains(page, report.Marker+"-"+peer) {
				shot := invoke(s, "browser_gui_screenshot", map[string]any{})
				var result struct {
					Content []struct {
						Type string `json:"type"`
						Data string `json:"data"`
					} `json:"content"`
				}
				_ = json.Unmarshal(shot.Result, &result)
				for _, block := range result.Content {
					if block.Type == "image" {
						data, _ := base64.StdEncoding.DecodeString(block.Data)
						_ = os.WriteFile(filepath.Join(output, "focus-failure.png"), data, 0600)
					}
				}
				t.Logf("parallel native outcomes: %s %s", results[0].Result, results[1].Result)
				t.Fatalf("screen crossed: %s", page)
			}
			image := invoke(s, "browser_gui_screenshot", map[string]any{})
			var result struct {
				Content []struct {
					Type string `json:"type"`
					Data string `json:"data"`
				} `json:"content"`
			}
			if json.Unmarshal(image.Result, &result) != nil {
				t.Fatal("invalid native image")
			}
			found := false
			for _, b := range result.Content {
				if b.Type == "image" && len(b.Data) > 100 {
					imageBytes, decodeErr := base64.StdEncoding.DecodeString(b.Data)
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					if err := os.WriteFile(filepath.Join(output, "screen-"+label+".png"), imageBytes, 0600); err != nil {
						t.Fatal(err)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing native image block")
			}
		}
		report.Checks = append(report.Checks, "parallel native GUI and DOM retain private markers", "both native image blocks returned without semantic conversion")
		click := makeCall("browser_click", map[string]any{"index": indexFor(a, `id="count"`)})
		first, err := a.Call(ctx, click)
		if err != nil {
			t.Fatal(err)
		}
		second, err := a.Call(ctx, click)
		if err != nil {
			t.Fatal(err)
		}
		if !first.ResultAvailable || string(first.Result) != string(second.Result) || !strings.Contains(text(a), "Clicked 1") {
			t.Fatal("mutation response replay executed twice")
		}
		changed := click
		changed.Arguments = json.RawMessage(`{"index":999}`)
		if _, err = a.Call(ctx, changed); !computer.Is(err, computer.Invalid) {
			t.Fatal("same ID different args")
		}
		for i, s := range screens {
			label := []string{"A", "B"}[i]
			invoke(s, "browser_form_input_fill", map[string]any{"selector": "[name=account]", "value": "fictional-" + label})
			invoke(s, "browser_click", map[string]any{"index": indexFor(s, "<button>Sign in</button>")})
			if !strings.Contains(text(s), "Account: fictional-"+label) {
				t.Fatal("fictional login failed")
			}
		}
		report.Checks = append(report.Checks, "same mutation ID returns original native result and click count remains one", "fictional login profiles are private")
		if err = a.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text(b), "Account: fictional-B") {
			t.Fatal("stopping A stopped B")
		}
		restored, err := p.EnsureScreen(ctx, binding("A"))
		if err != nil {
			t.Fatal(err)
		}
		invoke(restored, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/?screen=A"})
		if !strings.Contains(text(restored), "Account: fictional-A") || restored.Info().ID != a.Info().ID || restored.Info().Epoch <= a.Info().Epoch {
			t.Fatalf("profile reconstruction: old=%+v new=%+v text=%s", a.Info(), restored.Info(), text(restored))
		}
		if _, err = a.Call(ctx, makeCall("browser_get_text", map[string]any{})); !computer.Is(err, computer.Stale) {
			t.Fatal("old screen epoch accepted")
		}
		report.Screens[0] = restored.Info()
		report.Checks = append(report.Checks, "single screen full stop leaves peer callable", "same profile restores fictional login under a new fenced epoch")
		crash := makeCall("browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/slow?screen=B"})
		report.CrashCall = crash
		callCtx, disconnect := context.WithCancel(ctx)
		returned := make(chan error, 1)
		go func() { _, err := b.Call(callCtx, crash); returned <- err }()
		deadline := time.Now().Add(10 * time.Second)
		for {
			out, err := b.Operation(ctx, crash.ID)
			if err == nil && out.Operation.State == "pending" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("could not observe accepted crash call")
			}
			time.Sleep(20 * time.Millisecond)
		}
		disconnect()
		<-returned
		report.Checks = append(report.Checks, "disconnect retains accepted remote operation pending for crash recovery")
		save("exercise.json")
	} else if phase == "recover" {
		data, err := os.ReadFile(filepath.Join(output, "exercise.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		if report.Info.InstanceID != info.InstanceID || report.Info.BootID == info.BootID {
			t.Fatal("instance or boot identity incorrect")
		}
		for i, old := range report.Screens {
			s, err := p.EnsureScreen(ctx, binding([]string{"A", "B"}[i]))
			if err != nil {
				t.Fatal(err)
			}
			if s.Info().ID != old.ID || s.Info().Epoch <= old.Epoch {
				t.Fatal("screen identity not recovered")
			}
			if _, err = p.Restore(old).Call(ctx, makeCall("browser_get_text", map[string]any{})); !computer.Is(err, computer.Stale) {
				t.Fatal("old Runtime lease survived restart")
			}
			if i == 1 {
				out, err := s.Operation(ctx, report.CrashCall.ID)
				if err != nil {
					t.Fatal(err)
				}
				if out.Operation.State != "unknown" || !out.Operation.Settled || out.ResultAvailable {
					t.Fatalf("crash invented terminal result: %+v", out)
				}
				again, err := s.Call(ctx, report.CrashCall)
				if err != nil || again.Operation.State != "unknown" {
					t.Fatalf("unknown operation replayed: %+v %v", again, err)
				}
			}
			label := []string{"A", "B"}[i]
			invoke(s, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/?screen=" + label})
			// A was flushed before reconstruction; abrupt SIGKILL makes B's most recent
			// cookie durability outside the normal-restart guarantee.
			if i == 0 && !strings.Contains(text(s), "fictional-A") {
				t.Fatal("flushed login lost after Runtime crash")
			}
			report.Screens[i] = s.Info()
		}
		report.Info = info
		report.Checks = append(report.Checks, "Runtime crash recovery preserves instance and profiles with new boot and screen epochs", "old leases rejected; pending mutation remains unknown and settled after UID and port stop proof; never replayed")
		for _, s := range report.Screens {
			if err = p.Restore(s).Stop(ctx); err != nil {
				t.Fatal(err)
			}
		}
		save("recover.json")
	} else {
		t.Fatal("unknown explicit acceptance phase")
	}
}
