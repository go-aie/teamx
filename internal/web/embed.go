// Package web serves the embedded TeamX user interface.
package web

import "embed"

//go:embed assets/index.html assets/*.css assets/*.js assets/*.mjs assets/features/*.mjs assets/styles/*.css assets/vendor/*.mjs
var files embed.FS
