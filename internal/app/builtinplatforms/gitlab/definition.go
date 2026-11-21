// Package gitlab assembles the existing gitlab capabilities without starting resources.
package gitlab

import (
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Definition declares static capabilities; it does not access accounts or start queries.
func Definition() platforms.Definition {
	actions, _ := gitlab.ImageReferenceActions(nil, nil, nil, nil, nil, nil, "", "")
	refs := platform.ReferenceCapabilities{CredentialKinds: []string{"gitlab:oauth"}, Actions: actions}
	return platforms.Definition{
		Type:       platform.TypeDefinition{Provider: "gitlab", ToolType: "gitlab", NotificationChannels: []string{"events", "todos"}},
		Connection: &platforms.Connection{Normalize: registry.Normalize, LegacyIdentity: gitlab.LegacyAccountIdentity},
		Tool: &platforms.Tool{Executable: "teamx-gitlab", OverrideEnvironment: "TEAMX_GITLAB_BIN", Definition: tool.Definition{
			Name: "gitlab", Description: "GitLab CLI and WorkItem extensions; uses account permissions", Version: "glab 1.127.0", Provider: "gitlab", Skill: "skills/gitlab/SKILL.md",
			Environment: map[string]string{"GITLAB_HOST": "instance", "GITLAB_TOKEN": "token"}, Dependencies: []tool.ExecutableDependency{{Name: "glab", OverrideEnvironment: "TEAMX_GLAB_BIN"}},
		}},
		Sources: []platforms.Source{
			{ID: gitlab.EventsSourceID, New: func() notification.Source { return &gitlab.EventsSource{} }},
			{ID: gitlab.TodosSourceID, New: func() notification.Source { return &gitlab.TodosSource{} }},
		},
		Notifications: gitlab.NotificationCapabilities(), References: &refs,
	}
}
