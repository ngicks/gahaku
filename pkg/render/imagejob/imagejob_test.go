package imagejob

import (
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"

	"github.com/ngicks/gahaku/pkg/worker"
)

// supportedFormats is the codec set of D-05, which is both what the pipeline
// decodes and what it encodes.
var supportedFormats = []worker.Format{
	worker.FormatPng,
	worker.FormatJpeg,
	worker.FormatGif,
	worker.FormatTiff,
	worker.FormatBmp,
	worker.FormatWebp,
	worker.FormatAvif,
}

// gradient has content in both axes so a resample that swapped them or lost
// the aspect ratio could not pass unnoticed.
func gradient(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 255 / max(width-1, 1)),
				G: uint8(y * 255 / max(height-1, 1)),
				B: 0x80,
				A: 0xff,
			})
		}
	}
	return img
}

// writeFixture encodes an input document without going through pkg/render, so
// a bug in the shared encoder table cannot cancel itself out here.
func writeFixture(t *testing.T, format worker.Format, img image.Image) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = f.Close() }()

	switch format {
	case worker.FormatPng:
		err = png.Encode(f, img)
	case worker.FormatJpeg:
		err = jpeg.Encode(f, img, nil)
	case worker.FormatGif:
		err = gif.Encode(f, img, nil)
	case worker.FormatTiff:
		err = tiff.Encode(f, img, nil)
	case worker.FormatBmp:
		err = bmp.Encode(f, img)
	case worker.FormatWebp:
		err = webp.Encode(f, img)
	case worker.FormatAvif:
		err = avif.Encode(f, img)
	default:
		t.Fatalf("no fixture encoder for %q", format)
	}
	if err != nil {
		t.Fatalf("encoding a %s fixture: %v", format, err)
	}
	return path
}

// renderedPage checks the one-page result shape every image render has and
// returns the page's decoded format and size.
func renderedPage(t *testing.T, result worker.Result, jobDir string) (string, image.Config) {
	t.Helper()
	if result.DocumentPages != 1 {
		t.Errorf("DocumentPages = %d, want 1 for an image", result.DocumentPages)
	}
	if len(result.Pages) != 1 {
		t.Fatalf("Pages = %v, want exactly one page", result.Pages)
	}
	page := result.Pages[0]
	if page.PageIndex != 0 {
		t.Errorf("PageIndex = %d, want 0", page.PageIndex)
	}
	if filepath.Dir(page.Path) != jobDir {
		t.Errorf("Path = %q, want a file inside the job dir %q", page.Path, jobDir)
	}

	f, err := os.Open(page.Path)
	if err != nil {
		t.Fatalf("Open rendered page: %v", err)
	}
	defer func() { _ = f.Close() }()
	cfg, name, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("DecodeConfig(%s): %v", page.Path, err)
	}
	return name, cfg
}

func TestRenderDecodesEverySupportedInputFormat(t *testing.T) {
	for _, format := range supportedFormats {
		t.Run(string(format), func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: writeFixture(t, format, gradient(40, 20)),
				JobDir:    jobDir,
				Options:   worker.Options{Format: worker.FormatPng},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			name, cfg := renderedPage(t, result, jobDir)
			if name != string(worker.FormatPng) {
				t.Errorf("output format = %q, want png", name)
			}
			if cfg.Width != 40 || cfg.Height != 20 {
				t.Errorf("size = %dx%d, want the input's 40x20", cfg.Width, cfg.Height)
			}
		})
	}
}

func TestRenderConvertsToEverySupportedFormat(t *testing.T) {
	input := writeFixture(t, worker.FormatPng, gradient(40, 20))

	for _, format := range supportedFormats {
		t.Run(string(format), func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Format: format},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			name, cfg := renderedPage(t, result, jobDir)
			if name != string(format) {
				t.Errorf("output format = %q, want %q", name, format)
			}
			if cfg.Width != 40 || cfg.Height != 20 {
				t.Errorf("size = %dx%d, want the input's 40x20", cfg.Width, cfg.Height)
			}
		})
	}
}

// A request without a format gets the one the document came in as, so a
// resize-only job does not silently change the caller's codec.
func TestRenderKeepsTheInputFormatByDefault(t *testing.T) {
	for _, format := range supportedFormats {
		t.Run(string(format), func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: writeFixture(t, format, gradient(40, 20)),
				JobDir:    jobDir,
				Options:   worker.Options{Resize: worker.Resize{MaxWidth: 20}},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			name, cfg := renderedPage(t, result, jobDir)
			if name != string(format) {
				t.Errorf("output format = %q, want the input's %q", name, format)
			}
			if cfg.Width != 20 || cfg.Height != 10 {
				t.Errorf("size = %dx%d, want 20x10", cfg.Width, cfg.Height)
			}
		})
	}
}

func TestRenderResizes(t *testing.T) {
	input := writeFixture(t, worker.FormatPng, gradient(100, 50))

	for _, tt := range []struct {
		name         string
		resize       worker.Resize
		wantW, wantH int
	}{
		{
			name:  "no resize keeps the input size",
			wantW: 100, wantH: 50,
		},
		{
			name:   "bounded on width",
			resize: worker.Resize{MaxWidth: 50},
			wantW:  50, wantH: 25,
		},
		{
			name:   "bounded on height",
			resize: worker.Resize{MaxHeight: 10},
			wantW:  20, wantH: 10,
		},
		{
			name:   "the tighter bound wins and the aspect ratio holds",
			resize: worker.Resize{MaxWidth: 60, MaxHeight: 20},
			wantW:  40, wantH: 20,
		},
		{
			name:   "bounds larger than the image never scale it up",
			resize: worker.Resize{MaxWidth: 400, MaxHeight: 400},
			wantW:  100, wantH: 50,
		},
		{
			name:   "one large bound does not scale up either",
			resize: worker.Resize{MaxWidth: 400},
			wantW:  100, wantH: 50,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Resize: tt.resize},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			_, cfg := renderedPage(t, result, jobDir)
			if cfg.Width != tt.wantW || cfg.Height != tt.wantH {
				t.Errorf("size = %dx%d, want %dx%d", cfg.Width, cfg.Height, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestRenderAcceptsPageRangesCoveringPageOne(t *testing.T) {
	input := writeFixture(t, worker.FormatPng, gradient(8, 8))

	for _, tt := range []struct {
		name  string
		pages worker.PageRange
	}{
		{name: "the whole document"},
		{name: "page 1 stated explicitly", pages: worker.PageRange{FirstPage: 1, LastPage: 1}},
		{name: "open ended from page 1", pages: worker.PageRange{FirstPage: 1}},
		{name: "open ended up to page 1", pages: worker.PageRange{LastPage: 1}},
		{
			name:  "a range running past the only page",
			pages: worker.PageRange{FirstPage: 1, LastPage: 5},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Pages: tt.pages},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			renderedPage(t, result, jobDir)
		})
	}
}

// Dpi describes a rasterization density an image no longer has, so carrying
// one must not turn into a rejection.
func TestRenderIgnoresDpi(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeFixture(t, worker.FormatPng, gradient(40, 20)),
		JobDir:    jobDir,
		Options:   worker.Options{Dpi: 300},
	}

	result, err := Render(t.Context(), spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	_, cfg := renderedPage(t, result, jobDir)
	if cfg.Width != 40 || cfg.Height != 20 {
		t.Errorf("size = %dx%d, want the input's 40x20", cfg.Width, cfg.Height)
	}
}
