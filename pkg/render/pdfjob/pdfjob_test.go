package pdfjob

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"

	// Decoders for the formats the pipeline encodes, so a test can read back
	// what it just rendered.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "github.com/gen2brain/avif"
	_ "github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"

	"github.com/ngicks/gahaku/pkg/worker"
)

// pdfPage is one page of a fixture document, sized in PDF points (1/72 inch).
type pdfPage struct {
	width  int
	height int
}

// fixture is the document most of these tests render. Its pages have distinct
// widths, and at 72 dpi a page comes out exactly as many pixels as it is
// points wide, so the width of a rendered image says which document page it
// holds — which is what lets a page-range test prove ordering and not just
// count.
var fixture = []pdfPage{{72, 72}, {144, 72}, {216, 72}}

// buildPDF assembles a complete PDF: catalog, page tree, one content stream
// per page and an xref table carrying real byte offsets. The fixture is
// generated rather than checked in so a test can pick the page sizes it needs
// to tell pages apart, and so there is no opaque binary in the tree.
func buildPDF(pages []pdfPage) []byte {
	n := len(pages)
	// Object 1 is the catalog, 2 the page tree, then one object per page
	// followed by one content stream per page.
	objects := make([]string, 0, 2+2*n)
	objects = append(objects, "<< /Type /Catalog /Pages 2 0 R >>")

	kids := &bytes.Buffer{}
	for i := range n {
		if i > 0 {
			kids.WriteByte(' ')
		}
		fmt.Fprintf(kids, "%d 0 R", 3+i)
	}
	objects = append(objects, fmt.Sprintf(
		"<< /Type /Pages /Kids [%s] /Count %d >>", kids, n))

	for i, page := range pages {
		objects = append(objects, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] "+
				"/Contents %d 0 R /Resources << >> >>",
			page.width, page.height, 3+n+i))
	}
	for _, page := range pages {
		// A filled rectangle over a quarter of the page: enough ink that a
		// render producing a blank bitmap would not look like a valid one.
		content := fmt.Sprintf("0 0 0 rg 0 0 %d %d re f\n", page.width/2, page.height/2)
		objects = append(objects, fmt.Sprintf(
			"<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}

	buf := &bytes.Buffer{}
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	startxref := buf.Len()
	fmt.Fprintf(buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, startxref)
	return buf.Bytes()
}

func writeFixture(t *testing.T, pages []pdfPage) string {
	t.Helper()
	return writeRawFixture(t, buildPDF(pages))
}

func writeRawFixture(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.pdf")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

type decodedPage struct {
	format string
	config image.Config
}

// decodePages checks the result shape every render shares — page indexes
// 0-based and consecutive within the requested range, files inside the job
// directory — and decodes each page back.
func decodePages(t *testing.T, result worker.Result, jobDir string) []decodedPage {
	t.Helper()
	decoded := make([]decodedPage, 0, len(result.Pages))
	for i, page := range result.Pages {
		if page.PageIndex != i {
			t.Errorf("Pages[%d].PageIndex = %d, want %d", i, page.PageIndex, i)
		}
		if filepath.Dir(page.Path) != jobDir {
			t.Errorf("Pages[%d].Path = %q, want a file inside the job dir %q",
				i, page.Path, jobDir)
		}

		f, err := os.Open(page.Path)
		if err != nil {
			t.Fatalf("Open rendered page %d: %v", i, err)
		}
		config, format, err := image.DecodeConfig(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("DecodeConfig(%s): %v", page.Path, err)
		}
		decoded = append(decoded, decodedPage{format: format, config: config})
	}
	return decoded
}

// assertPixels checks one axis of a page rendered at dpi against the size its
// points ask for, allowing the pixel of slack pdfium's ceil of a float64 scale
// costs: a 216 point page at 150 dpi comes out 451 pixels wide, not 450.
func assertPixels(t *testing.T, axis string, got, points, dpi int) {
	t.Helper()
	want := float64(points) * float64(dpi) / 72
	if float64(got) < want-1 || float64(got) > want+1 {
		t.Errorf("%s = %d px, want about %.0f px (%d points at %d dpi)",
			axis, got, want, points, dpi)
	}
}

func TestRenderRasterizesEveryPage(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeFixture(t, fixture),
		JobDir:    jobDir,
		Options:   worker.Options{Dpi: 72},
	}

	result, err := Render(t.Context(), spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.DocumentPages != len(fixture) {
		t.Errorf("DocumentPages = %d, want %d", result.DocumentPages, len(fixture))
	}

	pages := decodePages(t, result, jobDir)
	if len(pages) != len(fixture) {
		t.Fatalf("rendered %d pages, want the document's %d", len(pages), len(fixture))
	}
	for i, page := range pages {
		// The request named no format, so every page must be the default.
		if page.format != string(worker.FormatPng) {
			t.Errorf("page %d format = %q, want png", i, page.format)
		}
		assertPixels(t, fmt.Sprintf("page %d width", i), page.config.Width, fixture[i].width, 72)
		assertPixels(t, fmt.Sprintf("page %d height", i), page.config.Height, fixture[i].height, 72)
	}
}

// The page range selects document pages, while the result indexes them from 0
// within that range (worker.Page), and the document's own page count travels
// back whatever the range was.
func TestRenderPageRange(t *testing.T) {
	input := writeFixture(t, fixture)

	for _, tt := range []struct {
		name string
		// wantPages are the document pages, 1-based, the render must hold.
		wantPages []int
		pages     worker.PageRange
	}{
		{name: "the whole document", wantPages: []int{1, 2, 3}},
		{
			name:      "a subset",
			pages:     worker.PageRange{FirstPage: 2, LastPage: 3},
			wantPages: []int{2, 3},
		},
		{
			name:      "a single page",
			pages:     worker.PageRange{FirstPage: 2, LastPage: 2},
			wantPages: []int{2},
		},
		{
			name:      "open ended from page 2",
			pages:     worker.PageRange{FirstPage: 2},
			wantPages: []int{2, 3},
		},
		{
			name:      "open ended up to page 2",
			pages:     worker.PageRange{LastPage: 2},
			wantPages: []int{1, 2},
		},
		{
			name:      "a range running past the end is clamped",
			pages:     worker.PageRange{FirstPage: 2, LastPage: 9},
			wantPages: []int{2, 3},
		},
		{
			name:      "a range past the end from the first page",
			pages:     worker.PageRange{LastPage: 99},
			wantPages: []int{1, 2, 3},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Dpi: 72, Pages: tt.pages},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if result.DocumentPages != len(fixture) {
				t.Errorf("DocumentPages = %d, want the document's %d",
					result.DocumentPages, len(fixture))
			}

			pages := decodePages(t, result, jobDir)
			if len(pages) != len(tt.wantPages) {
				t.Fatalf("rendered %d pages, want %d", len(pages), len(tt.wantPages))
			}
			for i, documentPage := range tt.wantPages {
				assertPixels(t, fmt.Sprintf("page %d width", i),
					pages[i].config.Width, fixture[documentPage-1].width, 72)
			}
		})
	}
}

func TestRenderDpiScalesTheOutput(t *testing.T) {
	page := pdfPage{width: 144, height: 72}
	input := writeFixture(t, []pdfPage{page})

	for _, dpi := range []int{72, 150, 300} {
		t.Run(fmt.Sprintf("%d dpi", dpi), func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Dpi: uint32(dpi)},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			rendered := decodePages(t, result, jobDir)
			if len(rendered) != 1 {
				t.Fatalf("rendered %d pages, want 1", len(rendered))
			}
			assertPixels(t, "width", rendered[0].config.Width, page.width, dpi)
			assertPixels(t, "height", rendered[0].config.Height, page.height, dpi)
		})
	}
}

