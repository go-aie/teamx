// Package gitlab declares the existing built-in platform as an opaque extension.
package gitlab

import (
	"github.com/go-aie/teamx/extension"
	implementation "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	"github.com/go-aie/teamx/internal/app/extensions"
)

// Extension declares the existing capabilities without acquiring runtime resources.
func Extension() extension.Extension {
	return extensions.Platform("gitlab", implementation.Definition)
}
