package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// HandlerFunc renders one job. It runs inside the worker subprocess, where the
// render packages implement it.
type HandlerFunc func(ctx context.Context, spec Spec) (Result, error)

// Classifier names the class a handler failure crosses the protocol as, message
// and structured fields included, and returns nil for an error it does not
// recognize.
//
// It is what keeps this package out of the render packages: the classes and
// what they translate back into live with the errors they stand for
// (pkg/render/workererr), and Handle only frames what the classifier produced.
type Classifier func(error) *JobError

// Handle runs one job from the worker side: it decodes the job spec from
// stdin, hands it to h, and writes the framed outcome to stdout. The hidden
// `gahaku worker <kind>` subcommands are a flag parse plus this call.
//
// A frame goes out on both paths, so a worker that failed still reports why
// instead of leaving the orchestrator with an exit status. The error is
// returned as well: the subcommand exits nonzero on it, which is what the
// orchestrator classifies.
//
// classify names what went wrong for the orchestrator; a nil classify, or one
// that does not recognize the failure, leaves it as the bare message the
// orchestrator reports as an *ExitError.
func Handle(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	h HandlerFunc,
	classify Classifier,
) error {
	result, err := runJob(ctx, stdin, h)
	f := frame{Result: &result}
	if err != nil {
		f = frame{Error: frameError(err, classify)}
	}
	return errors.Join(err, writeFrame(stdout, f))
}

func frameError(err error, classify Classifier) *JobError {
	if classify != nil {
		if jobErr := classify(err); jobErr != nil {
			return jobErr
		}
	}
	return &JobError{Message: err.Error()}
}

func runJob(ctx context.Context, stdin io.Reader, h HandlerFunc) (Result, error) {
	encoded, err := io.ReadAll(stdin)
	if err != nil {
		return Result{}, fmt.Errorf("worker: reading job spec: %w", err)
	}
	var spec Spec
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return Result{}, fmt.Errorf("worker: decoding job spec: %w", err)
	}
	return h(ctx, spec)
}
