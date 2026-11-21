package room

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Exercise the assembled backend instructions so saved/custom roles receive the
// same shared guidance under both input contracts, without rewriting their role.
func TestSharedGuidanceInEffectiveSessionInstructions(t *testing.T) {
	for _, transport := range []string{"legacy", "cli", "http"} {
		unified := transport != "legacy"
		for _, role := range []string{assistantInstructions, "Saved custom role: review changes only when requested."} {
			dir, id := recoveryFixture(t)
			store := openTestStore(t, dir)
			var delivered string
			b := dshBackend{store: store, profile: dshProfile{Instructions: role},
				open: func(_ context.Context, options dsh.Options) (dshComponent, error) {
					delivered = options.Instructions
					return &adapterComponent{}, nil
				}}
			if unified {
				b.inputService = &service{store: store, info: ServiceInfo{RoomIdentity: id.RoomIdentity, ServiceEpoch: "current-service"}}
				b.access, _, _ = accessFixture(t)
				if transport == "http" {
					b.inboxMCP = b.inputService
				}
			}
			// OpenSession must deliver the composed guide through PrepareOpen and
			// Launch.Instructions to the selected factory, including saved roles.
			session, err := b.OpenSession(t.Context(), adapterOptions(store, id))
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, contract := range []string{
				`"$TEAMX_CLI" platforms skill`,
				`"$TEAMX_CLI" platforms skill --topic notifications`,
				`"$TEAMX_CLI" platforms skill --topic selection`,
				"platforms/selection/ID",
				"platforms/connect/ID",
				"Reuse guidance already available",
				"Connections, notification subscriptions, tool grants and business writes are separate",
				"reuse a verified exact target_ref and valid action contract across turns",
				"A saved target/contract with available guidance needs no discovery or guidance reload",
				"If sending guidance is missing and ENTRY is unknown, discover it once with tools list; never guess an entry",
				"Submit each new intent once with one stable ID and complete content",
				"Latest explicit corrections or revocations win",
				"History do not consume",
				"Keep lasting constraints, important progress, verified results and next steps",
				"Write web links as `[label](https://...)`",
				"Avoid raw HTML `<a>` and `<code>` tags",
				"do not authorize rewriting it",
				"automatically associates real human answers with explicit reply_to",
				"marks it fulfilled automatically",
				"only when the entry declares a Skill",
				"Bound local tools may have no platform account",
				"actions resolve/execute --tool ENTRY",
				`"$TEAMX_CLI" tools skill ENTRY --topic managed-send`,
				"Unknown does not prove success",
				"You may use runtime-provided internal collaboration within your role and authority",
				"Their work cannot stand for another Room member's review, agreement or authorization",
				"send_message communicates with DSH internal agents using runtime-returned Agent IDs",
				"A successful internal send_message does not establish Room publication",
				"do not assume they inherit your complete instructions",
				"## Member role\n\n" + strings.TrimSpace(role),
			} {
				if !strings.Contains(delivered, contract) {
					t.Fatalf("missing shared guidance %q (transport=%s)", contract, transport)
				}
			}
			// Workflow mechanics belong to the directly routed resources; check
			// the final factory delivery, including saved roles and both transports.
			for _, detail := range []string{"platforms member notifications --platform-id", "selection start directly", "Feishu availability expires after 60 seconds", "Application gaps, user OAuth gaps"} {
				if strings.Contains(delivered, detail) {
					t.Fatalf("on-demand platform procedure remained in fixed instructions: %q", detail)
				}
			}
			t.Logf("transport=%s role_bytes=%d final_instructions_bytes=%d", transport, len(role), len(delivered))
			if strings.Count(delivered, "# TeamX agent guide") != 1 || strings.Count(delivered, "Calendar tasks in direct conversations") != 1 {
				t.Fatal("duplicated public guide")
			}
			sections := []string{"# TeamX agent guide", "# DSH internal collaboration and the TeamX Room", "## Session identity", "## Member role"}
			previous := -1
			for _, section := range sections {
				position := strings.Index(delivered, section)
				if strings.Count(delivered, section) != 1 || position <= previous {
					t.Fatal("missing, duplicated or out-of-order Session guidance", section)
				}
				previous = position
			}
			if strings.Contains(delivered, "Do not delegate or manage members") || strings.Contains(delivered, "no authority to delegate") || strings.Contains(dshAgentGuide, "send_message") {
				t.Fatal("conflicting delegation ban or backend details in shared guide")
			}
			if unified && (!strings.Contains(delivered, "matters source --source SOURCE_REF") || !strings.Contains(delivered, "--cursor") || strings.Contains(delivered, "Inbox entries have type")) {
				t.Fatal("stream source recovery guidance missing or legacy batch guidance leaked")
			}
		}
	}
}

