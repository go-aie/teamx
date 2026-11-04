package room

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Private Git helper, independent of the agent tools catalog and glab. It never
// prints a token except as the password answer on Git's private askpass pipe.
func runWorkspaceGitCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: teamx workspace-git askpass --binding FILE PROMPT")
	}
	command, args := args[0], args[1:]
	if command != "askpass" {
		return errors.New("unknown workspace Git helper")
	}
	flags := flag.NewFlagSet("workspace-git askpass", flag.ContinueOnError)
	flags.SetOutput(stderr)
	binding := flags.String("binding", "", "private workspace binding file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("workspace askpass requires one Git prompt")
	}
	b, err := gitlab.ReadWorkspaceBinding(*binding)
	if err != nil {
		return err
	}
	answer, err := gitlab.Askpass(ctx, b, flags.Arg(0), env, nil)
	if err != nil {
		return err
	}
	return writeOutput(stdout, []byte(answer+"\n"))
}
