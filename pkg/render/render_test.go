package render

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/ngicks/gahaku/pkg/worker"
)

// gradient is a fixture with content in both axes, so a resample that got the
// axes or the aspect ratio wrong would not be hidden by a flat color.
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

func TestScaledSize(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		resize        worker.Resize
		wantW, wantH  int
	}{
		{
			name:  "no bounds leaves the size alone",
			width: 100, height: 50,
			wantW: 100, wantH: 50,
		},
		{
			name:  "bounded on width",
			width: 100, height: 50,
			resize: worker.Resize{MaxWidth: 50},
			wantW:  50, wantH: 25,
		},
		{
			name:  "bounded on height",
			width: 100, height: 50,
			resize: worker.Resize{MaxHeight: 10},
			wantW:  20, wantH: 10,
		},
		{
			name:  "both bounds, height binds",
			width: 100, height: 50,
			resize: worker.Resize{MaxWidth: 60, MaxHeight: 20},
			wantW:  40, wantH: 20,
		},
		{
			name:  "both bounds, width binds",
			width: 100, height: 50,
			resize: worker.Resize{MaxWidth: 20, MaxHeight: 40},
			wantW:  20, wantH: 10,
		},
		{
			name:  "already inside the bounds is not scaled up",
			width: 100, height: 50,
			resize: worker.Resize{MaxWidth: 400, MaxHeight: 400},
			wantW:  100, wantH: 50,
		},
		{
			name:  "one unbounded axis does not scale up the other",
			width: 100, height: 50,
			resize: worker.Resize{MaxWidth: 400},
			wantW:  100, wantH: 50,
		},
		{
			name:  "exactly at the bounds",
			width: 100, height: 50,
			resize: worker.Resize{MaxWidth: 100, MaxHeight: 50},
			wantW:  100, wantH: 50,
		},
		{
			name:  "a squashed axis keeps its last pixel",
			width: 1000, height: 10,
			resize: worker.Resize{MaxWidth: 5},
			wantW:  5, wantH: 1,
		},
		{
			name:  "empty image",
			width: 0, height: 0,
			resize: worker.Resize{MaxWidth: 10},
			wantW:  0, wantH: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotW, gotH := ScaledSize(tt.width, tt.height, tt.resize)
			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("ScaledSize = %dx%d, want %dx%d", gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestScaleDownscales(t *testing.T) {
	src := gradient(100, 50)

	got := Scale(src, worker.Resize{MaxWidth: 60, MaxHeight: 20})

	if got.Bounds() != image.Rect(0, 0, 40, 20) {
		t.Fatalf("bounds = %v, want 40x20 anchored at the origin", got.Bounds())
	}
}

// A source that needs no scaling is handed on untouched, which keeps a
// paletted or grayscale image in its own type for the encoders that have a
// dedicated path for it.
func TestScaleKeepsAnUnscaledSource(t *testing.T) {
	src := gradient(100, 50)

	for _, tt := range []struct {
		name   string
		resize worker.Resize
	}{
		{name: "no bounds", resize: worker.Resize{}},
		{name: "bounds larger than the image", resize: worker.Resize{MaxWidth: 400}},
		{name: "bounds equal to the image", resize: worker.Resize{MaxWidth: 100, MaxHeight: 50}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Scale(src, tt.resize); got != image.Image(src) {
				t.Errorf("Scale returned a copy (%T), want the source image", got)
			}
		})
	}
}

func TestWritePageEncodesEveryFormat(t *testing.T) {
	src := gradient(40, 20)

	for _, tt := range []struct {
		format  worker.Format
		wantExt string
	}{
		{format: worker.FormatPng, wantExt: "png"},
		{format: worker.FormatJpeg, wantExt: "jpg"},
		{format: worker.FormatGif, wantExt: "gif"},
		{format: worker.FormatTiff, wantExt: "tiff"},
		{format: worker.FormatBmp, wantExt: "bmp"},
		{format: worker.FormatWebp, wantExt: "webp"},
		{format: worker.FormatAvif, wantExt: "avif"},
	} {
		t.Run(string(tt.format), func(t *testing.T) {
			dir := t.TempDir()

			page, err := WritePage(dir, 2, src, tt.format)
			if err != nil {
				t.Fatalf("WritePage(%s): %v", tt.format, err)
			}
			if page.PageIndex != 2 {
				t.Errorf("PageIndex = %d, want 2", page.PageIndex)
			}
			want := filepath.Join(dir, "page-0002."+tt.wantExt)
			if page.Path != want {
				t.Errorf("Path = %q, want %q", page.Path, want)
			}

			cfg, name := decodeConfig(t, page.Path)
			if name != string(tt.format) {
				t.Errorf("encoded as %q, want %q", name, tt.format)
			}
			if cfg.Width != 40 || cfg.Height != 20 {
				t.Errorf("size = %dx%d, want 40x20", cfg.Width, cfg.Height)
			}
		})
	}
}

// A hard downscale can leave an axis a single pixel long, which is where an
// encoder's chroma subsampling is likeliest to balk.
func TestWritePageEncodesADegenerateSize(t *testing.T) {
	src := Scale(gradient(1000, 10), worker.Resize{MaxWidth: 5})

	for format := range encoders {
		t.Run(string(format), func(t *testing.T) {
			page, err := WritePage(t.TempDir(), 0, src, format)
			if err != nil {
				t.Fatalf("WritePage(%s): %v", format, err)
			}
			cfg, name := decodeConfig(t, page.Path)
			if name != string(format) {
				t.Errorf("encoded as %q, want %q", name, format)
			}
			if cfg.Width != 5 || cfg.Height != 1 {
				t.Errorf("size = %dx%d, want 5x1", cfg.Width, cfg.Height)
			}
		})
	}
}

func TestWritePageRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()

	_, err := WritePage(dir, 0, gradient(4, 4), worker.Format("psd"))
	if _, ok := errors.AsType[*UnsupportedFormatError](err); !ok {
		t.Fatalf("err = %v, want *UnsupportedFormatError", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("job dir holds %d files, want a rejected format to write none", len(entries))
	}
}

func TestWritePageMissingDirectory(t *testing.T) {
	_, err := WritePage(filepath.Join(t.TempDir(), "gone"), 0, gradient(4, 4), worker.FormatPng)
	if err == nil {
		t.Fatal("WritePage succeeded, want an error")
	}
}

func decodeConfig(t *testing.T, path string) (image.Config, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	cfg, name, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("DecodeConfig(%s): %v", path, err)
	}
	return cfg, name
}