// A request naming no density has to land on defaultDpi, which is checked
// against a request that asks for that density outright rather than against
// the constant, so the test would notice a default that never reached pdfium.
func TestRenderDefaultsToOneHundredFiftyDpi(t *testing.T) {
	page := pdfPage{width: 144, height: 72}
	input := writeFixture(t, []pdfPage{page})

	sizes := map[string]image.Config{}
	for _, tt := range []struct {
		name    string
		options worker.Options
	}{
		{name: "no dpi requested"},
		{name: "150 dpi requested", options: worker.Options{Dpi: 150}},
	} {
		jobDir := t.TempDir()
		spec := worker.Spec{InputPath: input, JobDir: jobDir, Options: tt.options}

		result, err := Render(t.Context(), spec)
		if err != nil {
			t.Fatalf("Render(%s): %v", tt.name, err)
		}
		rendered := decodePages(t, result, jobDir)
		if len(rendered) != 1 {
			t.Fatalf("Render(%s) rendered %d pages, want 1", tt.name, len(rendered))
		}
		sizes[tt.name] = rendered[0].config
	}

	if sizes["no dpi requested"] != sizes["150 dpi requested"] {
		t.Errorf("a request without a dpi rendered %v, want the 150 dpi %v",
			sizes["no dpi requested"], sizes["150 dpi requested"])
	}
	assertPixels(t, "width", sizes["no dpi requested"].Width, page.width, 150)
}

