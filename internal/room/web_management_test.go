package room

import (
	"flag"
)

var webPreviewDuration = flag.Duration("teamx-web-preview", 0, "keep the isolated team HTTP test server available for browser inspection")
