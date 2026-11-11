package room

import _ "embed"

const assistantRoleID = "assistant"

//go:embed prompts/assistant.md
var assistantInstructions string

type agentRoleSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type agentRoleDefinition struct {
	agentRoleSummary
	Instructions   string
	OpeningMessage string
}

func builtinAgentRole(id string) (agentRoleDefinition, bool) {
	if id != assistantRoleID {
		return agentRoleDefinition{}, false
	}
	return agentRoleDefinition{
		agentRoleSummary: agentRoleSummary{ID: assistantRoleID, Name: "个人助理", Description: "Assistant：梳理需求、协助设置、汇总进展"},
		Instructions:     assistantInstructions, OpeningMessage: "帮我梳理当前需要处理的事情。",
	}, true
}

func builtinAgentRoleSummaries() []agentRoleSummary {
	role, _ := builtinAgentRole(assistantRoleID)
	return []agentRoleSummary{role.agentRoleSummary}
}
