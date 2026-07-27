package quadlet

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// systemdImage carries systemd and podman both. Verified against the image
	// itself: /usr/sbin/init resolves through usr/sbin -> bin to
	// usr/bin/init -> ../lib/systemd/systemd, and it runs as root.
	systemdImage = "quay.io/podman/stable"
	// initPath is what has to be PID 1 for the generator to run at all.
	initPath = "/usr/sbin/init"
	// unitDir is the rootful search path, which is the deviation this harness
	// makes from the rootless install of deployment/quadlet/README.md: inside a
	// throwaway privileged container there is no service user worth creating,
	// and %h then resolves to /root rather than to that user's home.
	unitDir = "/etc/containers/systemd"
	// credentialDir is where versitygw.container's EnvironmentFile=%h/... lands
	// under the system manager.
	credentialDir  = "/root/.config/gahaku"
	credentialFile = "versitygw.env"
)

const (
	// pullTimeout is generous: the image is pulled cold on a first run.
	pullTimeout = 15 * time.Minute
	// managerTimeout bounds the wait for the container's systemd to answer on
	// the bus. Deliberately not a wait for the boot to *finish*: default.target
	// pulls in versitygw.service, so a manager that is still "starting" is a
	// manager whose gateway is still pulling its image, and unitTimeout is the
	// one deadline that should govern that.
	managerTimeout = 2 * time.Minute
	// unitTimeout bounds the wait for versitygw.service. It has to exceed the
	// unit's own TimeoutStartSec, since the inner podman pulls the gateway
	// image on the first start.
	unitTimeout = 6 * time.Minute
	// pollInterval paces every is-active / HTTP poll below.
	pollInterval = time.Second
	// execTimeout bounds one `podman exec` of a command that should answer at
	// once.
	execTimeout = 30 * time.Second
)

// systemd is a running container with the quadlet units mounted where the
// generator looks for them.
type systemd struct {
	podman string
	name   string
	// endpoint is the gateway's S3 url as seen from the host, published twice:
	// out of the inner container into this one, and out of this one onto a
	// loopback port of the host.
	endpoint string
}

// start boots the systemd container. Every way the environment can refuse
// — no podman, no network, a podman that cannot run containers — is a skip
// rather than a failure: the same gate as e2e/, so an ordinary `go test ./...`
// never depends on any of it.
func start(t *testing.T) *systemd {
	t.Helper()

	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skipf("podman is not installed: %v", err)
	}

	pullCtx, cancel := context.WithTimeout(t.Context(), pullTimeout)
	defer cancel()
	pull := exec.CommandContext(pullCtx, podman, "pull", systemdImage)
	if out, err := pull.CombinedOutput(); err != nil {
		t.Skipf("pulling %s failed, likely without network: %v\n%s", systemdImage, err, out)
	}

	// Written before the container starts, not exec'd in afterwards: systemd
	// reaches default.target and starts versitygw.service on its own, well
	// before an exec could land, and a missing EnvironmentFile is a start
	// failure the unit is deliberately not lenient about.
	credentials := t.TempDir()
	env := fmt.Sprintf(
		"ROOT_ACCESS_KEY_ID=%s\nROOT_SECRET_ACCESS_KEY=%s\n",
		accessKey, secretKey,
	)
	envPath := filepath.Join(credentials, credentialFile)
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		t.Fatalf("writing the gateway credentials: %v", err)
	}

	units, err := filepath.Abs(filepath.Join("..", "quadlet"))
	if err != nil {
		t.Fatalf("locating the quadlet units: %v", err)
	}

	addr := freeAddr(t)
	s := &systemd{
		podman:   podman,
		name:     fmt.Sprintf("gahaku-quadlet-%d", time.Now().UnixNano()),
		endpoint: "http://" + addr,
	}
	run := exec.CommandContext(t.Context(), podman, "run",
		"--detach",
		"--rm",
		"--name", s.name,
		// Both are what lets systemd be PID 1 here: a private cgroup it may
		// write to, and the tmpfs mounts it expects.
		"--privileged",
		"--systemd=always",
		"--publish", addr+":"+gatewayPort,
		"--volume", units+":"+unitDir+":ro",
		"--volume", credentials+":"+credentialDir+":ro",
		systemdImage,
		initPath,
	)
	if out, err := run.CombinedOutput(); err != nil {
		t.Skipf("podman cannot run a systemd container here: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(podman, "rm", "--force", s.name).CombinedOutput(); err != nil {
			t.Logf("removing %s: %v\n%s", s.name, err, out)
		}
	})

	s.waitManager(t)
	return s
}

// exec runs a command inside the container and returns its combined output
// with the trailing newline trimmed, which is what makes systemctl's one-word
// answers directly comparable.
func (s *systemd) exec(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	full := append([]string{"exec", s.name}, args...)
	out, err := exec.CommandContext(ctx, s.podman, full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// waitManager blocks until the container's systemd answers with a state it
// recognizes, which is all `systemctl daemon-reload` needs. "starting" counts
// and "degraded" counts equally: gahaku.service is expected to fail here (see
// quadlet_test.go), so a wait for "running" would never return, and a wait for
// the boot to finish would fold the gateway's image pull into this deadline.
func (s *systemd) waitManager(t *testing.T) {
	t.Helper()
	var last string
	for deadline := time.Now().Add(managerTimeout); time.Now().Before(deadline); {
		// The error is ignored on purpose: is-system-running exits non-zero
		// for every state but "running", including every state below, and
		// exits non-zero early on too while the bus is still coming up.
		last, _ = s.exec(t.Context(), "systemctl", "is-system-running")
		switch last {
		case "initializing", "starting", "running", "degraded", "maintenance":
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("systemd did not come up within %s, last state %q\n%s",
		managerTimeout, last, s.diagnose(t))
}

// waitActive polls one generated unit until systemd calls it active. The
// gateway's own readiness is checked separately over HTTP: the unit is
// deliberately left at podman's default notify mode, so active here means the
// container started, not that it is answering yet.
func (s *systemd) waitActive(t *testing.T, unit string) {
	t.Helper()
	var last string
	for deadline := time.Now().Add(unitTimeout); time.Now().Before(deadline); {
		last, _ = s.exec(t.Context(), "systemctl", "is-active", unit)
		if last == "active" {
			return
		}
		if last == "failed" {
			t.Fatalf("%s failed to start\n%s", unit, s.diagnose(t))
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("%s was %q rather than active within %s\n%s",
		unit, last, unitTimeout, s.diagnose(t))
}

// diagnose collects what makes a convergence failure readable: the generated
// units, why systemd is unhappy with them, and what the inner podman did.
func (s *systemd) diagnose(t *testing.T) string {
	t.Helper()
	var report strings.Builder
	commands := [][]string{
		{"systemctl", "list-units", "--all", "--no-pager", "--no-legend", "gahaku*", "versitygw*"},
		{"systemctl", "status", "--no-pager", "--full", "versitygw.service"},
		{"journalctl", "--no-pager", "--lines=80", "--unit=versitygw.service"},
		{"podman", "ps", "--all"},
		{"podman", "logs", "versitygw"},
	}
	for _, command := range commands {
		out, err := s.exec(context.WithoutCancel(t.Context()), command...)
		fmt.Fprintf(&report, "\n--- %s ---\n%s\n", strings.Join(command, " "), out)
		if err != nil {
			fmt.Fprintf(&report, "(exited with %v)\n", err)
		}
	}
	return report.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return addr
}
