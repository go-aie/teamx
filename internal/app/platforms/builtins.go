package platforms

import (
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// SourceBindings carries the existing provider query dependencies. Binding does
// not start runners, validate accounts, read credentials or grant membership.
type SourceBindings struct {
	GitLabEvents gitlab.EventsPrepare
	GitLabTodos  gitlab.TodosPrepare
	LarkReader   func(lark.QueryScope) (lark.MessageReader, error)
}

// Builtins is the one explicit selection point for existing capabilities. There
// is no init registration and no dependency from common consumers to this package.
func Builtins() (*Catalog, error) {
	return NewCatalog(gitlabDefinition, larkDefinition, scionDefinition)
}

func gitlabDefinition() Definition {
	actions, _ := gitlab.ImageReferenceActions(nil, nil, nil, nil, nil, nil, "", "")
	refs := platform.ReferenceCapabilities{CredentialKinds: []string{"gitlab:oauth"}, Actions: actions}
	return Definition{
		Type:       platform.TypeDefinition{Provider: "gitlab", ToolType: "gitlab", NotificationChannels: []string{"events", "todos"}},
		Connection: &Connection{Normalize: registry.Normalize, LegacyIdentity: gitlab.LegacyAccountIdentity},
		Tool: &Tool{Executable: "teamx-gitlab", OverrideEnvironment: "TEAMX_GITLAB_BIN", Definition: tool.Definition{
			Name: "gitlab", Description: "GitLab CLI and WorkItem extensions; uses account permissions", Version: "glab 1.127.0", Provider: "gitlab", Skill: "skills/gitlab/SKILL.md",
			Environment: map[string]string{"GITLAB_HOST": "instance", "GITLAB_TOKEN": "token"}, Dependencies: []tool.ExecutableDependency{{Name: "glab", OverrideEnvironment: "TEAMX_GLAB_BIN"}},
		}},
		Sources: []Source{
			{ID: gitlab.EventsSourceID, Bind: func(b SourceBindings) notification.Source { return &gitlab.EventsSource{Prepare: b.GitLabEvents} }},
			{ID: gitlab.TodosSourceID, Bind: func(b SourceBindings) notification.Source { return &gitlab.TodosSource{Prepare: b.GitLabTodos} }},
		},
		Notifications: gitlab.NotificationCapabilities(), References: &refs,
	}
}

func larkDefinition() Definition {
	refs := lark.WithImageActions(lark.AcceptedActionCapabilities(nil, nil, nil), nil, nil, nil, nil)
	return Definition{
		Type: platform.TypeDefinition{Provider: "lark", ToolType: "lark", NotificationChannels: []string{"messages"}},
		Connection: &Connection{Normalize: registry.Normalize, ValidProfile: func(origin string, p platform.CLIProfile) bool {
			return lark.ValidProfile(p) && origin == lark.Origin(p.Brand)
		}},
		Tool: &Tool{Definition: tool.Definition{Name: "lark", Description: "飞书内置消息读取与已开放的托管动作；使用入口 --help 查看命令", Provider: "lark", Version: "lark-cli 1.0.99", InternalOnly: true, Skill: "skills/lark/SKILL.md"}},
		Sources: []Source{{ID: lark.MessagesSourceID, Bind: func(b SourceBindings) notification.Source {
			return &lark.SearchSource{Source: lark.Source{Reader: b.LarkReader}}
		}}},
		Notifications: lark.NotificationCapabilities(), References: &refs,
	}
}

func scionDefinition() Definition {
	return Definition{
		Type:       platform.TypeDefinition{Provider: "scion", ToolType: "scion"},
		Connection: &Connection{Normalize: registry.Normalize, ValidProfile: func(_ string, p platform.CLIProfile) bool { return scion.ValidProfile(p) }},
		Tool: &Tool{Executable: "teamx-scion", OverrideEnvironment: "TEAMX_SCION_BIN", Definition: tool.Definition{
			Name: "scion", Description: "Scion native repository and Review CLI; uses account permissions", Version: "scn 0.1.13", Provider: "scion", Skill: "skills/scion/SKILL.md",
			Environment: map[string]string{"SCION_ENDPOINT": "instance", "SCION_HUB": "instance", "SCION_CREDENTIALS_FILE": "credentials_file", "SCION_CREDENTIALS": "credential_mode", "SCION_NO_DAEMON": "no_daemon", "SCION_CONFIG_FILE": "config_file"},
		}},
	}
}
