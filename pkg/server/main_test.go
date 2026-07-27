package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/render/officejob"
	"github.com/ngicks/gahaku/pkg/render/workererr"
	"github.com/ngicks/gahaku/pkg/worker"
)

// failureEnv makes the test binary stand in for a render worker: TestMain
// dispatches on it before the test framework starts, so a Service left to build
// its own *worker.Runner spawns a subprocess that fails the way a test asked
// for. That is what puts the crossing itself — the classifier, the result
// frame, the pipe, the exit status — inside the assertion, where a fakeRunner
// handing over a Go value would skip all of it.
const failureEnv = "GAHAKU_TEST_WORKER_FAILURE"

// workerFailures are the failures a spawned worker can be asked to stop on.
var workerFailures = map[string]error{
	"decode":     &render.DecodeError{Err: errors.New("unexpected EOF")},
	"page-range": &render.PageRangeOutOfRangeError{FirstPage: 5, DocumentPages: 2},
	"converter-missing": &officejob.ConverterMissingError{
		Binary: "soffice",
		Err:    errors.New("no soffice on PATH"),
	},
	"unclassified": errors.New("the renderer gave way"),
}

func TestMain(m *testing.M) {
	if failure := os.Getenv(failureEnv); failure != "" {
		os.Exit(failingWorker(failure))
	}
	os.Exit(m.Run())
}

func failingWorker(failure string) int {
	rendering, ok := workerFailures[failure]
	if !ok {
		fmt.Fprintf(os.Stderr, "fake worker: unknown failure %q\n", failure)
		return 2
	}
	handler := func(context.Context, worker.Spec) (worker.Result, error) {
		return worker.Result{}, rendering
	}
	err := worker.Handle(context.Background(), os.Stdin, os.Stdout, handler, workererr.Classify)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake worker:", err)
		return 1
	}
	return 0
}

// renderWithFailingWorker runs a job whose worker is this test binary, failing
// the named way, and returns the status the caller ends up with.
func renderWithFailingWorker(t *testing.T, failure string) error {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	client := newClient(t, Config{Worker: worker.Config{
		ExePath:  exe,
		ExtraEnv: []string{failureEnv + "=" + failure},
		Stderr:   io.Discard,
	}})
	_, got := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		chunkRequest(pdfDocument),
	)
	if got == nil {
		t.Fatal("want the worker's failure, got a rendered document")
	}
	return got
}
