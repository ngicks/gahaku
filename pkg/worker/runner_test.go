package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

type runOutcome struct {
	result Result
	err    error
}

func runAsync(ctx context.Context, r *Runner, kind Kind, spec Spec) <-chan runOutcome {
	out := make(chan runOutcome, 1)
	go func() {
		result, err := r.Run(ctx, kind, spec)
		out <- runOutcome{result: result, err: err}
	}()
	return out
}

func TestRunPassesSpecAndReturnsResult(t *testing.T) {
	r, _ := newRunner(t, "success", Config{MaxConcurrent: 1})
	spec := newJob(t)

	result, err := r.Run(t.Context(), KindImage, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Pages) != 1 || result.Pages[0].PageIndex != 0 {
		t.Fatalf("Pages = %+v, want one page at index 0", result.Pages)
	}
	if result.DocumentPages != 3 {
		t.Fatalf("DocumentPages = %d, want 3", result.DocumentPages)
	}

	want, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := jobFile(t, spec, specFile); got != string(want) {
		t.Fatalf("worker received spec %s, want %s", got, want)
	}
	if got := jobFile(t, spec, argsFile); got != "worker image" {
		t.Fatalf("worker ran with args %q, want %q", got, "worker image")
	}
}

func TestRunRejectsUnknownKindWithoutSpawning(t *testing.T) {
	r, _ := newRunner(t, "success", Config{MaxConcurrent: 1})
	spec := newJob(t)

	_, err := r.Run(t.Context(), Kind("bogus"), spec)
	if err == nil || !strings.Contains(err.Error(), "unknown worker kind") {
		t.Fatalf("err = %v, want an unknown kind error", err)
	}
	if jobFileExists(t, spec, specFile) {
		t.Fatal("a worker ran for an unknown kind")
	}
}

func TestRunReportsWorkerError(t *testing.T) {
	r, logs := newRunner(t, "fail", Config{MaxConcurrent: 1})
	spec := newJob(t)

	_, err := r.Run(t.Context(), KindPdf, spec)
	exitErr, ok := errors.AsType[*ExitError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *ExitError", err, err)
	}
	if exitErr.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1", exitErr.ExitCode)
	}
	if exitErr.Message != "render exploded" {
		t.Fatalf("Message = %q, want %q", exitErr.Message, "render exploded")
	}
	if !strings.Contains(logs.String(), "render exploded") {
		t.Fatalf("worker logs = %q, want them passed through", logs.String())
	}
}

func TestRunKeepsTheClassOfAReportedFailure(t *testing.T) {
	r, _ := newRunner(t, "fail-classified", Config{MaxConcurrent: 1})

	_, err := r.Run(t.Context(), KindPdf, newJob(t))
	jobErr, ok := errors.AsType[*JobError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *JobError", err, err)
	}
	if jobErr.Code != "test.page_range" {
		t.Fatalf("Code = %q, want the class the worker named", jobErr.Code)
	}
	if jobErr.Message != "page range starts past the end" {
		t.Fatalf("Message = %q, want what the handler reported", jobErr.Message)
	}
	if got := string(jobErr.Details); got != `{"firstPage":5,"documentPages":2}` {
		t.Fatalf("Details = %s, want the fields the class crossed with", got)
	}
	if !strings.Contains(jobErr.Stderr, "page range starts past the end") {
		t.Fatalf("Stderr = %q, want the tail of the worker's log", jobErr.Stderr)
	}
}

func TestRunMapsNonzeroExitToExitErrorWithBoundedStderr(t *testing.T) {
	const tailBytes = 128
	r, logs := newRunner(t, "exit-nonzero", Config{
		MaxConcurrent:   1,
		StderrTailBytes: tailBytes,
	})

	_, err := r.Run(t.Context(), KindOffice, newJob(t))
	exitErr, ok := errors.AsType[*ExitError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *ExitError", err, err)
	}
	if exitErr.ExitCode != 3 || exitErr.Signal != 0 {
		t.Fatalf(
			"ExitCode = %d, Signal = %v, want 3 and no signal",
			exitErr.ExitCode,
			exitErr.Signal,
		)
	}
	if len(exitErr.Stderr) > tailBytes {
		t.Fatalf("Stderr is %d bytes, want at most %d", len(exitErr.Stderr), tailBytes)
	}
	if !strings.Contains(exitErr.Stderr, "the last thing it said") {
		t.Fatalf("Stderr = %q, want the tail of the worker's log", exitErr.Stderr)
	}
	if len(logs.String()) <= tailBytes {
		t.Fatalf("pass-through logs are %d bytes, want the untruncated log", len(logs.String()))
	}
}

func TestRunMapsSigkillToOomKilled(t *testing.T) {
	r, _ := newRunner(t, "sigkill-self", Config{MaxConcurrent: 1})

	_, err := r.Run(t.Context(), KindPdf, newJob(t))
	if _, ok := errors.AsType[*OOMKilledError](err); !ok {
		t.Fatalf("err = %v (%T), want *OOMKilledError", err, err)
	}
}

func TestRunMapsOtherSignalToExitError(t *testing.T) {
	r, _ := newRunner(t, "sigterm-self", Config{MaxConcurrent: 1})

	_, err := r.Run(t.Context(), KindPdf, newJob(t))
	exitErr, ok := errors.AsType[*ExitError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *ExitError", err, err)
	}
	if exitErr.Signal != syscall.SIGTERM {
		t.Fatalf("Signal = %v, want SIGTERM", exitErr.Signal)
	}
}

