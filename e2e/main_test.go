// Package e2e drives the compiled gahaku binary the way a deployment does:
// `gahaku serve` over a real TCP listener, worker subprocesses it spawns of
// itself, the real render pipelines, and — where the environment provides one —
// a real object store the pages travel through.
//
// It lives outside pkg/ rather than beside a package (AGENTS.md) because what
// it exercises is the built artifact rather than any one package's API: no test
// here imports gahaku code other than the generated protobuf client.
//
// The suite is opt-in. It compiles a binary and rasterizes real documents,
// which is more than an ordinary `go test ./...` should pay for, so every test
// skips unless GAHAKU_TEST_E2E is set — the switch `make e2e` throws, and the
// one the deployment harness of step 11 mirrors with GAHAKU_TEST_QUADLET.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// enableEnv opts the suite in.
const enableEnv = "GAHAKU_TEST_E2E"

// commandPath is the binary under test, named by import path so the build does
// not depend on which directory the test runs from.
const commandPath = "github.com/ngicks/gahaku/cmd/gahaku"

// gahakuBinary is the freshly built binary every test starts. Empty means the
// suite is switched off.
var gahakuBinary string

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// run owns what TestMain cannot: os.Exit skips deferred cleanup, so the build
// directory is removed here and only the exit code crosses back.
func run(m *testing.M) (int, error) {
	// Checked before the build, so a switched-off run costs nothing at all.
	if os.Getenv(enableEnv) == "" {
		return m.Run(), nil
	}

	dir, err := os.MkdirTemp("", "gahaku-e2e-build-")
	if err != nil {
		return 0, fmt.Errorf("creating the build directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	gahakuBinary = filepath.Join(dir, "gahaku")
	build := exec.Command("go", "build", "-o", gahakuBinary, commandPath)
	if out, err := build.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("building %s: %w\n%s", commandPath, err, out)
	}
	return m.Run(), nil
}

// requireBinary skips a test when the suite is switched off, which is what
// makes the gate visible as a skip rather than as an absent test.
func requireBinary(t *testing.T) {
	t.Helper()
	if gahakuBinary == "" {
		t.Skipf("set %s=1 to run the end-to-end suite", enableEnv)
	}
}
