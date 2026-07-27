package officejob

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	// Decoders for the formats these tests ask the pipeline to produce, so a
	// test can read back what it rendered.
	_ "image/jpeg"
	_ "image/png"

	"github.com/ngicks/gahaku/pkg/worker"
)

// pdfPage is one page of a fixture document, sized in PDF points (1/72 inch).
type pdfPage struct {
	width  int
	height int
}

// buildPDF assembles a complete PDF: catalog, page tree, one content stream
// per page and an xref table carrying real byte offsets. It is generated
// rather than checked in so a test can pick page sizes it can tell apart, and
// it is duplicated from pdfjob's rather than shared because a _test.go file is
// not importable. What officejob needs it for is a document its fake soffice
// can hand back.
func buildPDF(pages []pdfPage) []byte {
	n := len(pages)
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

// fakeSoffice writes body into an executable script and returns its path,
// which a Converter runs in place of LibreOffice. The path holds a separator,
// so os/exec runs it as it is instead of searching PATH.
func fakeSoffice(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "soffice")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// convertingSoffice is a fake that does what LibreOffice does with the
// arguments this package passes: it writes pdf into the --outdir under the
// input document's name with the extension replaced. It creates no directory,
// so a Render that skipped creating the output directory fails here instead of
// passing quietly.
func convertingSoffice(t *testing.T, pdf []byte) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "fixture.pdf")
	if err := os.WriteFile(fixture, pdf, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return fakeSoffice(t, `
outdir=""
prev=""
for arg in "$@"; do
	if [ "$prev" = "--outdir" ]; then
		outdir="$arg"
	fi
	prev="$arg"
	input="$arg"
done
base=$(basename "$input")
cp "`+fixture+`" "$outdir/${base%.*}.pdf"
`)
}

func writeInput(t *testing.T, jobDir string, content []byte) string {
	t.Helper()
	// input.bin is the name pkg/input materializes every source to.
	path := filepath.Join(jobDir, "input.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

type decodedPage struct {
	format string
	config image.Config
}

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

// The argument list is what a deployment without LibreOffice can still be held
// to, so it is asserted whole. Synthetic job directories keep it literal:
// building a command touches no filesystem.
func TestCommandArgs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		converter Converter
		spec      worker.Spec
		want      []string
	}{
		{
			name: "the default binary",
			spec: worker.Spec{InputPath: "/tmp/job-1/input.bin", JobDir: "/tmp/job-1"},
			want: []string{
				"soffice",
				"-env:UserInstallation=file:///tmp/job-1/soffice-profile",
				"--headless",
				"--convert-to", "pdf",
				"--outdir", "/tmp/job-1/converted",
				"/tmp/job-1/input.bin",
			},
		},
		{
			name:      "an installation elsewhere",
			converter: Converter{Binary: "/opt/libreoffice/program/soffice"},
			spec:      worker.Spec{InputPath: "/tmp/job-2/input.bin", JobDir: "/tmp/job-2"},
			want: []string{
				"/opt/libreoffice/program/soffice",
				"-env:UserInstallation=file:///tmp/job-2/soffice-profile",
				"--headless",
				"--convert-to", "pdf",
				"--outdir", "/tmp/job-2/converted",
				"/tmp/job-2/input.bin",
			},
		},
		{
			// The profile is a URL, so a job directory holding a space has to
			// come back escaped rather than as two arguments' worth of URL.
			name: "a job directory holding a space",
			spec: worker.Spec{InputPath: "/tmp/job 3/input.bin", JobDir: "/tmp/job 3"},
			want: []string{
				"soffice",
				"-env:UserInstallation=file:///tmp/job%203/soffice-profile",
				"--headless",
				"--convert-to", "pdf",
				"--outdir", "/tmp/job 3/converted",
				"/tmp/job 3/input.bin",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := tt.converter.command(t.Context(), tt.spec)
			if err != nil {
				t.Fatalf("command: %v", err)
			}
			if !slices.Equal(cmd.Args, tt.want) {
				t.Errorf("args =\n%q\nwant\n%q", cmd.Args, tt.want)
			}
		})
	}
}

func argWithPrefix(t *testing.T, cmd *exec.Cmd, prefix string) string {
	t.Helper()
	for _, arg := range cmd.Args {
		if after, ok := strings.CutPrefix(arg, prefix); ok {
			return after
		}
	}
	t.Fatalf("no argument starting with %q in %q", prefix, cmd.Args)
	return ""
}

