package imagejob

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/worker"
)

func writeRawFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestRenderRejectsPageRangesWithoutPageOne(t *testing.T) {
	input := writeFixture(t, worker.FormatPng, gradient(8, 8))

	for _, tt := range []struct {
		name  string
		pages worker.PageRange
	}{
		{name: "starting past the page", pages: worker.PageRange{FirstPage: 2, LastPage: 2}},
		{name: "open ended, starting past the page", pages: worker.PageRange{FirstPage: 2}},
		{name: "well past the page", pages: worker.PageRange{FirstPage: 3, LastPage: 5}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: input,
				JobDir:    jobDir,
				Options:   worker.Options{Pages: tt.pages},
			}

			_, err := Render(t.Context(), spec)
			if _, ok := errors.AsType[*render.PageRangeOutOfRangeError](err); !ok {
				t.Fatalf("err = %v, want *render.PageRangeOutOfRangeError", err)
			}
			assertNoPageWritten(t, jobDir)
		})
	}
}

func TestRenderRejectsReversedPageRange(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeFixture(t, worker.FormatPng, gradient(8, 8)),
		JobDir:    jobDir,
		Options:   worker.Options{Pages: worker.PageRange{FirstPage: 5, LastPage: 2}},
	}

	_, err := Render(t.Context(), spec)
	if _, ok := errors.AsType[*render.InvalidPageRangeError](err); !ok {
		t.Fatalf("err = %v, want *render.InvalidPageRangeError", err)
	}
}

// The format is checked before the document is opened, so a request nothing
// could satisfy costs no decode.
func TestRenderRejectsUnsupportedFormat(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: filepath.Join(t.TempDir(), "never-read.bin"),
		JobDir:    jobDir,
		Options:   worker.Options{Format: worker.Format("psd")},
	}

	_, err := Render(t.Context(), spec)
	if _, ok := errors.AsType[*render.UnsupportedFormatError](err); !ok {
		t.Fatalf("err = %v, want *render.UnsupportedFormatError", err)
	}
	assertNoPageWritten(t, jobDir)
}

func TestRenderUndecodableInput(t *testing.T) {
	pngHeader := "\x89PNG\r\n\x1a\n"

	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "not an image at all", content: "this is not an image"},
		{name: "empty file", content: ""},
		{name: "a png header with nothing behind it", content: pngHeader},
		{name: "a truncated png", content: pngHeader + "\x00\x00\x00\rIHDR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: writeRawFixture(t, tt.content),
				JobDir:    jobDir,
			}

			_, err := Render(t.Context(), spec)
			if _, ok := errors.AsType[*render.DecodeError](err); !ok {
				t.Fatalf("err = %v, want *render.DecodeError", err)
			}
			assertNoPageWritten(t, jobDir)
		})
	}
}

func TestRenderMissingInput(t *testing.T) {
	spec := worker.Spec{
		InputPath: filepath.Join(t.TempDir(), "gone.bin"),
		JobDir:    t.TempDir(),
	}

	_, err := Render(t.Context(), spec)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if _, ok := errors.AsType[*render.DecodeError](err); ok {
		t.Error("a missing input must not report as undecodable")
	}
}

func TestRenderHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeFixture(t, worker.FormatPng, gradient(8, 8)),
		JobDir:    jobDir,
	}

	_, err := Render(ctx, spec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoPageWritten(t, jobDir)
}

func assertNoPageWritten(t *testing.T, jobDir string) {
	t.Helper()
	entries, err := os.ReadDir(jobDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("job dir holds %s, want a failed render to leave none", strings.Join(names, ", "))
	}
}
