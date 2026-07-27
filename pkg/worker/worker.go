// Package worker runs one rendering job in a subprocess.
//
// The server re-executes the gahaku binary as `gahaku worker <kind>` per job
// (D-08, D-B): the job spec goes in on stdin, a framed result comes back on
// stdout, and the worker's logs pass through on stderr. A weighted semaphore
// caps how many workers run at once, each job runs under its own deadline, and
// a job that outlives it has its whole process group killed so tools the worker
// spawned (soffice) die with it.
//
// Runner is the orchestrator half, used by pkg/server; Handle is the worker
// half, used by the hidden `gahaku worker <kind>` subcommands, so both ends of
// the stdio protocol live here.
//
// Nothing here knows about protobuf or gRPC: Options mirrors the proto
// RenderOptions as plain Go types, and failures are typed errors that
// pkg/server maps to status codes. Nor does it know the render packages: a
// failure they name crosses the pipe as the class a Classifier gave it, which
// this package frames and hands back without reading.
package worker

// Kind selects the render pipeline. It is also the subcommand argument the
// worker process is started with.
type Kind string

const (
	KindImage  Kind = "image"
	KindPdf    Kind = "pdf"
	KindOffice Kind = "office"
)

func (k Kind) valid() bool {
	switch k {
	case KindImage, KindPdf, KindOffice:
		return true
	}
	return false
}

// Spec is the job description the orchestrator writes to the worker's stdin.
//
// Documents travel as paths, never through the pipe: pkg/input has already
// materialized the input into the job directory the worker renders into.
type Spec struct {
	// InputPath is the materialized input document.
	InputPath string `json:"inputPath"`
	// JobDir is the per-job directory the worker renders into. It also holds
	// whatever scratch its pipeline needs — a soffice profile directory, an
	// intermediate PDF. The caller owns it: this package neither creates nor
	// removes it, so whoever made the directory has to clean it up once it has
	// read the rendered pages out of it.
	JobDir  string  `json:"jobDir"`
	Options Options `json:"options"`
}

// Options describes the raster to produce, mirroring the proto RenderOptions.
type Options struct {
	// Format is the encoding of the rendered pages. Empty selects the
	// pipeline's default.
	Format Format `json:"format,omitzero"`
	// Resize bounds the output; its zero value leaves the rendered size alone.
	Resize Resize `json:"resize,omitzero"`
	// Dpi is the rasterization density for page-based inputs (PDF, Office) and
	// is ignored by the image pipeline. 0 selects the pipeline's default.
	Dpi uint32 `json:"dpi,omitzero"`
	// Pages is the range to render; its zero value means the whole document.
	Pages PageRange `json:"pages,omitzero"`
}

// Format is the encoding of a rendered page.
type Format string

const (
	FormatPng  Format = "png"
	FormatJpeg Format = "jpeg"
	FormatGif  Format = "gif"
	FormatTiff Format = "tiff"
	FormatBmp  Format = "bmp"
	FormatWebp Format = "webp"
	FormatAvif Format = "avif"
)

// Resize bounds the output in pixels, preserving aspect ratio. Pages already
// within the bounds are left at their rendered size, pages are never scaled up,
// and 0 leaves that axis unbounded.
type Resize struct {
	MaxWidth  uint32 `json:"maxWidth,omitzero"`
	MaxHeight uint32 `json:"maxHeight,omitzero"`
}

// PageRange is an inclusive, 1-based range of document pages.
type PageRange struct {
	// FirstPage is the first page to render; 0 means the first page.
	FirstPage uint32 `json:"firstPage,omitzero"`
	// LastPage is the last page to render; 0 means the last page of the
	// document.
	LastPage uint32 `json:"lastPage,omitzero"`
}

// Result is what a worker reports back after rendering.
type Result struct {
	// Pages are the rendered pages in page-range order.
	Pages []Page `json:"pages"`
	// DocumentPages is the page count of the whole document, which lets
	// pkg/server tell a range that ran past the end from a short render (D-01).
	// 0 when the pipeline does not know it.
	DocumentPages int `json:"documentPages,omitzero"`
}

// Page is one rendered page left in the job directory.
type Page struct {
	// PageIndex is the 0-based position within the requested page range,
	// matching how pkg/output indexes pages.
	PageIndex int `json:"pageIndex"`
	// Path is the rendered file inside the job directory.
	Path string `json:"path"`
}
