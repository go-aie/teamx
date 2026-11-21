package tool

import (
	"embed"
	"fmt"
	"io/fs"
)

// Skills are optional, read-only workflow files. There is no workflow engine or
// provider execution behind this resource lookup.
//
//go:embed skills/*/SKILL.md skills/platforms/*.md skills/gitlab/managed-send.md
var skills embed.FS

func ReadSkill(d Definition) ([]byte, error) {
	if !fs.ValidPath(d.Skill) {
		return nil, fs.ErrNotExist
	}
	return skills.ReadFile(d.Skill)
}

// ReadToolSkill offers one task resource for the built-in GitLab Skill. Topic
// selection does not extend the public tool declaration or execute a provider.
func ReadToolSkill(d Definition, topic string) ([]byte, error) {
	if topic == "" {
		return ReadSkill(d)
	}
	if d.Provider != "gitlab" || d.Skill != "skills/gitlab/SKILL.md" {
		return nil, fmt.Errorf("%w: this entry has no skill topic %q; omit --topic for its declared Skill", fs.ErrNotExist, topic)
	}
	if topic != "managed-send" {
		return nil, fmt.Errorf("%w: unknown GitLab skill topic %q; use managed-send, or omit --topic for the complete guide", fs.ErrNotExist, topic)
	}
	return skills.ReadFile("skills/gitlab/managed-send.md")
}

// ReadPlatformSkill keeps the complete setup guide as the default. The two
// task-specific resources include their shared identity/authorization boundary;
// callers need no index or additional common-resource lookup.
func ReadPlatformSkill(topic string) ([]byte, error) {
	if topic == "" {
		return ReadSkill(Definition{Skill: "skills/platforms/SKILL.md"})
	}
	if topic != "notifications" && topic != "selection" {
		return nil, fmt.Errorf("%w: unknown platform skill topic %q; use notifications or selection, or omit --topic for the complete guide", fs.ErrNotExist, topic)
	}
	common, err := skills.ReadFile("skills/platforms/boundary.md")
	if err != nil {
		return nil, err
	}
	workflow, err := skills.ReadFile("skills/platforms/" + topic + ".md")
	if err != nil {
		return nil, err
	}
	return append(append(common, '\n'), workflow...), nil
}
