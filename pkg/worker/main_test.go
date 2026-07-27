package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// behaviorEnv makes the test binary stand in for a worker: TestMain dispatches
// on it before the test framework starts, so a Runner pointed at os.Executable
// spawns a fake worker that behaves however the test needs.
const behaviorEnv = "GAHAKU_TEST_WORKER_BEHAVIOR"

const (
	// fifoName is the named pipe a job directory carries so a test and its
	// worker can hand off without polling: opening one end blocks until the
	// other end opens.
	fifoName = "handshake.fifo"
	// specFile and argsFile are what a fake worker records about the job it
	// was handed.
	specFile = "spec.json"
	argsFile = "args.txt"
	// pidFile holds the pid of the process a fake worker spawned and left
	// running.
	pidFile = "grandchild.pid"
)

func TestMain(m *testing.M) {
	if behavior := os.Getenv(behaviorEnv); behavior != "" {
		os.Exit(fakeWorker(behavior))
	}
	os.Exit(m.Run())
}

func fakeWorker(behavior string) int {
	ctx := context.Background()
	switch behavior {
	case "success":
		return handleJob(ctx, recordingHandler(nil, false), nil)
	case "fail":
		return handleJob(ctx, recordingHandler(errors.New("render exploded"), false), nil)
	case "fail-classified":
		return handleJob(
			ctx,
			recordingHandler(errors.New("page range starts past the end"), false),
			classifyTestFailure,
		)
	case "wait-fifo":
		return handleJob(ctx, recordingHandler(nil, true), nil)
	case "sleep":
		time.Sleep(time.Hour)
		return 0
	case "exit-nonzero":
		fmt.Fprint(os.Stderr, strings.Repeat("noise from the renderer\n", 200))
		fmt.Fprintln(os.Stderr, "the last thing it said")
		return 3
	case "sigkill-self":
		return raise(syscall.SIGKILL)
	case "sigterm-self":
		return raise(syscall.SIGTERM)
	case "garbage":
		fmt.Fprintln(os.Stdout, "this is not a result frame")
		return 0
	case "grandchild-block":
		return handleJob(ctx, grandchildHandler(true), nil)
	case "grandchild-exit":
		return handleJob(ctx, grandchildHandler(false), nil)
	default:
		fmt.Fprintf(os.Stderr, "fake worker: unknown behavior %q\n", behavior)
		return 2
	}
}

func handleJob(ctx context.Context, h HandlerFunc, classify Classifier) int {
	if err := Handle(ctx, os.Stdin, os.Stdout, h, classify); err != nil {
		fmt.Fprintln(os.Stderr, "fake worker:", err)
		return 1
	}
	return 0
}

// classifyTestFailure stands in for pkg/render/workererr: this package cannot
// import the render packages and does not have to, since it frames a class
// without reading it.
func classifyTestFailure(err error) *JobError {
	return &JobError{
		Code:    "test.page_range",
		Message: err.Error(),
		Details: json.RawMessage(`{"firstPage":5,"documentPages":2}`),
	}
}

func raise(sig syscall.Signal) int {
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		fmt.Fprintln(os.Stderr, "fake worker:", err)
		return 2
	}
	time.Sleep(time.Hour)
	return 0
}

// recordingHandler leaves the spec it received and the command line it ran
// under in the job directory, so a test can assert what crossed the pipe. With
// waitFifo set it then blocks on the job's fifo, which is how a test holds a
// worker running for as long as it needs one.
func recordingHandler(fail error, waitFifo bool) HandlerFunc {
	return func(_ context.Context, spec Spec) (Result, error) {
		encoded, err := json.Marshal(spec)
		if err != nil {
			return Result{}, err
		}
		if err := writeJobFile(spec.JobDir, specFile, encoded); err != nil {
			return Result{}, err
		}
		args := []byte(strings.Join(os.Args[1:], " "))
		if err := writeJobFile(spec.JobDir, argsFile, args); err != nil {
			return Result{}, err
		}
		if fail != nil {
			return Result{}, fail
		}
		if waitFifo {
			if err := drainFifo(spec.JobDir); err != nil {
				return Result{}, err
			}
		}
		return renderedResult(spec), nil
	}
}

