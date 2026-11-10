//go:build e2e

package lark

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type realContinuousHost struct{ *stepHarness }

func (*realContinuousHost) Now() time.Time { return time.Now().UTC() }

type realDirectoryPages struct {
	*Client
	pages int
}

func (c *realDirectoryPages) Chats(ctx context.Context, a platform.AccountIdentity, q ChatsRequest) (ChatPage, error) {
	q.PageSize = 20
	c.pages++
	return c.Client.Chats(ctx, a, q)
}

// Explicit opt-in: read all joined conversations in isolated in-memory source
// state. The report contains only aggregate counts; no login mutation or write.
// A short run is measurement, never proof of expiry renewal or full coverage.
func TestRealLarkAllChatsContinuous(t *testing.T) {
	profile := os.Getenv("TEAMX_LARK_TEST_PROFILE")
	if profile == "" || os.Getenv("TEAMX_LARK_ALL_REAL") != "1" {
		t.Skip("set TEAMX_LARK_ALL_REAL=1 and explicit test profile/binary/brand")
	}
	seconds := 180
	if raw := os.Getenv("TEAMX_LARK_RUN_SECONDS"); raw != "" {
		var err error
		seconds, err = strconv.Atoi(raw)
		if err != nil || seconds < 30 || seconds > 1800 {
			t.Fatal("run duration must be 30..1800 seconds")
		}
	}
	c, err := New(platform.CLIProfile{Version: 1, Executable: os.Getenv("TEAMX_LARK_TEST_BIN"), Profile: profile, Brand: os.Getenv("TEAMX_LARK_TEST_BRAND"), CLIVersion: CLIVersion})
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	v, err := c.Verify(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	now := time.Now().UTC()
	binding := sampleBinding()
	binding.Account = v.Account
	state, err := Configure(platform.RoomIdentity{RoomID: "isolated", RoomInstance: "continuous"}, nil, binding, Config{Enabled: true, PlatformID: binding.PlatformID, Private: PrivateConfig{Enabled: true, Scope: "all"}, Target: notification.Target{Mode: "inbox", AgentID: "acceptance", MembershipID: "isolated-member"}}, now)
	if err != nil {
		t.Fatal("isolated source configuration failed")
	}
	host := &realContinuousHost{&stepHarness{s: state, limits: notification.DefaultLimits()}}
	reader := &realDirectoryPages{Client: c}
	source := &Source{Reader: func(QueryScope) (MessageReader, error) { return reader, nil }}
	runner := source.NewRunner(host)
	steps, restarts := 0, 0
	for ctx.Err() == nil {
		// Explicit production scope, retaining the profile verified above.
		host.stepHarness.profile = &c.profile
		delay, stop := runner.Step(ctx)
		steps++
		if ctx.Err() != nil {
			break
		}
		if stop {
			t.Fatalf("continuous scan paused: %s", host.s.ErrorCode)
		}
		if steps%25 == 0 {
			b, _ := json.Marshal(host.s)
			decoded, e := source.Decode(b, host.s.RoomIdentity)
			if e != nil {
				t.Fatal("checkpoint decode failed")
			}
			host.s = decoded.(State)
			runner = source.NewRunner(host)
			restarts++
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	s := host.s
	complete, failed, oldest := 0, 0, int64(0)
	for _, p := range s.Chats {
		if p.Completed > 0 {
			complete++
		}
		if p.ErrorCode != "" {
			failed++
		}
		oldest = max(oldest, time.Now().Unix()-max(s.Baseline.Unix(), p.Completed))
	}
	report := struct {
		CLIVersion        string               `json:"cli_version"`
		Started           time.Time            `json:"started"`
		DurationSeconds   int                  `json:"duration_seconds"`
		Discovered        int                  `json:"discovered_chats"`
		DirectoryComplete bool                 `json:"directory_complete"`
		DirectoryPages    int                  `json:"directory_pages"`
		Checked           int                  `json:"chats_with_completed_window"`
		Failed            int                  `json:"unreadable_chats"`
		OldestLag         int64                `json:"oldest_lag_seconds"`
		Restarts          int                  `json:"checkpoint_restarts"`
		Metrics           notification.Metrics `json:"metrics"`
		LastError         string               `json:"last_error,omitempty"`
	}{CLIVersion: CLIVersion, Started: now, DurationSeconds: seconds, Discovered: len(s.Chats), DirectoryComplete: s.Directory != nil && s.Directory.Scan == nil && !s.Directory.LastFull.IsZero(), DirectoryPages: reader.pages, Checked: complete, Failed: failed, OldestLag: oldest, Restarts: restarts, Metrics: s.Metrics, LastError: s.ErrorCode}
	if path := os.Getenv("TEAMX_LARK_REPORT"); path != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if os.WriteFile(path, append(b, '\n'), 0600) != nil {
			t.Fatal("report save failed")
		}
	}
	t.Logf("duration=%ds discovered=%d directory_complete=%t directory_pages=%d checked=%d unreadable=%d oldest_lag=%ds restarts=%d", seconds, report.Discovered, report.DirectoryComplete, reader.pages, complete, failed, oldest, restarts)
	if !report.DirectoryComplete || complete == 0 {
		t.Fatal("directory/history coverage incomplete; inspect aggregate report")
	}
}
