package room

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

type bundleModelSetup struct {
	Model dsh.ModelConfig
	Env   map[string]string
}

type bundlePrompt struct {
	read func(label, fallback string, secret bool) (string, error)
	out  io.Writer
}

// A redirected stdin keeps the original non-interactive behavior. stty is
// available on the supported macOS/Linux systems; no shell evaluates input.
func newBundlePrompt(input *os.File, out io.Writer) (*bundlePrompt, error) {
	info, err := input.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return nil, nil
	}
	stty := func(args ...string) *exec.Cmd {
		cmd := exec.Command("stty", args...)
		cmd.Stdin = input
		return cmd
	}
	state, err := stty("-g").Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("interactive init requires stty; install it or use --non-interactive")
	}
	if err != nil { // Includes character devices such as /dev/null.
		return nil, nil
	}
	reader := bufio.NewReader(input)
	readLine := func() (string, error) {
		value, err := reader.ReadString('\n')
		if err != nil {
			return "", errors.New("initialization cancelled before configuration was saved")
		}
		return strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r"), nil
	}
	prompt := &bundlePrompt{out: out}
	prompt.read = func(label, fallback string, secret bool) (value string, err error) {
		if fallback != "" {
			label += " [" + fallback + "]"
		}
		if !secret {
			if _, err = fmt.Fprint(out, label+": "); err != nil {
				return "", err
			}
			value, err = readLine()
			value = strings.TrimSpace(value)
			if value == "" {
				value = fallback
			}
			return value, err
		}

		// Restore echo on EOF, read/write errors and interrupt/termination signals.
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(interrupts)
		if err = stty("-echo").Run(); err != nil {
			return "", errors.New("cannot hide API Key input")
		}
		defer func() {
			if restore := stty(strings.TrimSpace(string(state))).Run(); restore != nil {
				err = errors.Join(err, errors.New("cannot restore terminal settings"))
			}
			fmt.Fprintln(out)
		}()
		if _, err = fmt.Fprint(out, label+": "); err != nil {
			return "", err
		}
		type result struct {
			value string
			err   error
		}
		done := make(chan result, 1)
		go func() {
			value, err := readLine()
			done <- result{value, err}
		}()
		select {
		case r := <-done:
			return r.value, r.err
		case <-interrupts:
			return "", errors.New("initialization cancelled before configuration was saved")
		}
	}
	return prompt, nil
}

func promptBundleModel(p *bundlePrompt) (*bundleModelSetup, error) {
	setup := &bundleModelSetup{Model: dsh.ModelConfig{
		Protocol: "openai-completions", BaseURL: "https://example.invalid/v1",
		ModelID: "model", APIKeyEnv: "OPENAI_API_KEY",
	}}
	if _, err := fmt.Fprintln(p.out, "Configure your model. Press Enter to accept defaults; Ctrl-C cancels."); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		label, fallback string
		value           *string
	}{
		{"Protocol (openai-completions / deepseek-messages)", "openai-completions", &setup.Model.Protocol},
		{"Model base URL", "", &setup.Model.BaseURL},
		{"Model ID", "", &setup.Model.ModelID},
		{"API Key environment variable", "OPENAI_API_KEY", &setup.Model.APIKeyEnv},
	} {
		for {
			value, err := p.read(field.label, field.fallback, false)
			if err != nil {
				return nil, err
			}
			*field.value = value
			err = dsh.CheckConfig(dsh.Options{BinaryPath: "/unused/dsh", Model: setup.Model})
			if err == nil && (isRoomToolEnv(setup.Model.APIKeyEnv) || setup.Model.APIKeyEnv == "TEAMX_CLI") {
				err = errors.New("reserved TeamX credential environment name")
			}
			if err == nil && strings.ContainsRune(value, 0) {
				err = errors.New("input cannot contain NUL")
			}
			if err == nil {
				break
			}
			if _, err := fmt.Fprintf(p.out, "Invalid setting: %s. Try again.\n", err); err != nil {
				return nil, err
			}
		}
	}
	name := setup.Model.APIKeyEnv
	if key, exported := os.LookupEnv(name); exported {
		if key == "" {
			return nil, fmt.Errorf("%s is exported but empty; unset it or export a non-empty key, then run init again", name)
		}
		_, err := fmt.Fprintf(p.out, "Using exported %s; keep it set when running doctor/start. The key will not be saved.\n", name)
		return setup, err
	}
	if _, err := fmt.Fprintln(p.out, "API Key will be saved in the private config.toml (owner-only permissions, 0600). Input is hidden."); err != nil {
		return nil, err
	}
	for {
		key, err := p.read("API Key", "", true)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(key) != "" && !strings.ContainsRune(key, 0) {
			setup.Env = map[string]string{name: key}
			return setup, nil
		}
		if _, err := fmt.Fprintln(p.out, "A non-empty API Key without NUL is required. Try again."); err != nil {
			return nil, err
		}
	}
}
