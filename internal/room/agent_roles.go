package room

import _ "embed"

const chiefOfStaffRoleID = "chief-of-staff"

//go:embed prompts/chief-of-staff.md
var chiefOfStaffInstructions string

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
	if id != chiefOfStaffRoleID {
		return agentRoleDefinition{}, false
	}
	return agentRoleDefinition{
		agentRoleSummary: agentRoleSummary{ID: chiefOfStaffRoleID, Name: "个人助理", Description: "Chief of Staff：梳理需求、协助设置、汇总进展"},
		Instructions:     chiefOfStaffInstructions, OpeningMessage: "帮我梳理当前需要处理的事情。",
	}, true
}

func builtinAgentRoleSummaries() []agentRoleSummary {
	role, _ := builtinAgentRole(chiefOfStaffRoleID)
	return []agentRoleSummary{role.agentRoleSummary}
}
