package tool

import (
	"embed"
	"io/fs"
)

// Skills are optional, read-only workflow files. There is no workflow engine or
// provider execution behind this resource lookup.
//
//go:embed skills/*/SKILL.md
var skills embed.FS

func ReadSkill(d Definition) ([]byte, error) {
	if !fs.ValidPath(d.Skill) {
		return nil, fs.ErrNotExist
	}
	return skills.ReadFile(d.Skill)
}
