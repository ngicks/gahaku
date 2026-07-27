package e2e

import (
	"os/exec"
	"strconv"
	"testing"

	"google.golang.org/grpc/codes"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
)

// sofficeBinary is the converter the office pipeline looks up on PATH, which
// the server inherits from the test process.
const sofficeBinary = "soffice"

func requireSoffice(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath(sofficeBinary)
	if err != nil {
		t.Skipf("%s is not installed: %v", sofficeBinary, err)
	}
	t.Logf("converting with %s", path)
}

func TestRenderImageResizesAndReencodes(t *testing.T) {
	client := startServer(t).client(t)

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{
		Format: gahakuv1.ImageFormat_IMAGE_FORMAT_JPEG,
		Resize: &gahakuv1.ResizeSpec{MaxWidth: 100, MaxHeight: 100},
	}
	responses, err := exchange(t, client.RenderImage,
		streamRequests(header, buildPNG(t, 400, 200))...)
	if err != nil {
		t.Fatalf("RenderImage: %v", err)
	}

	pages := decodePages(t, pageBytes(t, responses))
	if len(pages) != 1 {
		t.Fatalf("rendered %d pages, want the 1 an image is", len(pages))
	}
	if pages[0].format != "jpeg" {
		t.Errorf("format = %q, want the requested jpeg", pages[0].format)
	}
	// The width bound is the tighter one, and the aspect ratio holds.
	if got := pages[0].config; got.Width != 100 || got.Height != 50 {
		t.Errorf("size = %dx%d, want 100x50", got.Width, got.Height)
	}
}

func TestRenderPdfRendersThePageRangeAtTheRequestedDpi(t *testing.T) {
	client := startServer(t).client(t)

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{
		Dpi:   144,
		Pages: &gahakuv1.PageRange{FirstPage: 2, LastPage: 3},
	}
	responses, err := exchange(t, client.RenderPdf,
		streamRequests(header, buildPDF(pdfFixture))...)
	if err != nil {
		t.Fatalf("RenderPdf: %v", err)
	}

	pages := decodePages(t, pageBytes(t, responses))
	if len(pages) != 2 {
		t.Fatalf("rendered %d pages, want the 2 the range selected", len(pages))
	}
	// Pages 2 and 3 of the fixture, in that order, at twice their point size.
	for i, source := range pdfFixture[1:] {
		if pages[i].format != "png" {
			t.Errorf("page %d format = %q, want the default png", i, pages[i].format)
		}
		assertPixels(t, "page width", pages[i].config.Width, source.width, 144)
		assertPixels(t, "page height", pages[i].config.Height, source.height, 144)
	}
}

func TestRenderOfficeConvertsAndRasterizes(t *testing.T) {
	requireSoffice(t)
	client := startServer(t).client(t)

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{Dpi: 72}
	responses, err := exchange(t, client.RenderOffice,
		streamRequests(header, buildDocx(t))...)
	if err != nil {
		t.Fatalf("RenderOffice: %v", err)
	}

	pages := decodePages(t, pageBytes(t, responses))
	if len(pages) != 1 {
		t.Fatalf("rendered %d pages, want the document's 1", len(pages))
	}
	if pages[0].format != "png" {
		t.Errorf("format = %q, want the default png", pages[0].format)
	}
	// The page size is LibreOffice's default rather than this test's to pick,
	// so the assert only holds it to a whole page.
	if got := pages[0].config; got.Width < 100 || got.Height < 100 {
		t.Errorf("size = %dx%d, want a whole page", got.Width, got.Height)
	}
}

