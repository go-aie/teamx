// Package localtool is an external tool author fixture: only public TeamX APIs.
package localtool

import (
	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/platform"
)

func Extension() extension.Extension {
	return extension.Extension{Name: "local-example", Tools: []platform.Tool{{
		Name: "local-echo", Description: "Echo arguments and stdin without network access", Version: "1",
		Executable: "teamx-fixture-echo",
	}}}
}