func TestPlatformSkillTopicHelpNeedsNoService(t *testing.T) {
	var out, errs bytes.Buffer
	if err := runPlatformConnectWithClient(t.Context(), []string{"skill", "--help"}, &out, &errs, func(string) string {
		t.Fatal("skill help read bound context")
		return ""
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--topic notifications", "--topic selection", "Omit --topic", "No prior connection or tool binding"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("skill help missing task entry", want, out.String())
		}
	}
}

func TestEntrySkillUsesExactAccountForRootActions(t *testing.T) {
	for _, provider := range []string{"lark", "gitlab", "scion"} {
		entry := provider + "-selected-account"
		data, err := entrySkill(tool.Definition{Name: provider, Skill: "skills/" + provider + "/SKILL.md"}, entry)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "tools run "+provider+" ") || strings.Contains(text, "--tool "+provider+" ") || strings.Contains(text, "tools run "+entry+" actions") || strings.Contains(text, "mr note create") {
			t.Fatal("Skill recommends an alias or a parallel send path", provider)
		}
		if provider == "lark" && (!strings.Contains(text, "actions resolve --tool "+entry) || !strings.Contains(text, "actions execute --tool "+entry)) {
			t.Fatal("root action examples lost the exact account", provider)
		}
	}
}

// The fixed guide routes directly here. Each returned resource must retain the
// relevant safety/recovery contract without a preliminary common-resource read.
func TestTaskGuidanceKeepsRecoveryAndReuseContracts(t *testing.T) {
	for _, topic := range []string{"", "notifications", "selection"} {
		data, err := tool.ReadPlatformSkill(topic)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		want := []string{"latest human corrections and revocations", "cancelled intent"}
		switch topic {
		case "notifications":
			want = append(want, "Do not first read member get/status", "completed save needs no acknowledge or extra get", "--source-seq ORIGINAL_SEQ", "never retry unknown work under a new source or ID")
		case "selection":
			want = append(want, "--notify inbox", "end the turn without waiting for clicks", "original human request, latest intent", "continuation.handled_version", "--result-version RESULT_VERSION", "Do not read member configuration")
		default:
			want = append(want, "source/tool customization", "observed versions", "60 seconds", "Unchecked is not ready", "connect authorize --platform-id ID", "image.upload", "unsupported host cannot send")
		}
		for _, contract := range want {
			if !strings.Contains(text, contract) {
				t.Fatalf("platform topic %q lost recovery contract %q", topic, contract)
			}
		}
	}
	for _, topic := range []string{"", "managed-send"} {
		data, err := entrySkillTopic(tool.Definition{Name: "gitlab", Provider: "gitlab", Skill: "skills/gitlab/SKILL.md"}, "gitlab-selected-account", topic)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(data), "gitlab-selected-account", "ENTRY")
		for _, contract := range []string{"do not discover ENTRY or reload the Skill", "omit\n   --tool if the exact entry is unknown", "no preliminary native", "actions read --id ORIGINAL_ID", "Never change ID, account or CLI to resend uncertain work", "never send a subset or fall back to text"} {
			if !strings.Contains(text, contract) {
				t.Fatalf("GitLab topic %q lost recovery contract %q", topic, contract)
			}
		}
	}
}

func TestPlatformMemberHelpFocusesOnRequestedCommand(t *testing.T) {
	for _, action := range []string{"get", "notifications", "tool", "status"} {
		t.Run(action, func(t *testing.T) {
			var out, errs bytes.Buffer
			if err := runPlatformMemberCLI(t.Context(), []string{"member", action, "--help"}, &out, &errs, func(string) string { t.Fatal("help read execution environment"); return "" }, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "teamx platforms member "+action) || strings.Contains(out.String(), "PKCE") || strings.Contains(out.String(), "connect start") {
				t.Fatal("unfocused help", out.String())
			}
			if action == "notifications" {
				for _, flag := range []string{"-enabled", "-private-chat-id", "-group-at-all", "-todos-enabled", "-event-type", "-notification-version"} {
					if !strings.Contains(errs.String(), flag) {
						t.Fatal("missing customization field", flag)
					}
				}
			}
		})
	}
}

func TestToolSkillTopicHelpNeedsNoService(t *testing.T) {
	for _, args := range [][]string{{"skill", "--help"}, {"skill", "gitlab-exact-account", "--help"}} {
		var out, errs bytes.Buffer
		if err := runToolsWithClient(t.Context(), args, nil, &out, &errs, func(string) string { t.Fatal("help read bound context"); return "" }, nil); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if err := runCLIWithCatalog(t.Context(), append([]string{"tools"}, args...), nil, &out, &errs, func(string) string { t.Fatal("top-level help read credentials or binding"); return "" }, nil); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--topic managed-send", "Omit --topic", "exact tool binding", "Unknown or unsupported"} {
			if !strings.Contains(out.String(), want) {
				t.Fatal("help missing resource contract", want, out.String())
			}
		}
	}
}