func TestRenderResizes(t *testing.T) {
	// At 72 dpi this page is exactly 216x72 pixels before any resize.
	input := writeFixture(t, []pdfPage{{width: 216, height: 72}})

	for _, tt := range []struct {
		name         string
		resize       worker.Resize
		wantW, wantH int
	}{
		{
			name:  "no resize keeps the rendered size",
			wantW: 216, wantH: 72,
		},
		{
			name:   "bounded on width",
			resize: worker.Resize{MaxWidth: 108},
			wantW:  108, wantH: 36,
		},
		{
			name:   "bounded on height",
			resize: worker.Resize{MaxHeight: 36},
			wantW:  108, wantH: 36,
		},
		{
			name:   "the tighter bound wins and the aspect ratio holds",
			resize: worker.Resize{MaxWidth: 200, MaxHeight: 36},
			wantW:  108, wantH: 36,
		},
		{
			name:   "bounds larger than the page never scale it up",
			resize: worker.Resize{MaxWidth: 500, MaxHeight: 500},
			wantW:  216, wantH: 72,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Dpi: 72, Resize: tt.resize},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			rendered := decodePages(t, result, jobDir)
			if len(rendered) != 1 {
				t.Fatalf("rendered %d pages, want 1", len(rendered))
			}
			got := rendered[0].config
			if got.Width != tt.wantW || got.Height != tt.wantH {
				t.Errorf("size = %dx%d, want %dx%d", got.Width, got.Height, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestRenderConvertsToEverySupportedFormat(t *testing.T) {
	input := writeFixture(t, []pdfPage{{width: 144, height: 72}})

	for _, format := range []worker.Format{
		worker.FormatPng,
		worker.FormatJpeg,
		worker.FormatGif,
		worker.FormatTiff,
		worker.FormatBmp,
		worker.FormatWebp,
		worker.FormatAvif,
	} {
		t.Run(string(format), func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Dpi: 72, Format: format},
			}

			result, err := Render(t.Context(), spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			rendered := decodePages(t, result, jobDir)
			if len(rendered) != 1 {
				t.Fatalf("rendered %d pages, want 1", len(rendered))
			}
			if rendered[0].format != string(format) {
				t.Errorf("output format = %q, want %q", rendered[0].format, format)
			}
			if rendered[0].config.Width != 144 || rendered[0].config.Height != 72 {
				t.Errorf("size = %dx%d, want 144x72",
					rendered[0].config.Width, rendered[0].config.Height)
			}
		})
	}
}