// The untyped RPC over one document of each kind: nothing but the bytes says
// which pipeline should run, and what comes back says which one did — an image
// keeps its own size, the PDF fixture comes back as its three pages, and a docx
// only renders at all if it reached LibreOffice.
func TestRenderSniffsEachDocumentKind(t *testing.T) {
	client := startServer(t).client(t)

	t.Run("an image", func(t *testing.T) {
		header := byteStreamHeader()
		responses, err := exchange(t, client.Render,
			streamRequests(header, buildPNG(t, 320, 240))...)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		pages := decodePages(t, pageBytes(t, responses))
		if len(pages) != 1 {
			t.Fatalf("rendered %d pages, want the 1 an image is", len(pages))
		}
		if got := pages[0].config; got.Width != 320 || got.Height != 240 {
			t.Errorf("size = %dx%d, want the source's 320x240", got.Width, got.Height)
		}
		if pages[0].format != "png" {
			t.Errorf("format = %q, want the source's png", pages[0].format)
		}
	})

	t.Run("a pdf", func(t *testing.T) {
		header := byteStreamHeader()
		header.Options = &gahakuv1.RenderOptions{Dpi: 72}
		responses, err := exchange(t, client.Render,
			streamRequests(header, buildPDF(pdfFixture))...)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		pages := decodePages(t, pageBytes(t, responses))
		if len(pages) != len(pdfFixture) {
			t.Fatalf("rendered %d pages, want the document's %d", len(pages), len(pdfFixture))
		}
		for i, source := range pdfFixture {
			assertPixels(t, "page width", pages[i].config.Width, source.width, 72)
		}
	})

	t.Run("an office document", func(t *testing.T) {
		requireSoffice(t)

		header := byteStreamHeader()
		header.Options = &gahakuv1.RenderOptions{Dpi: 72}
		responses, err := exchange(t, client.Render,
			streamRequests(header, buildDocx(t))...)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		pages := decodePages(t, pageBytes(t, responses))
		if len(pages) != 1 {
			t.Fatalf("rendered %d pages, want the document's 1", len(pages))
		}
	})

	t.Run("bytes of no kind at all", func(t *testing.T) {
		header := byteStreamHeader()
		_, err := exchange(t, client.Render,
			streamRequests(header, []byte("\x01\x02\x03\x04gahaku not a document\x05\x06"))...)

		requireCode(t, err, codes.InvalidArgument)
	})
}

// The limit is enforced as the chunks arrive, so the stream is refused before
// anything is rendered — which is what the empty response and the received
// count, larger than the whole limit but smaller than the document, say.
func TestRenderRejectsInputOverTheStreamLimit(t *testing.T) {
	const limit = 4 << 10
	client := startServer(t, "--max-stream-bytes", strconv.Itoa(limit)).client(t)

	document := buildPNG(t, 400, 400)
	if len(document) <= limit {
		t.Fatalf("the fixture is %d bytes, want more than the %d byte limit", len(document), limit)
	}
	responses, err := exchange(t, client.RenderImage,
		streamRequests(byteStreamHeader(), document)...)

	requireCode(t, err, codes.ResourceExhausted)
	detail := detailOf[*gahakuv1.InputSizeLimitExceeded](t, err)
	if detail.GetLimitBytes() != limit {
		t.Errorf("want the %d byte limit reported, got %d", limit, detail.GetLimitBytes())
	}
	if got := detail.GetReceivedBytes(); got <= limit || got > uint64(len(document)) {
		t.Errorf("received = %d, want past the limit and short of the whole %d byte document",
			got, len(document))
	}
	if len(responses) != 0 {
		t.Errorf("want nothing rendered, got %d response frames", len(responses))
	}
}

// A deadline the render cannot meet: the orchestrator kills the worker's
// process group and the caller is told so, rather than waiting on a job nobody
// is coming back from. The follow-up request is what says the server itself
// survived the kill.
func TestRenderKillsAJobThatOutlivesItsTimeout(t *testing.T) {
	client := startServer(t, "--job-timeout", "100ms").client(t)

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{Dpi: 300}
	_, err := exchange(t, client.RenderPdf, streamRequests(header, buildPDF(pdfFixture))...)

	requireCode(t, err, codes.DeadlineExceeded)

	rejected := byteStreamHeader()
	rejected.Options = &gahakuv1.RenderOptions{
		Pages: &gahakuv1.PageRange{FirstPage: 5, LastPage: 2},
	}
	_, err = exchange(t, client.RenderPdf, headerRequest(rejected))

	requireCode(t, err, codes.InvalidArgument)
}
