package sniff

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ngicks/gahaku/pkg/worker"
)

// Every supported format is checked through both entry points, so the file
// path never drifts from the prefix it is supposed to be reading.
func TestDetect(t *testing.T) {
	for _, tt := range []struct {
		name     string
		document []byte
		want     worker.Kind
	}{
		{name: "png", document: []byte(pngHeader), want: worker.KindImage},
		{name: "jpeg", document: []byte(jpegHeader), want: worker.KindImage},
		{name: "gif", document: []byte(gifHeader), want: worker.KindImage},
		{name: "little endian tiff", document: []byte(tiffLeHeader), want: worker.KindImage},
		{name: "big endian tiff", document: []byte(tiffBeHeader), want: worker.KindImage},
		{name: "bmp", document: bmpHeader(), want: worker.KindImage},
		{name: "webp", document: []byte(webpHeader), want: worker.KindImage},
		{name: "avif", document: []byte(avifHeader), want: worker.KindImage},

		{name: "pdf", document: []byte(pdfHeader), want: worker.KindPdf},

		{name: "docx", document: ooxmlFixture(t, "word", 0), want: worker.KindOffice},
		{name: "xlsx", document: ooxmlFixture(t, "xl", 0), want: worker.KindOffice},
		{name: "pptx", document: ooxmlFixture(t, "ppt", 0), want: worker.KindOffice},

		{name: "doc", document: cfbFixture("WordDocument"), want: worker.KindOffice},
		{name: "xls", document: cfbFixture("Workbook"), want: worker.KindOffice},
		{name: "ppt", document: cfbFixture("PowerPoint Document"), want: worker.KindOffice},

		{
			name:     "odt",
			document: odfFixture(t, "application/vnd.oasis.opendocument.text"),
			want:     worker.KindOffice,
		},
		{
			name:     "ods",
			document: odfFixture(t, "application/vnd.oasis.opendocument.spreadsheet"),
			want:     worker.KindOffice,
		},
		{
			name:     "odp",
			document: odfFixture(t, "application/vnd.oasis.opendocument.presentation"),
			want:     worker.KindOffice,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kind, err := Detect(tt.document)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if kind != tt.want {
				t.Errorf("Detect = %q, want %q", kind, tt.want)
			}

			kind, err = DetectFile(writeDocument(t, tt.document))
			if err != nil {
				t.Fatalf("DetectFile: %v", err)
			}
			if kind != tt.want {
				t.Errorf("DetectFile = %q, want %q", kind, tt.want)
			}
		})
	}
}

// The formats below are ones detection recognizes but nothing here renders,
// plus input it cannot place at all. Each must come back naming what it saw,
// which is what pkg/server puts in the status detail.
func TestDetectRejects(t *testing.T) {
	for _, tt := range []struct {
		name     string
		document []byte
		want     string
	}{
		{name: "plain text", document: []byte("just some words\n"), want: "text/plain"},
		{name: "no input at all", document: nil, want: "text/plain"},
		{
			name:     "bytes of no recognizable format",
			document: []byte{0x17, 0x42, 0x99, 0x03, 0xfe, 0x00, 0x21, 0x7a},
			want:     "application/octet-stream",
		},
		{
			// An image kind pkg/render/imagejob has no decoder for is refused
			// here rather than after a worker has spawned to fail at decode.
			name:     "an image format no decoder here reads",
			document: []byte(heicHeader),
			want:     "image/heic",
		},
		{
			name:     "svg, which is a document rather than a raster",
			document: []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`),
			want:     "image/svg+xml",
		},
		{
			// The zip that is only a zip must not reach soffice on the strength
			// of sharing a container with docx and odt.
			name:     "a plain zip archive",
			document: []byte("PK\x03\x04\x14\x00\x00\x00\x00\x00"),
			want:     "application/zip",
		},
		{
			// A compound file whose directory names no format detection knows
			// falls back to the container type, which routes nowhere.
			name:     "a compound file naming no known stream",
			document: cfbFixture("Unknown Stream"),
			want:     "application/x-ole-storage",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertUnsupported(t, tt.want, mustFailDetect(t, tt.document))
			assertUnsupported(t, tt.want, mustFailDetectFile(t, tt.document))
		})
	}
}

// A marker sitting past the prefix leaves the document as its container type,
// which is refused rather than guessed at. The boundary is a property of
// PrefixSize, so this pins where it is.
func TestDetectPastPrefixSize(t *testing.T) {
	within := ooxmlFixture(t, "word", PrefixSize/2)
	kind, err := Detect(within)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if kind != worker.KindOffice {
		t.Fatalf("Detect = %q, want %q", kind, worker.KindOffice)
	}

	beyond := ooxmlFixture(t, "word", PrefixSize)
	assertUnsupported(t, "application/zip", mustFailDetect(t, beyond))
	assertUnsupported(t, "application/zip", mustFailDetectFile(t, beyond))
}

// A marker at the head of a document places it however much follows, which is
// what lets DetectFile stop at PrefixSize.
func TestDetectFileFromTheHeadOfALargeDocument(t *testing.T) {
	document := append([]byte(pdfHeader), make([]byte, 4*PrefixSize)...)

	kind, err := DetectFile(writeDocument(t, document))
	if err != nil {
		t.Fatalf("DetectFile: %v", err)
	}
	if kind != worker.KindPdf {
		t.Errorf("DetectFile = %q, want %q", kind, worker.KindPdf)
	}
}

// A document that could not be read at all is a different failure from one
// that was read and not recognized, and the server maps the two apart.
func TestDetectFileMissingDocument(t *testing.T) {
	_, err := DetectFile(filepath.Join(t.TempDir(), "gone.bin"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if _, ok := errors.AsType[*UnsupportedMediaTypeError](err); ok {
		t.Error("an unreadable document must not report as an unsupported one")
	}
}

func mustFailDetect(t *testing.T, document []byte) error {
	t.Helper()

	kind, err := Detect(document)
	if err == nil {
		t.Fatalf("Detect = %q, want an error", kind)
	}
	return err
}

func mustFailDetectFile(t *testing.T, document []byte) error {
	t.Helper()

	kind, err := DetectFile(writeDocument(t, document))
	if err == nil {
		t.Fatalf("DetectFile = %q, want an error", kind)
	}
	return err
}

func assertUnsupported(t *testing.T, wantMediaType string, err error) {
	t.Helper()

	unsupported, ok := errors.AsType[*UnsupportedMediaTypeError](err)
	if !ok {
		t.Fatalf("err = %v, want *UnsupportedMediaTypeError", err)
	}
	if unsupported.MediaType != wantMediaType {
		t.Errorf("MediaType = %q, want %q", unsupported.MediaType, wantMediaType)
	}
}

func writeDocument(t *testing.T, document []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
