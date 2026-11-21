// Package platforms owns the explicit complete selection of built-in platforms.
// The common catalog and entry resolver do not depend on this package.
package platforms

import (
	"github.com/go-aie/teamx/builtin/platforms/gitlab"
	"github.com/go-aie/teamx/builtin/platforms/lark"
	"github.com/go-aie/teamx/builtin/platforms/scion"
	"github.com/go-aie/teamx/extension"
)

// Extensions is the sole explicit selection for the standard executable.
func Extensions() []extension.Extension {
	return []extension.Extension{gitlab.Extension(), lark.Extension(), scion.Extension()}
}
