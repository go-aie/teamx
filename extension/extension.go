// Package extension declares and checks the capabilities selected by a TeamX entry point.
// Compose performs static validation only; it does not read credentials, resolve
// executables, open data directories or start services.
package extension

import "github.com/go-aie/teamx/internal/app/extensions"

type Extension = extensions.Extension
type Composition = extensions.Composition

func Compose(selected ...Extension) (*Composition, error) {
	return extensions.Compose(selected...)
}