// Two jobs may run at once, and soffice processes pointed at one user profile
// will not, so no two jobs may be handed the same profile — nor the same
// output directory, where they would overwrite each other's PDF.
func TestCommandProfilesAreJobLocal(t *testing.T) {
	commands := make([]*exec.Cmd, 0, 2)
	for _, jobDir := range []string{"/tmp/job-1", "/tmp/job-2"} {
		spec := worker.Spec{InputPath: filepath.Join(jobDir, "input.bin"), JobDir: jobDir}
		cmd, err := Converter{}.command(t.Context(), spec)
		if err != nil {
			t.Fatalf("command(%s): %v", jobDir, err)
		}
		commands = append(commands, cmd)

		profile := argWithPrefix(t, cmd, "-env:UserInstallation=file://")
		if !strings.HasPrefix(profile, jobDir+"/") {
			t.Errorf("profile = %q, want it inside the job dir %q", profile, jobDir)
		}
		outdir := cmd.Args[slices.Index(cmd.Args, "--outdir")+1]
		if !strings.HasPrefix(outdir, jobDir+"/") {
			t.Errorf("outdir = %q, want it inside the job dir %q", outdir, jobDir)
		}
	}

	first := argWithPrefix(t, commands[0], "-env:UserInstallation=")
	second := argWithPrefix(t, commands[1], "-env:UserInstallation=")
	if first == second {
		t.Errorf("both jobs got the profile %q, want one each", first)
	}
}

// The whole pipeline over a fake converter: the PDF soffice leaves behind
// reaches pdfjob, and the options the request carried are the ones that
// rasterized it — the page range picked the second page, the dpi sized it and
// the format encoded it.
func TestRenderConvertsThenRasterizes(t *testing.T) {
	pages := []pdfPage{{width: 72, height: 72}, {width: 144, height: 72}}
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeInput(t, jobDir, []byte("an office document")),
		JobDir:    jobDir,
		Options: worker.Options{
			Format: worker.FormatJpeg,
			Dpi:    72,
			Pages:  worker.PageRange{FirstPage: 2},
		},
	}
	converter := Converter{Binary: convertingSoffice(t, buildPDF(pages))}

	result, err := converter.Render(t.Context(), spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.DocumentPages != len(pages) {
		t.Errorf("DocumentPages = %d, want the converted document's %d",
			result.DocumentPages, len(pages))
	}

	rendered := decodePages(t, result, jobDir)
	if len(rendered) != 1 {
		t.Fatalf("rendered %d pages, want the 1 the range selected", len(rendered))
	}
	if rendered[0].format != string(worker.FormatJpeg) {
		t.Errorf("format = %q, want jpeg", rendered[0].format)
	}
	// At 72 dpi a page comes out as many pixels as it is points wide, so the
	// width says which page of the document was rendered.
	if got := rendered[0].config; got.Width != 144 || got.Height != 72 {
		t.Errorf("size = %dx%d, want the second page's 144x72", got.Width, got.Height)
	}
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

// The one test that runs LibreOffice itself, which is what holds the argument
// list and the output naming to what soffice really does. It also renders an
// input named input.bin, the name pkg/input materializes to: nothing gives
// soffice the format but the bytes, and the pipeline would stop here if that
// were not enough for it.
func TestRenderWithRealSoffice(t *testing.T) {
	if path, err := exec.LookPath(DefaultBinary); err != nil {
		t.Skipf("%s is not installed: %v", DefaultBinary, err)
	} else {
		t.Logf("converting with %s", path)
	}

	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeInput(t, jobDir, buildDocx(t)),
		JobDir:    jobDir,
		Options:   worker.Options{Dpi: 72},
	}

	result, err := Render(t.Context(), spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.DocumentPages != 1 {
		t.Errorf("DocumentPages = %d, want the document's 1", result.DocumentPages)
	}

	rendered := decodePages(t, result, jobDir)
	if len(rendered) != 1 {
		t.Fatalf("rendered %d pages, want 1", len(rendered))
	}
	if rendered[0].format != string(worker.FormatPng) {
		t.Errorf("format = %q, want the default png", rendered[0].format)
	}
	// A4 at 72 dpi is about 595x842 points; the assert stays loose because the
	// page size is LibreOffice's default and not this test's to pick.
	if got := rendered[0].config; got.Width < 100 || got.Height < 100 {
		t.Errorf("size = %dx%d, want a whole page", got.Width, got.Height)
	}
}
