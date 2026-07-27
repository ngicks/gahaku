package e2e

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
)

const (
	// startTimeout bounds the wait for a started server to accept connections.
	startTimeout = 30 * time.Second
	// stopTimeout bounds the graceful stop before the process is killed.
	stopTimeout = 15 * time.Second
)

// syncBuffer collects a server's output; its stdout and stderr are two pipes
// os/exec copies from concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gahakuServer is a `gahaku serve` process under test.
type gahakuServer struct {
	addr string
	logs *syncBuffer
	// exited carries the process's wait status once it is over. Every reader
	// puts it back, so the readiness loop and the cleanup can both look.
	exited chan error
}

// startServer runs `gahaku serve` with args appended to the flags every test
// needs — an address of its own and a job directory of its own — and returns
// once it is accepting connections.
func startServer(t *testing.T, args ...string) *gahakuServer {
	t.Helper()
	requireBinary(t)

	server := &gahakuServer{
		addr:   freeAddr(t),
		logs:   &syncBuffer{},
		exited: make(chan error, 1),
	}
	serveArgs := append([]string{
		"serve",
		"--listen", server.addr,
		"--temp-dir", t.TempDir(),
	}, args...)

	cmd := exec.Command(gahakuBinary, serveArgs...)
	cmd.Stdout = server.logs
	cmd.Stderr = server.logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s %v: %v", gahakuBinary, serveArgs, err)
	}
	go func() { server.exited <- cmd.Wait() }()

	t.Cleanup(func() {
		// The signal path a deployment stops the server on, rather than a kill:
		// it is the one that has to leave no worker behind.
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-server.exited:
			server.exited <- err
		case <-time.After(stopTimeout):
			_ = cmd.Process.Kill()
			<-server.exited
		}
		if t.Failed() {
			t.Logf("server log:\n%s", server.logs)
		}
	})

	server.waitReady(t)
	return server
}

// waitReady polls the listener, watching for the process dying meanwhile: a
// server that refuses its flags must fail the test with what it printed rather
// than after a dial timeout that says nothing.
func (s *gahakuServer) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-s.exited:
			s.exited <- err
			t.Fatalf("the server exited before it listened: %v\n%s", err, s.logs)
		default:
		}
		conn, err := net.DialTimeout("tcp", s.addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the server did not listen on %s within %s\n%s", s.addr, startTimeout, s.logs)
}

func (s *gahakuServer) client(t *testing.T) gahakuv1.GahakuServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(s.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialing %s: %v", s.addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gahakuv1.NewGahakuServiceClient(conn)
}

// freeAddr picks a loopback address nothing is listening on. The port is
// released before the server claims it, which is the one race in this harness;
// a random ephemeral port makes it small enough to live with, and the
// alternative — the server reporting the port it bound — is not something the
// binary offers.
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
