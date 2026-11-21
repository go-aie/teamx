//go:build integration || e2e

package room

import (
	"os/exec"
	"testing"
)

func TestLarkImagesRealWriteBound(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.CommandContext(t.Context(), python, "-S", "testdata/lark_image_send_real_test.py")
	cmd.Env = append(cmd.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
}
