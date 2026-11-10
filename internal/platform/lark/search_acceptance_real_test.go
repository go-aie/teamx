//go:build e2e && (darwin || linux)

package lark

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type larkSearchAcceptanceInput struct {
	Executable string    `json:"executable"`
	Profile    string    `json:"profile"`
	Brand      string    `json:"brand"`
	AppID      string    `json:"app_id"`
	OpenID     string    `json:"open_id"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Samples    []struct {
		Kind   string `json:"kind"`
		ChatID string `json:"chat_id"`
	} `json:"samples"`
}

func readLarkSearchAcceptanceInput(t *testing.T) larkSearchAcceptanceInput {
	t.Helper()
	inputPath := os.Getenv("TEAMX_LARK_ACCEPTANCE_INPUT")
	if inputPath == "" {
		t.Skip("set TEAMX_LARK_ACCEPTANCE_INPUT to a private fixed-window input (at most one hour)")
	}
	var input larkSearchAcceptanceInput
	raw, err := os.ReadFile(inputPath)
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &input) != nil || !input.End.After(input.Start) || input.End.Sub(input.Start) > time.Hour || input.End.After(time.Now()) || input.Start.Nanosecond() != 0 || input.End.Nanosecond() != 0 {
		t.Fatal("invalid bounded acceptance input")
	}
	if input.Executable == "" {
		input.Executable, err = exec.LookPath("lark-cli")
	}
	if err != nil || !filepath.IsAbs(input.Executable) {
		t.Fatal("acceptance requires an absolute CLI executable")
	}
	return input
}

// This is a bounded production-client comparison in disposable StepHosts, not
// a cutover approval. S1's independent history/thread matrix is still required.
func TestRealLarkSearchRecoveryAndCost(t *testing.T) {
	input := readLarkSearchAcceptanceInput(t)
	output := os.Getenv("TEAMX_LARK_ACCEPTANCE_OUTPUT")
	if !filepath.IsAbs(output) || os.Mkdir(output, 0700) != nil {
		t.Fatal("TEAMX_LARK_ACCEPTANCE_OUTPUT must be a new absolute directory")
	}
	proxy, err := os.ReadFile("testdata/read_metrics.py")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(output, "lark-cli")
	if err = os.WriteFile(bin, proxy, 0700); err != nil {
		t.Fatal(err)
	}
	phase := func(name string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"executable": input.Executable, "profile": input.Profile, "phase": name})
		if err := os.WriteFile(filepath.Join(output, "metrics-config.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	phase("verify")
	profile := platform.CLIProfile{Version: 1, Executable: bin, Profile: input.Profile, Brand: input.Brand, CLIVersion: CLIVersion}
	client, err := New(profile)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	verified, err := client.Verify(ctx)
	if err != nil || verified.Account.ID.Value != input.OpenID || verified.Account.ID.Namespace != "lark:open_id:"+input.AppID {
		t.Fatal("acceptance identity", ErrorCode(err))
	}
	binding := sampleBinding()
	binding.Account = verified.Account
	cfg := Config{Enabled: true, PlatformID: binding.PlatformID, Target: sampleState(t).Config.Target, IntervalSeconds: 60, BackfillStart: input.Start, Private: PrivateConfig{Scope: "specified"}, Groups: GroupConfig{Scope: "specified", MessageMode: "all"}}
	for _, s := range input.Samples {
		if s.Kind == "peer" {
			cfg.Private.Enabled = true
			cfg.Private.ChatIDs = append(cfg.Private.ChatIDs, s.ChatID)
		} else if s.Kind == "group" || s.Kind == "topic" {
			cfg.Groups.Enabled = true
			cfg.Groups.ChatIDs = append(cfg.Groups.ChatIDs, s.ChatID)
		}
	}
	if !cfg.Private.Enabled || !cfg.Groups.Enabled || len(cfg.Groups.ChatIDs) > 8 || len(cfg.Private.ChatIDs) > 2 {
		t.Fatal("supply 1-2 peer samples and 1-8 joined groups")
	}
	owner := sampleState(t).RoomIdentity
	reader := func(QueryScope) (MessageReader, error) { return client, nil }
	sources := []notification.Source{&Source{Reader: reader}, &SearchSource{Source: Source{Reader: reader}}}
	results := map[string]any{}
	accepted := map[string]map[string]bool{}
	locators := map[string]map[string]string{}
	for i, source := range sources {
		name := []string{"history", "search"}[i]
		phase(name)
		var state State
		if i == 0 {
			state, err = Configure(owner, nil, binding, cfg, input.End.Add(time.Second))
		} else {
			state, err = ConfigureSearch(owner, nil, binding, cfg, input.End.Add(time.Second))
		}
		if err != nil {
			t.Fatal("acceptance config", err)
		}
		h := &stepHarness{s: state, now: input.End.Add(time.Second), limits: notification.DefaultLimits(), profile: &profile}
		seen := map[string]bool{}
		revisions := map[string]bool{}
		started := time.Now()
		var first time.Duration
		for step := 0; ; step++ {
			if step >= 3000 || ctx.Err() != nil || h.s.Metrics.Calls > 150 {
				t.Fatal("acceptance read/step/time budget exhausted")
			}
			// Reconstruct from the committed codec after every step, retaining
			// delivered receipts outside the source, as the real Host does.
			raw, _ := json.Marshal(h.s)
			decoded, e := source.Decode(raw, owner)
			if e != nil {
				t.Fatal("acceptance restart", e)
			}
			h.s = decoded.(State)
			_, stop := source.NewRunner(h).Step(ctx)
			if stop || h.s.ErrorCode != "" {
				t.Fatal("acceptance runner stopped", h.s.ErrorCode)
			}
			if h.s.Search != nil {
				for _, b := range h.s.Search.Branches {
					if b.ErrorCode != "" {
						t.Fatal("acceptance branch blocked", b.ErrorCode)
					}
				}
			}
			for _, item := range h.s.PendingItems() {
				if first == 0 {
					first = time.Since(started)
				}
				if seen[item.DeliveryID] {
					t.Fatal("duplicate receipt after restart/overlap")
				}
				seen[item.DeliveryID] = true
				revisions[item.Key] = true
				m := h.s.Inbox[item.Key].Message
				locators[item.Key] = map[string]string{"message_id": m.ID, "chat_id": m.ChatID, "thread_id": m.ThreadID, "parent_id": m.ParentID, "root_id": m.RootID, "create_time": m.Created}
				h.s = h.s.Acknowledge(item.Key).(State)
			}
			if !h.s.LastFull.IsZero() && h.s.LastFull.Unix() >= input.End.Unix() {
				break
			}
		}
		elapsed := time.Since(started)
		var metrics *SearchMetrics
		if h.s.Search != nil {
			metrics = &h.s.Search.Metrics
		}
		results[name] = map[string]any{"seconds": elapsed.Seconds(), "first_source_receipt_seconds": first.Seconds(), "source_receipts": len(seen), "receipts_per_second": float64(len(seen)) / elapsed.Seconds(), "public_reader_calls": h.s.Metrics.Calls, "response_bytes": h.s.Metrics.ResponseBytes, "search_metrics": metrics}
		accepted[name] = revisions
		t.Logf("%s: receipts=%d elapsed=%s first_source_receipt=%s public_calls=%d", name, len(seen), elapsed.Round(time.Millisecond), first.Round(time.Millisecond), h.s.Metrics.Calls)
	}
	// Counts alone never establish coverage. Compare revision sets with the same
	// binding, then use the independent history/thread matrix for acceptance.
	differences := map[string]int{"history_only": 0, "search_only": 0}
	privateDifferences := map[string][]map[string]string{"history_only": {}, "search_only": {}}
	for key := range accepted["history"] {
		if !accepted["search"][key] {
			differences["history_only"]++
			privateDifferences["history_only"] = append(privateDifferences["history_only"], locators[key])
		}
	}
	for key := range accepted["search"] {
		if !accepted["history"][key] {
			differences["search_only"]++
			privateDifferences["search_only"] = append(privateDifferences["search_only"], locators[key])
		}
	}
	file, err := os.Open(filepath.Join(output, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	calls := map[string]map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record struct {
			Phase string `json:"phase"`
			Kind  string `json:"kind"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			t.Fatal("invalid CLI metric record")
		}
		if calls[record.Phase] == nil {
			calls[record.Phase] = map[string]int{}
		}
		calls[record.Phase][record.Kind]++
	}
	if scanner.Err() != nil {
		t.Fatal(scanner.Err())
	}
	if len(accepted["search"]) == 0 {
		t.Fatal("acceptance window has no receipts")
	}
	report, _ := json.MarshalIndent(map[string]any{"window_start": input.Start, "window_end": input.End, "runs": results, "revision_differences": differences, "cli_calls": calls, "remote_writes": 0, "production_cutover": false, "measurement": "disposable StepHost receipts; excludes Inbox/Agent and scheduling waits"}, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "summary.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
	private, _ := json.MarshalIndent(privateDifferences, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "revision-differences.json"), private, 0600); err != nil {
		t.Fatal(err)
	}
	if differences["history_only"] != 0 {
		t.Fatal("search missed revisions accepted by history; review private differences")
	}
}

