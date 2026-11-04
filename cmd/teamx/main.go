package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-aie/teamx/internal/room"
)

func main() {
	if err := room.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var exit *room.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
