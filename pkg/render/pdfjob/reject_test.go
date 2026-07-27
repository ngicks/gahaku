package pdfjob

import (
	"bytes"
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

// A range starting past the last page selects nothing, which is the one
// out-of-range case the pipeline refuses instead of clamping (D-01).
func TestRenderRejectsPageRangesPastTheEnd(t *testing.T) {
	input := writeFixture(t, fixture)

	for _, tt := range []struct {
		name  string
		pages worker.PageRange
	}{
		{name: "one past the last page", pages: worker.PageRange{FirstPage: 4, LastPage: 4}},
		{name: "open ended past the last page", pages: worker.PageRange{FirstPage: 4}},
		{name: "well past the last page", pages: worker.PageRange{FirstPage: 9, LastPage: 12}},
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
		InputPath: writeFixture(t, fixture),
		JobDir:    jobDir,
		Options:   worker.Options{Pages: worker.PageRange{FirstPage: 3, LastPage: 2}},
	}

	_, err := Render(t.Context(), spec)
	if _, ok := errors.AsType[*render.InvalidPageRangeError](err); !ok {
		t.Fatalf("err = %v, want *render.InvalidPageRangeError", err)
	}
	assertNoPageWritten(t, jobDir)
}

// The format is checked before the input is opened, so a request no encoder
// could satisfy costs neither a read nor a wasm instance.
func TestRenderRejectsUnsupportedFormat(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: filepath.Join(t.TempDir(), "never-read.pdf"),
		JobDir:    jobDir,
		Options:   worker.Options{Format: worker.Format("psd")},
	}

	_, err := Render(t.Context(), spec)
	if _, ok := errors.AsType[*render.UnsupportedFormatError](err); !ok {
		t.Fatalf("err = %v, want *render.UnsupportedFormatError", err)
	}
	assertNoPageWritten(t, jobDir)
}

// Input pdfium cannot load has to arrive as a DecodeError, which is what
// pkg/server maps to INVALID_ARGUMENT rather than to an internal failure.
func TestRenderUndecodableInput(t *testing.T) {
	full := buildPDF(fixture)

	for _, tt := range []struct {
		name    string
		content []byte
	}{
		{name: "not a pdf at all", content: []byte("this is not a pdf")},
		{name: "empty file", content: []byte{}},
		{name: "a single byte", content: []byte("x")},
		{name: "a pdf header with nothing behind it", content: []byte("%PDF-1.7\n")},
		{name: "truncated mid object", content: full[:len(full)/2]},
		{name: "no xref table or trailer", content: full[:bytes.Index(full, []byte("xref"))]},
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
		InputPath: filepath.Join(t.TempDir(), "gone.pdf"),
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
		InputPath: writeFixture(t, fixture),
		JobDir:    jobDir,
	}

	_, err := Render(ctx, spec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoPageWritten(t, jobDir)
}
