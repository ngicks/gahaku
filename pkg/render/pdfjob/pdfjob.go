// Package pdfjob rasterizes a PDF into one image per page.
//
// The rendering is pdfium's, running as WebAssembly on wazero (D-A): no native
// pdfium to distribute and no CGo, so the worker stays a plain Go binary. The
// wasm runtime starts inside the worker subprocess, which is what keeps a
// document that hangs or eats memory to the one process rendering it (D-B).
//
// The requested page range is resolved against the document's real page count
// and that count travels back in the result, which is how pkg/server tells a
// range that merely ran past the end from one that selected nothing (D-01).
package pdfjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/worker"
)

const (
	// defaultDpi is the density a request without one gets: 150 dpi is about
	// twice screen resolution, which keeps text legible when the page is
	// viewed at full width without doubling the pixels again at 300.
	defaultDpi = 150
	// defaultFormat is what a request without one gets. A page render is
	// flat-shaded text and line art, which png keeps sharp and lossless
	// while jpeg would ring around every glyph.
	defaultFormat = worker.FormatPng
)

// errEmptyDocument stands in for a load error pdfium does not produce: handed
// a zero-length reader it complains about the missing size rather than about
// the document, and that would reach pkg/server as INTERNAL instead of the
// INVALID_ARGUMENT an empty file deserves.
var errEmptyDocument = errors.New("the input document is empty")

var _ worker.HandlerFunc = Render

// pool compiles the pdfium wasm module once per process instead of once per
// job. Compiling it costs a couple of seconds while claiming an instance from
// the compiled module costs milliseconds, and a worker process renders exactly
// one job before exiting (D-08), so nothing here outlives a job that a per-call
// pool would have released — what the singleton buys is that a caller
// rendering several documents in one process pays the compile once.
var pool = sync.OnceValues(func() (pdfium.Pool, error) {
	p, err := webassembly.Init(webassembly.Config{
		// One instance, matching the one job this process renders. A second
		// concurrent caller waits for it rather than compiling its own.
		MaxIdle:  1,
		MaxTotal: 1,
		// The document reaches pdfium through a reader, so the module never
		// opens a path and the default config's mount of / into the sandbox
		// would only be reachable surface for nothing.
		FSConfig: wazero.NewFSConfig(),
		// stdout is where the worker leaves its result frame, and pkg/worker
		// keeps only the tail of it: a document that made pdfium write enough
		// there could push the frame out of what the orchestrator captured.
		Stdout: os.Stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("pdfjob: starting the pdfium wasm runtime: %w", err)
	}
	return p, nil
})

// Render rasterizes the pages spec.Options.Pages selects out of the PDF at
// spec.InputPath, scales each to fit spec.Options.Resize and writes them into
// spec.JobDir. It is the entry point `gahaku worker pdf` runs.
func Render(ctx context.Context, spec worker.Spec) (worker.Result, error) {
	// Validated before the input is touched so an unrenderable request costs
	// neither a read nor a wasm instance.
	format := spec.Options.Format
	if format == "" {
		format = defaultFormat
	}
	if !render.CanEncode(format) {
		return worker.Result{}, &render.UnsupportedFormatError{Format: format}
	}
	dpi := defaultDpi
	if spec.Options.Dpi > 0 {
		dpi = int(spec.Options.Dpi)
	}
	if err := ctx.Err(); err != nil {
		return worker.Result{}, err
	}

	file, err := os.Open(spec.InputPath)
	if err != nil {
		return worker.Result{}, fmt.Errorf("pdfjob: opening input document: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return worker.Result{}, fmt.Errorf("pdfjob: reading input document: %w", err)
	}
	if info.Size() == 0 {
		return worker.Result{}, &render.DecodeError{Err: errEmptyDocument}
	}

	p, err := pool()
	if err != nil {
		return worker.Result{}, err
	}
	// With the job's context the wait for the instance ends when the job's
	// deadline does, rather than lasting as long as whoever holds it.
	instance, err := p.GetInstanceWithContext(ctx)
	if err != nil {
		return worker.Result{}, fmt.Errorf("pdfjob: claiming a pdfium instance: %w", err)
	}
	defer func() { _ = instance.Close() }()

	document, err := instance.OpenDocument(&requests.OpenDocument{
		FileReader:     file,
		FileReaderSize: info.Size(),
	})
	if err != nil {
		return worker.Result{}, documentError(err)
	}
	defer func() {
		_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
			Document: document.Document,
		})
	}()

	// Not documentError: counting the pages of a document pdfium already
	// opened can only fail on the wasm side, which is this binary's problem
	// and not something the caller handed us.
	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{
		Document: document.Document,
	})
	if err != nil {
		return worker.Result{}, fmt.Errorf("pdfjob: counting the document's pages: %w", err)
	}
	first, last, err := render.NormalizePageRange(spec.Options.Pages, count.PageCount)
	if err != nil {
		return worker.Result{}, err
	}

	renderer := pageRenderer{
		instance: instance,
		document: document.Document,
		dpi:      dpi,
		format:   format,
		jobDir:   spec.JobDir,
		resize:   spec.Options.Resize,
	}
	pages := make([]worker.Page, 0, last-first+1)
	for page := first; page <= last; page++ {
		if err := ctx.Err(); err != nil {
			return worker.Result{}, err
		}
		rendered, err := renderer.render(page, len(pages))
		if err != nil {
			return worker.Result{}, err
		}
		pages = append(pages, rendered)
	}
	return worker.Result{Pages: pages, DocumentPages: count.PageCount}, nil
}

// pageRenderer holds what every page of one job renders with.
type pageRenderer struct {
	instance pdfium.Pdfium
	document references.FPDF_DOCUMENT
	dpi      int
	format   worker.Format
	jobDir   string
	resize   worker.Resize
}

// render rasterizes the page-th page of the document, 1-based, and writes it
// into the job directory as the outputIndex-th page of the render.
//
// A page renders in its own function because the image pdfium hands back is a
// view into the wasm module's memory rather than a copy of it: releasing that
// memory has to wait until the page is encoded, and has to happen before the
// next page's bitmap claims it back.
func (r pageRenderer) render(page, outputIndex int) (worker.Page, error) {
	rendered, err := r.instance.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI: r.dpi,
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: r.document, Index: page - 1},
		},
	})
	if err != nil {
		return worker.Page{}, fmt.Errorf("pdfjob: rasterizing page %d: %w", page, err)
	}
	defer rendered.Cleanup()

	scaled := render.Scale(rendered.Result.Image, r.resize)
	return render.WritePage(r.jobDir, outputIndex, scaled, r.format)
}

// documentErrors are the pdfium failures that describe the document rather
// than the runtime, so they are the input's fault and not gahaku's. Password
// and security are among them by choice: pkg/render has no encrypted-document
// error of its own, and INVALID_ARGUMENT is the honest answer for a document
// the caller gave no way to open. The rest of the pdfium error set
// (ErrUnsupportedOnWebassembly and the other build-mode ones) is about how
// this binary was built, which no caller can fix.
var documentErrors = []error{
	pdfiumerrors.ErrFile,
	pdfiumerrors.ErrFormat,
	pdfiumerrors.ErrPage,
	pdfiumerrors.ErrPassword,
	pdfiumerrors.ErrSecurity,
	pdfiumerrors.ErrUnknown,
}

func documentError(err error) error {
	for _, target := range documentErrors {
		if errors.Is(err, target) {
			return &render.DecodeError{Err: err}
		}
	}
	return fmt.Errorf("pdfjob: reading input document: %w", err)
}
