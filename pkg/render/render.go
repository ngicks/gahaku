// Package render holds what the rendering backends have in common: the
// resize arithmetic, the encoder table behind worker.Format, the naming of
// pages inside the job directory, and page-range resolution.
//
// The option types themselves live in pkg/worker, where the job protocol
// defines them. This package only interprets them, so imagejob, pdfjob and
// officejob agree on what a Resize, a Format or a PageRange means and a
// caller sees one behavior whichever pipeline ran.
//
// Nothing here knows about protobuf or gRPC; failures are typed errors that
// pkg/server maps to status codes.
package render

import (
	"bufio"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/tiff"

	"github.com/ngicks/gahaku/pkg/worker"
)

// encoder is one entry of the codec set (D-05): stdlib png/jpeg/gif,
// golang.org/x/image tiff/bmp, and the CGo-free gen2brain webp/avif
// encoders. Quality settings stay at each encoder's default because
// worker.Options exposes no knob for them.
type encoder struct {
	ext    string
	encode func(w io.Writer, m image.Image) error
}

var encoders = map[worker.Format]encoder{
	worker.FormatPng:  {ext: "png", encode: png.Encode},
	worker.FormatJpeg: {ext: "jpg", encode: encodeJpeg},
	worker.FormatGif:  {ext: "gif", encode: encodeGif},
	worker.FormatTiff: {ext: "tiff", encode: encodeTiff},
	worker.FormatBmp:  {ext: "bmp", encode: bmp.Encode},
	worker.FormatWebp: {ext: "webp", encode: encodeWebp},
	worker.FormatAvif: {ext: "avif", encode: encodeAvif},
}

func encodeJpeg(w io.Writer, m image.Image) error { return jpeg.Encode(w, m, nil) }
func encodeGif(w io.Writer, m image.Image) error  { return gif.Encode(w, m, nil) }
func encodeTiff(w io.Writer, m image.Image) error { return tiff.Encode(w, m, nil) }
func encodeWebp(w io.Writer, m image.Image) error { return webp.Encode(w, m) }
func encodeAvif(w io.Writer, m image.Image) error { return avif.Encode(w, m) }

// CanEncode reports whether f is a format WritePage can produce.
func CanEncode(f worker.Format) bool {
	_, ok := encoders[f]
	return ok
}

// ScaledSize returns the size an image of width x height takes once r is
// applied: the largest size fitting inside both bounds with the aspect ratio
// preserved. An image already within the bounds keeps its size, since
// rendering never scales up (worker.Resize).
func ScaledSize(width, height int, r worker.Resize) (int, int) {
	if width <= 0 || height <= 0 {
		return width, height
	}
	scale := 1.0
	if r.MaxWidth > 0 {
		scale = min(scale, float64(r.MaxWidth)/float64(width))
	}
	if r.MaxHeight > 0 {
		scale = min(scale, float64(r.MaxHeight)/float64(height))
	}
	if scale >= 1 {
		return width, height
	}
	return scaleAxis(width, scale), scaleAxis(height, scale)
}

// scaleAxis keeps a heavily scaled-down axis at one pixel rather than
// letting it round away to a zero-sized image.
func scaleAxis(length int, scale float64) int {
	return max(1, int(math.Round(float64(length)*scale)))
}

// Scale resizes img to fit r, resampling with Catmull-Rom: a render is a
// one-shot downscale whose output a human looks at, so the sharper kernel is
// worth the milliseconds a bilinear one would save.
//
// An image needing no scaling is returned as it is, which also lets a
// paletted or grayscale source reach the encoders in its own type.
func Scale(img image.Image, r worker.Resize) image.Image {
	bounds := img.Bounds()
	width, height := ScaledSize(bounds.Dx(), bounds.Dy(), r)
	if width == bounds.Dx() && height == bounds.Dy() {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Src, nil)
	return dst
}

// WritePage encodes img into dir as the pageIndex-th page of a render and
// reports it as a worker.Page. The name carries the index so the job
// directory lists in page order, and the returned path is the full one the
// orchestrator reads the page back from.
func WritePage(dir string, pageIndex int, img image.Image, f worker.Format) (worker.Page, error) {
	enc, ok := encoders[f]
	if !ok {
		return worker.Page{}, &UnsupportedFormatError{Format: f}
	}
	path := filepath.Join(dir, fmt.Sprintf("page-%04d.%s", pageIndex, enc.ext))
	file, err := os.Create(path)
	if err != nil {
		return worker.Page{}, fmt.Errorf("render: creating page file: %w", err)
	}
	// The tiff and bmp encoders write field by field, so the buffer is what
	// keeps a page from turning into thousands of syscalls.
	buffered := bufio.NewWriter(file)
	if err := enc.encode(buffered, img); err != nil {
		_ = file.Close()
		return worker.Page{}, fmt.Errorf("render: encoding page as %s: %w", f, err)
	}
	if err := buffered.Flush(); err != nil {
		_ = file.Close()
		return worker.Page{}, fmt.Errorf("render: writing page file: %w", err)
	}
	if err := file.Close(); err != nil {
		return worker.Page{}, fmt.Errorf("render: writing page file: %w", err)
	}
	return worker.Page{PageIndex: pageIndex, Path: path}, nil
}
