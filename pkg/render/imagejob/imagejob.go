// Package imagejob renders an image document into an image: decode,
// optionally scale down, encode to the requested format.
//
// An image is a one-page document, so the requested page range only ever
// selects page 1 and one that starts later is refused. The result still
// carries the page count, which is how pkg/server tells that refusal apart
// from a document that simply ran short (D-01).
package imagejob

import (
	"context"
	"fmt"
	"image"
	"os"

	// Decoders for the input formats of D-05. The webp decoder comes from
	// gen2brain rather than golang.org/x/image/webp: pkg/render already
	// imports that package for its encoder, and registering a second "webp"
	// decoder would only add a registry entry that loses on init order.
	// gen2brain/avif likewise brings avif decoding along with its encoder.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "github.com/gen2brain/avif"
	_ "github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/worker"
)

// documentPages is what an image always has.
const documentPages = 1

var _ worker.HandlerFunc = Render

// Render decodes spec.InputPath, scales it to fit spec.Options.Resize and
// writes it back into spec.JobDir in the requested format, defaulting to the
// format it came in as. It is the entry point `gahaku worker image` runs.
//
// spec.Options.Dpi is ignored: an image arrives already rasterized, so there
// is no density left to choose (worker.Options).
func Render(ctx context.Context, spec worker.Spec) (worker.Result, error) {
	// Validated before the input is touched so an unrenderable request costs
	// nothing to reject.
	if f := spec.Options.Format; f != "" && !render.CanEncode(f) {
		return worker.Result{}, &render.UnsupportedFormatError{Format: f}
	}
	if _, _, err := render.NormalizePageRange(spec.Options.Pages, documentPages); err != nil {
		return worker.Result{}, err
	}

	img, sourceFormat, err := decode(spec.InputPath)
	if err != nil {
		return worker.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return worker.Result{}, err
	}

	format := spec.Options.Format
	if format == "" {
		format = sourceFormat
	}

	scaled := render.Scale(img, spec.Options.Resize)
	if err := ctx.Err(); err != nil {
		return worker.Result{}, err
	}

	page, err := render.WritePage(spec.JobDir, 0, scaled, format)
	if err != nil {
		return worker.Result{}, err
	}
	return worker.Result{Pages: []worker.Page{page}, DocumentPages: documentPages}, nil
}

// decode reads the input document and reports the format it was stored in,
// which names the encoder a request without an explicit format gets.
func decode(path string) (image.Image, worker.Format, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("imagejob: opening input document: %w", err)
	}
	defer func() { _ = file.Close() }()

	img, name, err := image.Decode(file)
	if err != nil {
		return nil, "", &render.DecodeError{Err: err}
	}
	return img, worker.Format(name), nil
}