// grandchildHandler spawns a process the worker never waits for — soffice, in
// production — and reports its pid through the job directory.
func grandchildHandler(block bool) HandlerFunc {
	return func(_ context.Context, spec Spec) (Result, error) {
		exe, err := os.Executable()
		if err != nil {
			return Result{}, err
		}
		cmd := exec.Command(exe, "worker", "grandchild")
		cmd.Env = append(os.Environ(), behaviorEnv+"=sleep")
		// Inheriting the worker's pipes is the point: that is what keeps
		// Cmd.Wait from returning once the grandchild outlives the worker.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return Result{}, err
		}
		pid := []byte(strconv.Itoa(cmd.Process.Pid))
		if err := writeJobFile(spec.JobDir, pidFile, pid); err != nil {
			return Result{}, err
		}
		if block {
			if err := drainFifo(spec.JobDir); err != nil {
				return Result{}, err
			}
		}
		return renderedResult(spec), nil
	}
}

func renderedResult(spec Spec) Result {
	return Result{
		Pages:         []Page{{PageIndex: 0, Path: filepath.Join(spec.JobDir, "page-0.png")}},
		DocumentPages: 3,
	}
}

func writeJobFile(jobDir, name string, content []byte) error {
	return os.WriteFile(filepath.Join(jobDir, name), content, 0o600)
}

// drainFifo blocks until the test opens the write end and then until it closes
// it again.
func drainFifo(jobDir string) error {
	f, err := os.Open(filepath.Join(jobDir, fifoName))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(io.Discard, f)
	return err
}

// syncBuffer collects pass-through worker logs. Every running worker writes to
// the Runner's stderr sink, so it has to tolerate concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// newRunner builds a Runner that re-executes the test binary as its worker,
// running the named behavior.
func newRunner(t *testing.T, behavior string, cfg Config) (*Runner, *syncBuffer) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	logs := &syncBuffer{}
	cfg.ExePath = exe
	cfg.ExtraEnv = append(cfg.ExtraEnv, behaviorEnv+"="+behavior)
	cfg.Stderr = logs
	if cfg.WaitDelay == 0 {
		cfg.WaitDelay = 500 * time.Millisecond
	}
	return New(cfg), logs
}

// newJob returns a spec whose job directory already holds the handshake fifo.
func newJob(t *testing.T) Spec {
	t.Helper()
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, fifoName), 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	return Spec{
		InputPath: filepath.Join(dir, "input.bin"),
		JobDir:    dir,
		Options: Options{
			Format: FormatPng,
			Resize: Resize{MaxWidth: 800},
			Dpi:    150,
			Pages:  PageRange{FirstPage: 2, LastPage: 4},
		},
	}
}

// openFifo blocks until the worker of this job opened its end, which is the
// handshake proving the worker is running. Closing the returned file lets the
// worker finish.
func openFifo(t *testing.T, spec Spec) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(spec.JobDir, fifoName), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening fifo: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func jobFile(t *testing.T, spec Spec, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(spec.JobDir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(content)
}

func jobFileExists(t *testing.T, spec Spec, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(spec.JobDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", name, err)
	}
	return err == nil
}

// waitProcessGone polls until pid is gone. A killed grandchild lingers as a
// zombie until it is reaped, and a zombie still answers kill(pid, 0), so the
// state comes from /proc instead.
func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(t, pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("reading process state: %v", err)
	}
	// The comm field is parenthesized and may itself contain spaces, so the
	// state is the first field after the last ')'.
	rest := string(stat[strings.LastIndex(string(stat), ")")+1:])
	fields := strings.Fields(rest)
	return len(fields) > 0 && fields[0] != "Z"
}

func grandchildPid(t *testing.T, spec Spec) int {
	t.Helper()
	pid, err := strconv.Atoi(jobFile(t, spec, pidFile))
	if err != nil {
		t.Fatalf("parsing grandchild pid: %v", err)
	}
	return pid
}
