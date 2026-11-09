//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Controlled source samples; real production prompt construction, installed
// DSH/ACP/model and normal Session recovery. No platform or business writes.
func TestDSHTimeRealModel(t *testing.T) {
	if os.Getenv("TEAMX_TIME_DSH_REAL") != "1" {
		t.Skip("set TEAMX_TIME_DSH_REAL=1, TEAMX_TIME_CONFIG and fresh TEAMX_DSH_OUTPUT")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "controlled material samples; real DSH/model, next-turn zone change and normal recovery", "external_business_writes": 0, "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, output, report)
	config, err := loadStartupConfig(os.Getenv("TEAMX_TIME_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		t.Fatal("runtime config required", err)
	}
	runtime := config.Runtime
	key := runtime.Model.APIKeyEnv
	if value := config.Env[key]; os.Getenv(key) == "" && value != "" {
		t.Setenv(key, value)
	}
	imageRealCredential(t, key)
	report["model"] = runtime.Model.ModelID
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(output, "room"))
	state := historyRoom(0, "")
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	settings := settingsFromFile(t, filepath.Join(output, "config.toml"), false)
	created, _ := time.Parse(time.RFC3339Nano, "2026-11-08T17:04:13.09Z")
	backend := dshBackend{store: store, settings: settings, turnTimeout: 2 * time.Minute,
		profile: dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(),
			Instructions: `This is a bounded, read-only time interpretation acceptance. Do not use tools, read files, delegate, send messages or perform actions. Reply only with a JSON object: event_local is the supplied material header's event time in the current system time zone, offset_event_local is the offset_event value converted to the current system time zone (both use RFC3339 with fractional seconds preserved), event_description is a short Chinese description of the event's local date and time, ambiguous_is_unknown is true if the time zone of ambiguous_text cannot be determined. Ordinary output is the requested private answer. Use the supplied system time convention and turn-start reference.`},
		prepare: func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error) {
			return &dshMaterials{groups: []materialGroup{{notice: materialNotice{request: attachmentRequest{Seq: 1}, author: "controlled-sample", created: created,
				text: "offset_event=2026-11-08T19:04:13.09+02:00\nambiguous_text=17:04（未标明时区）"}}}, check: func(context.Context) error { return nil }}, nil
		}}
	if err := backend.profile.checkCredentials(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	opts := adapterOptions(store, id)
	session, err := backend.OpenSession(ctx, opts)
	if err != nil {
		if session != nil {
			_ = session.Close(ctx)
		}
		t.Fatal(err)
	}
	defer func() {
		if session != nil {
			if err := session.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}()
	var turns []map[string]any
	report["turns"] = &turns
	run := func(zone, expected string, full bool) {
		t.Helper()
		ds := session.(*dshSession)
		actual := ds.component
		row := map[string]any{"zone": zone}
		turns = append(turns, row)
		ds.component = &adapterComponent{images: actual.SupportsImages(), runPrompt: func(ctx context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
			row["prompt"] = blocks
			assertTimeSupply(t, blocks[0].Text, zone, full)
			if !strings.Contains(blocks[2].Text, "time=2026-11-08T17:04:13.09Z;") || !strings.Contains(blocks[2].Text, "19:04:13.09+02:00") {
				t.Fatal("source time was rewritten")
			}
			result, err := actual.RunPrompt(ctx, blocks)
			row["result"] = result
			if err != nil {
				row["error"] = err.Error()
			}
			return result, err
		}, close: actual.Close}
		err := session.Run(ctx, UnreadChanged{MemberIdentity: id})
		ds.component = actual
		if err != nil {
			t.Fatal("real model turn failed; inspect private report", err)
		}
		result := row["result"].(dsh.TurnResult)
		for _, event := range result.Events {
			if event.Tool != nil {
				t.Fatal("unexpected tool use")
			}
		}
		var answer struct {
			EventLocal         string `json:"event_local"`
			OffsetEventLocal   string `json:"offset_event_local"`
			EventDescription   string `json:"event_description"`
			AmbiguousIsUnknown bool   `json:"ambiguous_is_unknown"`
		}
		text := strings.TrimSpace(result.Text)
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimSuffix(text, "```")
		if err := json.Unmarshal([]byte(text), &answer); err != nil || answer.EventLocal != expected || answer.OffsetEventLocal != expected || !answer.AmbiguousIsUnknown || answer.EventDescription == "" {
			t.Fatal("incorrect time interpretation; inspect private report", err, answer)
		}
	}
	run("Asia/Shanghai", "2026-11-09T01:04:13.09+08:00", true)
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal(err)
	}
	run("UTC", "2026-11-08T17:04:13.09Z", true)
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	backend.recover = true
	session, err = backend.OpenSession(ctx, opts)
	if err != nil {
		t.Fatal("normal recovery failed", err)
	}
	run("UTC", "2026-11-08T17:04:13.09Z", true)
	report["status"] = "passed"
}
