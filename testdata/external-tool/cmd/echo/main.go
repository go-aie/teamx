package main

import (
	"encoding/json"
	"io"
	"os"
)

func main() {
	// The controlled runner must keep credentials and member transport out of
	// this child even when its parent Session has an execution token.
	for _, key := range []string{"TEAMX_EXECUTION_TOKEN", "TEAMX_EXECUTION_AUTH", "TEAMX_ADMIN_TOKEN", "TEAMX_EXTERNAL_FIXTURE_KEY", "GITLAB_TOKEN"} {
		if os.Getenv(key) != "" {
			os.Exit(91)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "--fail" {
		os.Exit(23)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(92)
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Args  []string `json:"args"`
		Input string   `json:"input"`
	}{os.Args[1:], string(input)})
}
