package officejob

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/worker"
)

// missingBinary is a name no PATH lookup can resolve, which is what a
// deployment without LibreOffice looks like from here.
const missingBinary = "gahaku-soffice-not-installed"

// assertNoPageWritten checks that a failed render left no rendered page behind.
// It looks for pages rather than for an empty directory the way the other
// pipelines' tests do: a conversion that got as far as running leaves its
// scratch directories in the job directory whether it worked or not.
func assertNoPageWritten(t *testing.T, jobDir string) {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join(jobDir, "page-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("job dir holds %s, want a failed render to leave no page",
			strings.Join(pages, ", "))
	}
}

func assertJobDirEmpty(t *testing.T, jobDir string) {
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
		t.Errorf("job dir holds %s, want it untouched", strings.Join(names, ", "))
	}
}

// A deployment without LibreOffice has to say so as itself rather than as a
// conversion that went wrong, and it has to say so before it makes anything:
// pkg/server answers this one UNIMPLEMENTED.
func TestRenderMissingConverter(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeInput(t, t.TempDir(), []byte("an office document")),
		JobDir:    jobDir,
	}

	_, err := Converter{Binary: missingBinary}.Render(t.Context(), spec)
	missing, ok := errors.AsType[*ConverterMissingError](err)
	if !ok {
		t.Fatalf("err = %v, want *ConverterMissingError", err)
	}
	if missing.Binary != missingBinary {
		t.Errorf("Binary = %q, want %q", missing.Binary, missingBinary)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want it to unwrap to exec.ErrNotFound", err)
	}
	assertJobDirEmpty(t, jobDir)
}

func TestRenderMissingInput(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: filepath.Join(t.TempDir(), "gone.bin"),
		JobDir:    jobDir,
	}

	_, err := Converter{Binary: fakeSoffice(t, "exit 0\n")}.Render(t.Context(), spec)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if _, ok := errors.AsType[*ConversionError](err); ok {
		t.Error("a missing input must not report as a failed conversion")
	}
	assertJobDirEmpty(t, jobDir)
}

// The two shapes a conversion fails in. Neither may pass for a render: the
// clean exit is the one that would, since nothing but the missing output says
// it went wrong.
func TestRenderFailedConversion(t *testing.T) {
	const message = "Error: source file could not be loaded"

	for _, tt := range []struct {
		name         string
		script       string
		wantExitCode int
		// wantWrapped is whether the error carries the process failure, which
		// only the run that failed as a process has.
		wantWrapped bool
		// wantMissingPath is whether the error names the document it went
		// looking for, which only the run that claimed success has.
		wantMissingPath bool
	}{
		{
			name:         "a nonzero exit",
			script:       "echo '" + message + "' >&2\nexit 3\n",
			wantExitCode: 3,
			wantWrapped:  true,
		},
		{
			name:            "a clean exit that converted nothing",
			script:          "echo '" + message + "'\nexit 0\n",
			wantMissingPath: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jobDir := t.TempDir()
			spec := worker.Spec{
				InputPath: writeInput(t, jobDir, []byte("an office document")),
				JobDir:    jobDir,
			}

			_, err := Converter{Binary: fakeSoffice(t, tt.script)}.Render(t.Context(), spec)
			failed, ok := errors.AsType[*ConversionError](err)
			if !ok {
				t.Fatalf("err = %v, want *ConversionError", err)
			}
			if failed.ExitCode != tt.wantExitCode {
				t.Errorf("ExitCode = %d, want %d", failed.ExitCode, tt.wantExitCode)
			}
			// The document that was looked for, so a soffice naming its
			// output differently reads as that and not as a bad input.
			wantPath := ""
			if tt.wantMissingPath {
				wantPath = filepath.Join(jobDir, convertedDirName, "input.pdf")
			}
			if failed.Path != wantPath {
				t.Errorf("Path = %q, want %q", failed.Path, wantPath)
			}
			// What soffice said travels with the error whichever stream it
			// said it on, since that is all an operator gets to read.
			if !strings.Contains(failed.Output, message) {
				t.Errorf("Output = %q, want it to carry %q", failed.Output, message)
			}
			if !strings.Contains(err.Error(), message) {
				t.Errorf("err = %v, want its message to carry %q", err, message)
			}
			if _, wrapped := errors.AsType[*exec.ExitError](err); wrapped != tt.wantWrapped {
				t.Errorf("unwraps to *exec.ExitError = %v, want %v", wrapped, tt.wantWrapped)
			}
			assertNoPageWritten(t, jobDir)
		})
	}
}

// A converter logging without end must not become the worker's memory
// problem, so what the error carries is capped.
func TestRenderBoundsConverterOutput(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeInput(t, jobDir, []byte("an office document")),
		JobDir:    jobDir,
	}
	// 64 bytes a line, well past the cap by the time it gives up.
	chatty := fakeSoffice(t, `
i=0
while [ $i -lt 1000 ]; do
	echo "0123456789012345678901234567890123456789012345678901234567890123"
	i=$((i + 1))
done
exit 1
`)

	_, err := Converter{Binary: chatty}.Render(t.Context(), spec)
	failed, ok := errors.AsType[*ConversionError](err)
	if !ok {
		t.Fatalf("err = %v, want *ConversionError", err)
	}
	if len(failed.Output) != maxOutputBytes {
		t.Errorf("Output is %d bytes, want it capped at %d", len(failed.Output), maxOutputBytes)
	}
}

// The format is checked before anything is converted, so a request no encoder
// could satisfy costs no LibreOffice run — which is what the binary that does
// not exist here proves.
func TestRenderRejectsUnsupportedFormat(t *testing.T) {
	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeInput(t, t.TempDir(), []byte("an office document")),
		JobDir:    jobDir,
		Options:   worker.Options{Format: worker.Format("psd")},
	}

	_, err := Converter{Binary: missingBinary}.Render(t.Context(), spec)
	if _, ok := errors.AsType[*render.UnsupportedFormatError](err); !ok {
		t.Fatalf("err = %v, want *render.UnsupportedFormatError", err)
	}
	assertJobDirEmpty(t, jobDir)
}

func TestRenderHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	jobDir := t.TempDir()
	spec := worker.Spec{
		InputPath: writeInput(t, jobDir, []byte("an office document")),
		JobDir:    jobDir,
	}
	// A converter that would have succeeded, so the cancellation is what
	// stopped the render and not a conversion that was going to fail anyway.
	converter := Converter{Binary: convertingSoffice(t, buildPDF([]pdfPage{{72, 72}}))}

	_, err := converter.Render(ctx, spec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoPageWritten(t, jobDir)
	if _, err := os.Stat(convertedDir(jobDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(converted dir) = %v, want a canceled render to convert nothing", err)
	}
}
