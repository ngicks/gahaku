// Package sniff decides which render pipeline an input document belongs to by
// looking at its magic bytes, which is what the untyped Render RPC needs and
// the typed RPCs skip because their caller has already asserted the kind.
//
// Detection reads a bounded prefix of the document
// (github.com/gabriel-vasile/mimetype), so a URI-delivered input is placed
// before the whole of it has been committed to a pipeline.
//
// Nothing here knows about protobuf or gRPC; failures are typed errors that
// pkg/server maps to status codes.
package sniff

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"os"

	"github.com/gabriel-vasile/mimetype"

	"github.com/ngicks/gahaku/pkg/worker"
)

// PrefixSize is the most leading bytes of a document detection can use. It is
// mimetype's own read limit, which Detect truncates its input to, so a longer
// prefix buys nothing; going past it would mean mimetype.SetLimit, a
// package-level global this package leaves alone so importing it cannot change
// how anything else in the process detects.
//
// A format whose marker sits past this point degrades to its container type —
// an OOXML document whose payload entries start late reads as application/zip
// — and is refused as unsupported rather than misrouted.
const PrefixSize = 4096

// kinds routes a detected media type to the pipeline that renders it.
//
// It is an allowlist rather than a family match: an image/* no decoder in this
// build reads, or a zip that merely looks office-adjacent, is refused for the
// cost of a map lookup instead of after a worker subprocess has spawned and
// failed. That also keeps image/svg+xml — a format whose "decoding" would
// fetch and execute — from ever reaching a pipeline by resembling one.
var kinds = map[string]worker.Kind{
	// The formats pkg/render/imagejob registers decoders for (D-05). avif is
	// among them because the gen2brain encoder imagejob imports brings its
	// decoder along (pkg/render/imagejob/imagejob.go:25).
	"image/png":  worker.KindImage,
	"image/jpeg": worker.KindImage,
	"image/gif":  worker.KindImage,
	"image/tiff": worker.KindImage,
	"image/bmp":  worker.KindImage,
	"image/webp": worker.KindImage,
	"image/avif": worker.KindImage,

	"application/pdf": worker.KindPdf,

	// OOXML and the legacy CFB formats behind it. application/x-ole-storage,
	// the compound-file type the legacy three sit under, is deliberately
	// absent: detection reaches it only when walking the container's directory
	// turned up no stream naming a format, so there is nothing to route on and
	// the file is as likely to be some other compound document as an Office
	// one.
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   worker.KindOffice,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         worker.KindOffice,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": worker.KindOffice,
	"application/msword":            worker.KindOffice,
	"application/vnd.ms-excel":      worker.KindOffice,
	"application/vnd.ms-powerpoint": worker.KindOffice,

	// OpenDocument, which PLAN.md left with a question mark: soffice converts
	// it natively, so the whole cost of supporting it is these three entries.
	// The template, drawing, chart and formula variants soffice also reads are
	// left out until something asks for them.
	"application/vnd.oasis.opendocument.text":         worker.KindOffice,
	"application/vnd.oasis.opendocument.spreadsheet":  worker.KindOffice,
	"application/vnd.oasis.opendocument.presentation": worker.KindOffice,
}

// Detect reports the pipeline that renders the document prefix begins.
//
// prefix is the leading bytes of the document; anything past PrefixSize of
// them is ignored, and a whole document short enough to hold in memory works
// as well as a prefix of one. A document no pipeline here renders fails with
// *UnsupportedMediaTypeError.
func Detect(prefix []byte) (worker.Kind, error) {
	mediaType := baseMediaType(mimetype.Detect(prefix).String())
	kind, ok := kinds[mediaType]
	if !ok {
		return "", &UnsupportedMediaTypeError{MediaType: mediaType}
	}
	return kind, nil
}

// DetectFile reports the pipeline that renders the document at path, reading
// no more of it than PrefixSize. A document shorter than that is detected from
// whatever it holds.
func DetectFile(path string) (worker.Kind, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("sniff: opening document: %w", err)
	}
	defer func() { _ = file.Close() }()

	prefix := make([]byte, PrefixSize)
	n, err := io.ReadFull(file, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("sniff: reading document: %w", err)
	}
	return Detect(prefix[:n])
}

// baseMediaType drops the parameters detection attaches to text types
// ("text/plain; charset=utf-8"), which the routing table is keyed without.
// A media type that will not parse is passed through to be reported as it was
// detected.
func baseMediaType(detected string) string {
	base, _, err := mime.ParseMediaType(detected)
	if err != nil {
		return detected
	}
	return base
}
