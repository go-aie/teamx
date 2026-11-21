package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-aie/teamx/boot"
	builtinplatforms "github.com/go-aie/teamx/builtin/platforms"
	"github.com/go-aie/teamx/extension"
)

func main() {
	selected, err := extension.Compose(builtinplatforms.Extensions()...)
	if err == nil {
		err = boot.Run(selected, os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		var exit *boot.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
