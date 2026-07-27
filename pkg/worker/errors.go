package worker

import (
	"encoding/json"
	"fmt"
	"syscall"
	"time"
)

// KilledError reports a worker the orchestrator killed, either because the
// job's deadline passed or because the caller's context was canceled. It
// unwraps to that context error, so one errors.Is on context.DeadlineExceeded
// covers both a job killed mid-render and a job that never got a worker slot.
// Maps to DEADLINE_EXCEEDED, or CANCELED for context.Canceled.
type KilledError struct {
	// Cause is context.DeadlineExceeded or context.Canceled.
	Cause error
	// Timeout is the per-job deadline that was in force.
	Timeout time.Duration
}

func (e *KilledError) Error() string {
	return fmt.Sprintf("worker: killed after %s: %s", e.Timeout, e.Cause)
}

func (e *KilledError) Unwrap() error { return e.Cause }

// OOMKilledError reports a worker that died of SIGKILL without the
// orchestrator killing it, which in practice means the kernel OOM killer took
// it. The signal is all we get: an operator kill or a container runtime
// stopping the process looks the same, so the label is a heuristic (see
// PLAN.md risks). Maps to RESOURCE_EXHAUSTED.
type OOMKilledError struct {
	// Stderr is the tail of what the worker logged before it died.
	Stderr string
}

func (e *OOMKilledError) Error() string {
	return fmt.Sprintf("worker: killed by SIGKILL, likely out of memory%s", stderrSuffix(e.Stderr))
}

// ExitError reports a worker that failed on its own — a nonzero exit status,
// or death by a signal we did not send and that is not the OOM killer's.
// Maps to INTERNAL.
type ExitError struct {
	// ExitCode is the worker's exit status, -1 when a signal killed it.
	ExitCode int
	// Signal is the signal that killed the worker, 0 when it exited normally.
	Signal syscall.Signal
	// Message is what the worker reported in its result frame, empty when it
	// died before writing one.
	Message string
	// Stderr is the tail of what the worker logged.
	Stderr string
}

func (e *ExitError) Error() string {
	what := fmt.Sprintf("exited with status %d", e.ExitCode)
	if e.Signal != 0 {
		what = fmt.Sprintf("died of signal %s", e.Signal)
	}
	if e.Message != "" {
		return fmt.Sprintf("worker: %s: %s%s", what, e.Message, stderrSuffix(e.Stderr))
	}
	return fmt.Sprintf("worker: %s%s", what, stderrSuffix(e.Stderr))
}

// JobError reports a failure the worker itself named before framing it, so a
// document a pipeline refused arrives here as what it is instead of as the
// message of an *ExitError.
//
// Code and Details are opaque to this package: it frames whatever the
// Classifier at the Handle call site produced and hands it back unread, which
// is what lets the classes mean something without this package knowing the
// render errors they stand for. Maps to whatever the classifier's owner
// translates the class back into (pkg/render/workererr); a class no build
// understands is INTERNAL like any other worker failure.
type JobError struct {
	// Code names the failure class.
	Code string `json:"code"`
	// Message is what the handler's error reported.
	Message string `json:"message"`
	// Details carries the class's structured fields in the shape its owner
	// marshaled them, empty for a class that carries none.
	Details json.RawMessage `json:"details,omitzero"`
	// Stderr is the tail of what the worker logged. It does not travel in the
	// frame: the orchestrator fills it in, as it does for the failures it
	// classifies itself.
	Stderr string `json:"-"`
}

func (e *JobError) Error() string {
	return fmt.Sprintf("worker: %s%s", e.Message, stderrSuffix(e.Stderr))
}

// ProtocolError reports a worker that exited fine but left no decodable result
// frame on stdout. Maps to INTERNAL.
type ProtocolError struct {
	Err error
	// Stderr is the tail of what the worker logged.
	Stderr string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("worker: %s%s", e.Err, stderrSuffix(e.Stderr))
}

func (e *ProtocolError) Unwrap() error { return e.Err }

func stderrSuffix(stderr string) string {
	if stderr == "" {
		return ""
	}
	return "; stderr: " + stderr
}