func TestRunReportsUnframedStdout(t *testing.T) {
	r, _ := newRunner(t, "garbage", Config{MaxConcurrent: 1})

	_, err := r.Run(t.Context(), KindImage, newJob(t))
	if _, ok := errors.AsType[*ProtocolError](err); !ok {
		t.Fatalf("err = %v (%T), want *ProtocolError", err, err)
	}
	if !errors.Is(err, errNoFrame) {
		t.Fatalf("err = %v, want it to wrap errNoFrame", err)
	}
}

func TestRunKillsWorkerOnJobDeadline(t *testing.T) {
	r, _ := newRunner(t, "sleep", Config{
		MaxConcurrent: 1,
		Timeout:       200 * time.Millisecond,
	})

	_, err := r.Run(t.Context(), KindOffice, newJob(t))
	if _, ok := errors.AsType[*KilledError](err); !ok {
		t.Fatalf("err = %v (%T), want *KilledError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to unwrap to context.DeadlineExceeded", err)
	}
}

func TestRunKillsWorkerOnContextCancel(t *testing.T) {
	r, _ := newRunner(t, "wait-fifo", Config{MaxConcurrent: 1})
	spec := newJob(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := runAsync(ctx, r, KindImage, spec)
	openFifo(t, spec)

	cancel()
	got := <-done
	if _, ok := errors.AsType[*KilledError](got.err); !ok {
		t.Fatalf("err = %v (%T), want *KilledError", got.err, got.err)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want it to unwrap to context.Canceled", got.err)
	}
}

func TestRunCapsConcurrentWorkers(t *testing.T) {
	r, _ := newRunner(t, "wait-fifo", Config{MaxConcurrent: 1})

	first := newJob(t)
	firstDone := runAsync(t.Context(), r, KindImage, first)
	// Opening the fifo returns once the first worker opened its end, so the
	// only slot is taken from here on.
	firstFifo := openFifo(t, first)

	second := newJob(t)
	secondCtx, cancelSecond := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancelSecond()
	_, err := r.Run(secondCtx, KindImage, second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the queue wait to time out", err)
	}
	if _, ok := errors.AsType[*KilledError](err); ok {
		t.Fatalf("err = %v, want a queue wait timeout rather than a killed worker", err)
	}
	if jobFileExists(t, second, specFile) {
		t.Fatal("a second worker ran while the only slot was taken")
	}

	if err := firstFifo.Close(); err != nil {
		t.Fatalf("releasing the first worker: %v", err)
	}
	if got := <-firstDone; got.err != nil {
		t.Fatalf("first job: %v", got.err)
	}

	third := newJob(t)
	thirdDone := runAsync(t.Context(), r, KindImage, third)
	// This open only returns once the third worker started, which it can only
	// do on the slot the first job released.
	thirdFifo := openFifo(t, third)
	if err := thirdFifo.Close(); err != nil {
		t.Fatalf("releasing the third worker: %v", err)
	}
	if got := <-thirdDone; got.err != nil {
		t.Fatalf("third job: %v", got.err)
	}
}

func TestRunHonorsCancelWhileWaitingForSlot(t *testing.T) {
	r, _ := newRunner(t, "wait-fifo", Config{MaxConcurrent: 1})

	first := newJob(t)
	firstDone := runAsync(t.Context(), r, KindImage, first)
	firstFifo := openFifo(t, first)

	ctx, cancel := context.WithCancel(t.Context())
	second := newJob(t)
	secondDone := runAsync(ctx, r, KindImage, second)
	cancel()

	got := <-secondDone
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", got.err)
	}
	if jobFileExists(t, second, specFile) {
		t.Fatal("a second worker ran while the only slot was taken")
	}

	if err := firstFifo.Close(); err != nil {
		t.Fatalf("releasing the first worker: %v", err)
	}
	if got := <-firstDone; got.err != nil {
		t.Fatalf("first job: %v", got.err)
	}
}

func TestRunKillsTheWholeProcessGroup(t *testing.T) {
	r, _ := newRunner(t, "grandchild-block", Config{MaxConcurrent: 1})
	spec := newJob(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := runAsync(ctx, r, KindOffice, spec)
	openFifo(t, spec)

	pid := grandchildPid(t, spec)
	if !processAlive(t, pid) {
		t.Fatalf("grandchild %d is not running", pid)
	}

	cancel()
	got := <-done
	if _, ok := errors.AsType[*KilledError](got.err); !ok {
		t.Fatalf("err = %v (%T), want *KilledError", got.err, got.err)
	}
	waitProcessGone(t, pid)
}

func TestRunKeepsResultWhenAStragglerHoldsThePipes(t *testing.T) {
	r, _ := newRunner(t, "grandchild-exit", Config{
		MaxConcurrent: 1,
		WaitDelay:     200 * time.Millisecond,
	})
	spec := newJob(t)

	result, err := r.Run(t.Context(), KindOffice, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Pages) != 1 {
		t.Fatalf("Pages = %+v, want one page", result.Pages)
	}
	waitProcessGone(t, grandchildPid(t, spec))
}
