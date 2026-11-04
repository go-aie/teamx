//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the real entry point under a controlling terminal, not a byte buffer.
func TestTerminalHelpHelper(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_TERMINAL_HELPER") != "1" {
		return
	}
	os.Args = []string{os.Args[0], "-h"}
	main()
}

func TestNativeHelpWithControllingTerminal(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for the standard-library PTY harness")
	}
	// A background process group in the caller's session is stopped by SIGTTIN
	// on this read. A detached session has no /dev/tty and continues immediately.
	bin := fakeGlab(t, "{ IFS= read -r probe < /dev/tty; } 2>/dev/null || :\nprintf 'native terminal help\\n'\n")
	for _, tc := range []struct{ name, bin, expected string }{
		{"terminal-probe", bin, "native terminal help"},
		{"pinned-glab", os.Getenv("TEAMX_GLAB_BIN"), "GLab is an open source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.bin == "" {
				t.Skip("set TEAMX_GLAB_BIN for the pinned glab PTY check")
			}
			binary, err := filepath.Abs(tc.bin)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, python, "-c", terminalHelpHarness, os.Args[0])
			cmd.Env = append(os.Environ(), "TEAMX_GITLAB_TERMINAL_HELPER=1", "TEAMX_GLAB_BIN="+binary)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), tc.expected) || !strings.Contains(string(out), "Additional commands:") {
				t.Fatalf("terminal help failed: %v\n%s", err, out)
			}
			if strings.Contains(string(out), "interrupted") || strings.Contains(string(out), "\x1b]11;") {
				t.Fatalf("help attempted terminal interrogation or was interrupted:\n%s", out)
			}
		})
	}
}

const terminalHelpHarness = `
import errno, fcntl, os, pty, select, signal, subprocess, sys, termios, time
master, slave = pty.openpty()
def attach_terminal():
    os.setsid()
    fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
p = subprocess.Popen([sys.argv[1], '-test.run=^TestTerminalHelpHelper$'],
    stdin=slave, stdout=slave, stderr=slave, preexec_fn=attach_terminal)
os.close(slave)
output = bytearray()
deadline = time.monotonic() + 5
timed_out = False
try:
    while True:
        if time.monotonic() > deadline:
            timed_out = True
            p.send_signal(signal.SIGINT)
            p.wait(timeout=3)
            break
        if select.select([master], [], [], .05)[0]:
            try:
                data = os.read(master, 65536)
            except OSError as e:
                if e.errno != errno.EIO:
                    raise
                break
            if not data:
                break
            output.extend(data)
    p.wait(timeout=3)
finally:
    if p.poll() is None:
        p.send_signal(signal.SIGINT)
        p.wait(timeout=3)
    os.close(master)
sys.stdout.buffer.write(output)
if timed_out:
    sys.stderr.write('help failed to finish under a controlling terminal\n')
sys.exit(1 if timed_out else p.returncode)
`
