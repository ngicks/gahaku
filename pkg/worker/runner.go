package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sync/semaphore"
)

// DefaultTimeout bounds one job's run.
const DefaultTimeout = 2 * time.Minute

// DefaultWaitDelay bounds the drain of a worker's pipes after it exits.
const DefaultWaitDelay = 3 * time.Second

// DefaultStderrTailBytes is how much of a failed worker's log a typed error
// carries.
const DefaultStderrTailBytes = 8 << 10

// maxStdoutBytes bounds the captured stdout. The result frame is small;
// anything else on that pipe is noise the frame is fished out of.
const maxStdoutBytes = 1 << 20

// selfExe is the worker binary by the path that keeps pointing at the running
// image even after the file is replaced or removed underneath it.
const selfExe = "/proc/self/exe"

// Config configures a Runner. The zero value is usable: it re-executes the
// running binary, runs as many workers as there are CPUs, and gives each job
// DefaultTimeout.
type Config struct {
	// MaxConcurrent caps how many workers run at once. 0 or less selects
	// runtime.NumCPU().
	MaxConcurrent int
	// Timeout bounds a single job, counted from the moment it holds a worker
	// slot: queue wait is bounded by the caller's context instead, so a busy
	// queue does not eat into render time. 0 or less selects DefaultTimeout.
	Timeout time.Duration
	// WaitDelay bounds how long a process that outlived its worker may hold
	// the worker's pipes open before Run stops draining them. 0 or less
	// selects DefaultWaitDelay.
	WaitDelay time.Duration
	// StderrTailBytes bounds the log excerpt a failure carries. 0 or less
	// selects DefaultStderrTailBytes.
	StderrTailBytes int
	// ExePath is the binary re-executed as the worker. Empty selects
	// /proc/self/exe.
	ExePath string
	// ExtraEnv is appended to the orchestrator's own environment for every
	// worker.
	ExtraEnv []string
	// Stderr receives worker logs as they arrive; it must be safe for
	// concurrent use, since every running worker writes to it. nil selects
	// os.Stderr, io.Discard drops them.
	Stderr io.Writer
}

// Runner spawns worker subprocesses. It is safe for concurrent use.
type Runner struct {
	sem             *semaphore.Weighted
	timeout         time.Duration
	waitDelay       time.Duration
	stderrTailBytes int
	exePath         string
	extraEnv        []string
	stderr          io.Writer
}

// New returns a Runner from cfg, filling in the defaults cfg left out.
func New(cfg Config) *Runner {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = runtime.NumCPU()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	waitDelay := cfg.WaitDelay
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	stderrTailBytes := cfg.StderrTailBytes
	if stderrTailBytes <= 0 {
		stderrTailBytes = DefaultStderrTailBytes
	}
	exePath := cfg.ExePath
	if exePath == "" {
		exePath = selfExe
	}
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	return &Runner{
		sem:             semaphore.NewWeighted(int64(maxConcurrent)),
		timeout:         timeout,
		waitDelay:       waitDelay,
		stderrTailBytes: stderrTailBytes,
		exePath:         exePath,
		extraEnv:        cfg.ExtraEnv,
		stderr:          stderr,
	}
}

// Run renders one job: it waits for a worker slot, spawns `<exe> worker
// <kind>`, hands spec to it on stdin, passes its logs through to the
// configured stderr and decodes the result frame it leaves on stdout.
//
// ctx bounds the wait for a slot; the job itself additionally runs under the
// configured timeout. Canceling ctx or letting either deadline pass kills the
// worker's whole process group, so a converter it started dies with it.
//
// Failures come back as *KilledError, *OOMKilledError, *ExitError,
// *ProtocolError or, when the worker named what went wrong, the *JobError it
// reported — which pkg/server maps to status codes.
func (r *Runner) Run(ctx context.Context, kind Kind, spec Spec) (Result, error) {
	if !kind.valid() {
		return Result{}, fmt.Errorf("worker: unknown worker kind %q", kind)
	}
	encodedSpec, err := json.Marshal(spec)
	if err != nil {
		return Result{}, fmt.Errorf("worker: encoding job spec: %w", err)
	}

	if err := r.sem.Acquire(ctx, 1); err != nil {
		return Result{}, fmt.Errorf("worker: waiting for a worker slot: %w", err)
	}
	defer r.sem.Release(1)

	jobCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	stdout := newTailWriter(maxStdoutBytes, nil)
	stderr := newTailWriter(r.stderrTailBytes, r.stderr)

	cmd := exec.CommandContext(jobCtx, r.exePath, "worker", string(kind))
	cmd.Stdin = bytes.NewReader(encodedSpec)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), r.extraEnv...)
	cmd.WaitDelay = r.waitDelay
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("worker: starting %q: %w", r.exePath, err)
	}
	// Cmd.Cancel only runs when jobCtx is done, so this is what reaps a
	// straggler the worker left behind on the paths where nothing timed out.
	defer func() { _ = killProcessGroup(cmd.Process.Pid) }()

	waitErr := cmd.Wait()
	stderrTail := string(stderr.tail())
	f, frameErr := decodeFrame(stdout.tail())

	if waitErr != nil {
		if cause := jobCtx.Err(); cause != nil {
			return Result{}, &KilledError{Cause: cause, Timeout: r.timeout}
		}
		// A worker that exited fine but left a child holding its pipes open
		// trips WaitDelay. Its result frame is already on stdout, so the job
		// stands and only the straggler is lost.
		if !errors.Is(waitErr, exec.ErrWaitDelay) || cmd.ProcessState.ExitCode() != 0 {
			return Result{}, failure(waitErr, f, frameErr, stderrTail)
		}
	}

	if frameErr != nil {
		return Result{}, &ProtocolError{Err: frameErr, Stderr: stderrTail}
	}
	if f.Error != nil {
		// A worker that reports an error and still exits 0 is a bug in the
		// subcommand, but what it reported beats reporting a missing result.
		return Result{}, reported(f.Error, 0, stderrTail)
	}
	if f.Result == nil {
		return Result{}, &ProtocolError{
			Err:    errors.New("result frame carries neither a result nor an error"),
			Stderr: stderrTail,
		}
	}
	return *f.Result, nil
}

// failure classifies a worker that did not exit cleanly and that the
// orchestrator did not kill.
func failure(waitErr error, f frame, frameErr error, stderrTail string) error {
	exitErr, ok := errors.AsType[*exec.ExitError](waitErr)
	if !ok {
		return fmt.Errorf("worker: waiting for the worker: %w", waitErr)
	}
	state := exitErr.ProcessState
	if signal := signalOf(state); signal != 0 {
		if signal == syscall.SIGKILL {
			return &OOMKilledError{Stderr: stderrTail}
		}
		return &ExitError{ExitCode: state.ExitCode(), Signal: signal, Stderr: stderrTail}
	}
	if frameErr != nil || f.Error == nil {
		return &ExitError{ExitCode: state.ExitCode(), Stderr: stderrTail}
	}
	return reported(f.Error, state.ExitCode(), stderrTail)
}

// reported is the failure the worker itself put in its frame: one it named
// keeps that name, so pkg/server can map it as the error it started out as,
// and one it did not stays the message of an *ExitError.
func reported(jobErr *JobError, exitCode int, stderrTail string) error {
	if jobErr.Code == "" {
		return &ExitError{ExitCode: exitCode, Message: jobErr.Message, Stderr: stderrTail}
	}
	classified := *jobErr
	classified.Stderr = stderrTail
	return &classified
}
