package e2e

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// The fixtures are generated rather than checked in, so a test can pick the
// sizes that tell one page from another and the tree holds no opaque binaries.
// They are copies of the builders pkg/render's tests use — a _test.go file is
// not importable — kept here so this suite depends on nothing but the binary
// and the generated client.

// pdfPage is one page of a fixture document, sized in PDF points (1/72 inch).
type pdfPage struct {
	width  int
	height int
}

// pdfFixture has pages of distinct widths: at 72 dpi a page comes out exactly
// as many pixels as it is points wide, so the width of a rendered image says
// which document page it holds, which is what lets a page-range assertion prove
// ordering and not just count.
var pdfFixture = []pdfPage{{72, 72}, {144, 72}, {216, 72}}

// buildPDF assembles a complete PDF: catalog, page tree, one content stream per
// page and an xref table carrying real byte offsets.
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

// buildPNG draws a checkerboard of 8 pixel squares. The pattern is there to
// cost the encoder something: a flat image would compress to a few hundred
// bytes, and the input-limit test needs a document whose size it can predict
// from its dimensions.
func buildPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			shade := uint8(255)
			if (x/8+y/8)%2 == 0 {
				shade = uint8((x*7 + y*13) % 256)
			}
			img.Set(x, y, color.RGBA{R: shade, G: uint8(x % 256), B: uint8(y % 256), A: 255})
		}
	}

	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encoding the png fixture: %v", err)
	}
	return buf.Bytes()
}

// buildDocx assembles the smallest OOXML document LibreOffice will open: the
// content types part, the package relationship pointing at the document, and a
// one-paragraph document body.
func buildDocx(t *testing.T) []byte {
	t.Helper()
	parts := []struct{ name, body string }{
		{
			name: "[Content_Types].xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
				`<Default Extension="rels" ContentType=` +
				`"application/vnd.openxmlformats-package.relationships+xml"/>` +
				`<Default Extension="xml" ContentType="application/xml"/>` +
				`<Override PartName="/word/document.xml" ContentType=` +
				`"application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
				`</Types>`,
		},
		{
			name: "_rels/.rels",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Relationships xmlns=` +
				`"http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type=` +
				`"http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"` +
				` Target="word/document.xml"/></Relationships>`,
		},
		{
			name: "word/document.xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<w:document xmlns:w=` +
				`"http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
				`<w:body><w:p><w:r><w:t>gahaku</w:t></w:r></w:p></w:body></w:document>`,
		},
	}

	buf := &bytes.Buffer{}
	archive := zip.NewWriter(buf)
	for _, part := range parts {
		w, err := archive.Create(part.name)
		if err != nil {
			t.Fatalf("zip Create(%s): %v", part.name, err)
		}
		if _, err := w.Write([]byte(part.body)); err != nil {
			t.Fatalf("zip Write(%s): %v", part.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}
