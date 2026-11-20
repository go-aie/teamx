package room

import (
	"bytes"
	"strings"
	"testing"
)

// Exercise the assembled backend instructions so saved/custom roles receive the
// same navigation under both input contracts, without rewriting their role.
func TestPlatformNavigationInEffectiveSessionInstructions(t *testing.T) {
	dir, id := recoveryFixture(t)
	store, err := openStore(dir, id.RoomID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, unified := range []bool{false, true} {
		for _, role := range []string{assistantInstructions, "Saved custom role: review changes only when requested."} {
			b := dshBackend{store: store, profile: dshProfile{Instructions: role}}
			if unified {
				b.inputService = &service{}
			}
			options, err := b.componentOptions(adapterOptions(store, id), sessionReference{})
			if err != nil {
				t.Fatal(err)
			}
			for _, contract := range []string{`"$TEAMX_CLI" platforms skill`, "selection start directly", "end the turn without waiting for clicks", "source customization uses observed versions", "## Member role\n\n" + strings.TrimSpace(role)} {
				if !strings.Contains(options.Instructions, contract) {
					t.Fatalf("missing navigation %q (unified=%v)", contract, unified)
				}
			}
			if strings.Count(options.Instructions, "# TeamX agent guide") != 1 {
				t.Fatal("duplicated public guide")
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
