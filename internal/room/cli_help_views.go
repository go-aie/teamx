package room

import (
	"strings"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/inputstream"
)

// This is a local presentation choice, not identity validation or authorization.
func rootHelpText(env func(string) string, catalog *platforms.Catalog) string {
	agent := env("TEAMX_AGENT_ID") != "" || env("TEAMX_MEMBERSHIP_ID") != ""
	text := rootCLIHelp
	if agent {
		text = `Usage: teamx COMMAND [--help]
Member business commands:
  send                          Publish a Room message with a stable ID
  inbox                         Formal input; follow the Session's input transport
  matters                       Restore and maintain business followups
  actions                       Resolve targets, execute and inspect managed actions
  policy                        Read, save and publish member long-term rules
  calendar                      Direct-conversation scheduled tasks
  skill usage                   Read TeamX interface and execution guidance
  tools list/run/skill           Bound platform entries and standalone tools
  workspace-git                 Git network operations for a bound Workspace
`
	}
	names, tools := []string{}, []string{}
	connections := false
	if catalog != nil {
		for _, d := range catalog.Definitions() {
			names = append(names, d.Type.Provider)
			connections = connections || d.Connection != nil
		}
		// Declaration text only: never locate executables or inspect credentials.
		for _, d := range catalog.Tools(func(name, _ string) string { return name }) {
			tools = append(tools, d.Name)
		}
	}
	if agent {
		if connections {
			text += "  platforms skill/list/connect/selection/chats/projects/member  Account and current-member setup (--help)\n"
		}
	}
	if agent {
		text += "Use teamx COMMAND SUBCOMMAND --help for parameters and recovery.\nHelp lists entry points; current bindings and service checks determine access.\n"
		for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
			if env(key) == "" {
				text += "Member binding is incomplete: execution requires all six TEAMX_* identity values from the Session.\n"
				break
			}
		}
	}
	label := func(names []string) string {
		if len(names) == 0 {
			return "none"
		}
		return strings.Join(names, ", ")
	}
	text += "Selected platform declarations: " + label(names) + ".\nSelected tool declarations: " + label(tools) + ". Use tools list for currently bound entries.\n"
	if agent && env("TEAMX_INPUT_CONTRACT") == inputstream.Contract {
		text += "Input protocol: streams. inbox list/read is the CLI path; MCP Sessions use discovered inbox_list/inbox_read. Follow the Session's transport instructions.\n"
	}
	return text
}

// Canonical help requests finish before credentials, execution binding or I/O.
// Explicit tool execution (including tools run ENTRY --help) keeps its own gate.
func localCLIHelp(args []string, env func(string) string, catalog *platforms.Catalog) (string, bool) {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return rootHelpText(env, catalog), true
	}
	if args[len(args)-1] != "--help" && args[len(args)-1] != "-h" {
		return "", false
	}
	path := args[:len(args)-1]
	if len(path) == 0 {
		return "", false
	}
	if path[0] == "tools" && len(path) >= 2 && path[1] == "skill" && (len(path) == 2 || len(path) == 3) {
		return toolSkillHelp, true
	}
	op := ""
	if len(path) == 2 {
		op = path[1]
	}
	if len(path) <= 2 {
		switch path[0] {
		case "inbox":
			if env("TEAMX_INPUT_CONTRACT") == inputstream.Contract || op == "list" {
				return inputCLIHelpText(op)
			}
			if op == "" || op == "read" {
				return referenceHelpText("inbox", op), true
			}
		case "matters", "actions", "policy":
			if _, ok := referenceCommandHelps[path[0]+"/"+op]; ok || op == "" {
				return referenceHelpText(path[0], op), true
			}
		case "calendar":
			return calendarHelpText(op)
		case "platforms":
			if op == "" {
				return platformConnectHelp, true
			}
			if op == "connect" {
				return platformConnectDirectory, true
			}
			if op == "selection" {
				return platformSelectionHelp, true
			}
			if op == "list" || op == "skill" {
				return platformConnectHelpText(op)
			}
		}
	}
	if len(path) == 3 && path[0] == "platforms" {
		switch path[1] {
		case "connect":
			return platformConnectHelpText(path[2])
		case "selection":
			return platformSelectionHelpText(path[2])
		}
	}
	return "", false
}

func scopedCommandHelp(h referenceCommandHelp) string {
	return "Usage: teamx " + h.usage + "\n" + h.purpose + "\nParameters: " + h.parameters + "\nReturns: " + h.result + "\nExample: " + h.example + "\nConstraints: " + h.constraints + "\n"
}
