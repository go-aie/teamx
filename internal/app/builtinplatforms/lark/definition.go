// Package lark assembles the existing lark capabilities without starting resources.
package lark

import (
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Definition declares static capabilities; it does not access accounts or start queries.
func Definition() platforms.Definition {
	refs := lark.WithImageActions(lark.AcceptedActionCapabilities(nil, nil, nil), nil, nil, nil, nil)
	return platforms.Definition{
		Type: platform.TypeDefinition{Provider: "lark", ToolType: "lark", NotificationChannels: []string{"messages"}},
		Connection: &platforms.Connection{Normalize: registry.Normalize, ValidProfile: func(origin string, p platform.CLIProfile) bool {
			return lark.ValidProfile(p) && origin == lark.Origin(p.Brand)
		}},
		Tool: &platforms.Tool{Bind: BindTool, Definition: tool.Definition{Name: "lark", Description: "飞书内置消息读取与已开放的托管动作；使用入口 --help 查看命令", Provider: "lark", Version: "lark-cli 1.0.99", InternalOnly: true, Skill: "skills/lark/SKILL.md"}},
		Sources: []platforms.Source{{ID: lark.MessagesSourceID, New: func() notification.Source {
			return &lark.SearchSource{}
		}, Bind: BindRuntime}},
		Notifications: lark.NotificationCapabilities(), References: &refs,
	}
}
