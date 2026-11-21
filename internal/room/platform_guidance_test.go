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
				"selection start directly",
				"end the turn without waiting for clicks",
				"source customization uses observed versions",
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
