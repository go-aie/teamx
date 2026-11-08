package room

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var productionFixtureBuild struct {
	sync.Once
	dir, path string
	err       error
}

// Native CLI integration tests share one production build. Its lifetime spans
// the entire suite, including parallel tests and their subprocess cleanup.
func productionFixtureCLI(t *testing.T) string {
	t.Helper()
	productionFixtureBuild.Do(func() {
		productionFixtureBuild.dir, productionFixtureBuild.err = os.MkdirTemp("", "teamx-production-fixture-*")
		if productionFixtureBuild.err != nil {
			return
		}
		productionFixtureBuild.path = filepath.Join(productionFixtureBuild.dir, "teamx")
		data, err := exec.CommandContext(t.Context(), "go", "build", "-o", productionFixtureBuild.path, "../../cmd/teamx").CombinedOutput()
		if err != nil {
			productionFixtureBuild.err = fmt.Errorf("build production CLI: %w: %s", err, data)
		}
	})
	if productionFixtureBuild.err != nil {
		t.Fatal(productionFixtureBuild.err)
	}
	return productionFixtureBuild.path
}

func TestMain(m *testing.M) {
	code := m.Run()
	if productionFixtureBuild.dir != "" {
		_ = os.RemoveAll(productionFixtureBuild.dir)
	}
	os.Exit(code)
}