type privateAppSampleReader struct {
	*Client
	chatID string
	apps   map[string]bool
}

func (r *privateAppSampleReader) BatchMessages(ctx context.Context, account platform.AccountIdentity, ids []string) (MessageBatch, error) {
	batch, err := r.Client.BatchMessages(ctx, account, ids)
	if err == nil {
		for _, m := range batch.Items {
			if m.ChatID == r.chatID && m.Sender.Type == "app" {
				r.apps[m.ID] = true
			}
		}
	}
	return batch, err
}

// Reuse a short fixed window containing the native app-P2P sample. The complete
// all-private branch must settle without treating that candidate as a person.
func TestRealLarkSearchPrivateAppExclusion(t *testing.T) {
	input := readLarkSearchAcceptanceInput(t)
	var botChat string
	for _, sample := range input.Samples {
		if sample.Kind == "bot" {
			botChat = sample.ChatID
			break
		}
	}
	if botChat == "" {
		t.Fatal("private exclusion needs a known app-P2P sample")
	}
	profile := platform.CLIProfile{Version: 1, Executable: input.Executable, Profile: input.Profile, Brand: input.Brand, CLIVersion: CLIVersion}
	client, err := New(profile)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	v, err := client.Verify(ctx)
	if err != nil || v.Account.ID.Value != input.OpenID || v.Account.ID.Namespace != "lark:open_id:"+input.AppID {
		t.Fatal("private exclusion identity", ErrorCode(err))
	}
	binding := sampleBinding()
	binding.Account = v.Account
	cfg := DefaultSearchConfig(binding.PlatformID)
	cfg.Groups.Enabled = false
	cfg.Target = sampleState(t).Config.Target
	cfg.BackfillStart = input.Start
	state, err := ConfigureSearch(sampleState(t).RoomIdentity, nil, binding, cfg, input.End.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	h := &stepHarness{s: state, now: input.End.Add(time.Second), limits: notification.DefaultLimits(), profile: &profile}
	reader := &privateAppSampleReader{Client: client, chatID: botChat, apps: map[string]bool{}}
	source := &SearchSource{Source: Source{Reader: func(QueryScope) (MessageReader, error) { return reader, nil }}}
	for step := 0; ; step++ {
		if step >= 200 || ctx.Err() != nil || h.s.Metrics.Calls > 25 {
			t.Fatal("private exclusion budget exhausted")
		}
		raw, _ := json.Marshal(h.s)
		decoded, err := source.Decode(raw, state.RoomIdentity)
		if err != nil {
			t.Fatal(err)
		}
		h.s = decoded.(State)
		_, stop := source.NewRunner(h).Step(ctx)
		b := h.s.Search.Branches[0]
		if stop || b.ErrorCode != "" {
			t.Fatal("private branch blocked", b.ErrorCode)
		}
		if b.Completed >= input.End.Unix() {
			break
		}
	}
	if len(reader.apps) == 0 || h.s.Search.Metrics.Excluded < uint64(len(reader.apps)) {
		t.Fatal("private exclusion window has no negative sample")
	}
	for _, entry := range h.s.Inbox {
		if entry.Message.ChatID == botChat {
			t.Fatal("app P2P admitted as personnel")
		}
	}
	if _, ok := h.s.Chats[botChat]; ok {
		t.Fatal("native app candidate was interpreted as personnel relationship evidence")
	}
	t.Logf("all-private window completed: app_messages=%d hits=%d excluded=%d admitted=%d verifications=%d remote_writes=0", len(reader.apps), h.s.Search.Metrics.Hits, h.s.Search.Metrics.Excluded, len(h.s.Inbox), h.s.Search.Metrics.Verifications)
}
