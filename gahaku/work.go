package gahaku

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ngicks/gahaku/pkg/render/imagejob"
	"github.com/ngicks/gahaku/pkg/render/officejob"
	"github.com/ngicks/gahaku/pkg/render/pdfjob"
	"github.com/ngicks/gahaku/pkg/render/workererr"
	"github.com/ngicks/gahaku/pkg/worker"
)

// WorkOption carries what a worker run needs beside its kind.
type WorkOption struct {
	// Stdin carries the job spec; nil selects os.Stdin.
	Stdin io.Reader
	// Stdout carries the result frame back to the orchestrator and nothing
	// else — a pipeline logging there would break the protocol. nil selects
	// os.Stdout.
	Stdout io.Writer
	// Soffice is the LibreOffice binary the office pipeline converts with;
	// empty looks "soffice" up on PATH.
	Soffice string
}

// Work runs one rendering job of the given kind: it reads the job spec the
// orchestrator wrote to stdin, renders it, and writes the framed result back to
// stdout. It is what each hidden `gahaku worker <kind>` subcommand does.
//
// A failure crosses classified (pkg/render/workererr), so a document the
// pipeline refused reaches the server as the error it raised rather than as a
// worker that exited nonzero.
func Work(ctx context.Context, kind worker.Kind, opt WorkOption) error {
	handler, err := handler(kind, opt.Soffice)
	if err != nil {
		return err
	}
	stdin := io.Reader(os.Stdin)
	if opt.Stdin != nil {
		stdin = opt.Stdin
	}
	stdout := io.Writer(os.Stdout)
	if opt.Stdout != nil {
		stdout = opt.Stdout
	}
	return worker.Handle(ctx, stdin, stdout, handler, workererr.Classify)
}

// handler is the routing table between a worker kind and the pipeline that
// renders it, the server's sniffing and assertions having already decided which.
func handler(kind worker.Kind, soffice string) (worker.HandlerFunc, error) {
	switch kind {
	case worker.KindImage:
		return imagejob.Render, nil
	case worker.KindPdf:
		return pdfjob.Render, nil
	case worker.KindOffice:
		return officejob.Converter{Binary: soffice}.Render, nil
	default:
		return nil, fmt.Errorf("gahaku: unknown worker kind %q", kind)
	}
}
