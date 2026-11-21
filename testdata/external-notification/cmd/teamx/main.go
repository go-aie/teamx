package main

import (
	"fmt"
	"os"

	localnotification "example.com/teamx-local-notification"
	"github.com/go-aie/teamx/boot"
	"github.com/go-aie/teamx/extension"
)

func main() {
	declaration := localnotification.Extension()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--fixture-without-codec" {
		declaration.Notifications[0].Input = nil
		args = args[1:]
	}
	selected, err := extension.Compose(declaration)
	if err == nil {
		err = boot.Run(selected, args, os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
