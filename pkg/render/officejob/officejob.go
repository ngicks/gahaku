// Package officejob renders an MS Office document by converting it to PDF with
// LibreOffice and rasterizing that PDF through pdfjob (D-C, D-06).
//
// Converting first keeps one raster path for every page-based kind: a page
// range, a dpi and an output format mean here exactly what they mean for a
// PDF, because the same code applies them. soffice's own --convert-to png
// would have been a second renderer with its own idea of both.
//
// soffice runs as a plain child of the worker process and nothing here bounds
// it: the orchestrator owns the job's deadline and kills the worker's whole
// process group when it passes (pkg/worker), which takes the launcher script
// and the soffice.bin it exec'd down with the worker.
//
// Every job converts under a LibreOffice user profile of its own inside the
// job directory, because soffice processes sharing one profile refuse to run
// at the same time (PLAN.md, worker model).
package officejob

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/render/pdfjob"
	"github.com/ngicks/gahaku/pkg/worker"
)

// DefaultBinary is the LibreOffice launcher a Converter runs, looked up on
// PATH.
const DefaultBinary = "soffice"

const (
	// profileDirName is the job-local LibreOffice user profile.
	profileDirName = "soffice-profile"
	// convertedDirName holds soffice's output. It is a directory of its own so
	// the intermediate PDF does not sit among the rendered pages the
	// orchestrator reads out of the job directory.
	convertedDirName = "converted"
	// maxOutputBytes bounds what a failed conversion carries back.
	maxOutputBytes = 8 << 10
)

var _ worker.HandlerFunc = Render

// Render converts the Office document at spec.InputPath to PDF with the
// LibreOffice found on PATH and rasterizes it into spec.JobDir. It is the
// entry point `gahaku worker office` runs.
func Render(ctx context.Context, spec worker.Spec) (worker.Result, error) {
	return Converter{}.Render(ctx, spec)
}

// Converter renders Office documents through a particular LibreOffice
// installation. The zero value uses DefaultBinary, which is what the
// package-level Render is.
type Converter struct {
	// Binary is the soffice executable. A bare name is looked up on PATH, a
	// path is run as it is, and empty selects DefaultBinary.
	Binary string
}

func (c Converter) binary() string {
	if c.Binary == "" {
		return DefaultBinary
	}
	return c.Binary
}

// Render converts spec.InputPath to PDF and hands that PDF to pdfjob.Render,
// which rasterizes the pages spec.Options selects into spec.JobDir.
//
// spec.Options travels untouched: page range, dpi and format describe the
// pages the document produces, and the conversion is a step on the way to
// them rather than something they apply to.
func (c Converter) Render(ctx context.Context, spec worker.Spec) (worker.Result, error) {
	// Checked here and not left to pdfjob so a request no encoder could
	// satisfy does not first cost a LibreOffice run. The empty format is
	// pdfjob's to default.
	if f := spec.Options.Format; f != "" && !render.CanEncode(f) {
		return worker.Result{}, &render.UnsupportedFormatError{Format: f}
	}
	if err := ctx.Err(); err != nil {
		return worker.Result{}, err
	}

	converted, err := c.convert(ctx, spec)
	if err != nil {
		return worker.Result{}, err
	}

	spec.InputPath = converted
	return pdfjob.Render(ctx, spec)
}

// convert runs soffice over spec's input document and returns the PDF it left
// in the job directory.
func (c Converter) convert(ctx context.Context, spec worker.Spec) (string, error) {
	cmd, err := c.command(ctx, spec)
	if err != nil {
		return "", err
	}
	// os/exec resolves the binary while the command is built, so a deployment
	// without LibreOffice is reported before the job directory is touched.
	if cmd.Err != nil {
		if errors.Is(cmd.Err, exec.ErrNotFound) {
			return "", &ConverterMissingError{Binary: c.binary(), Err: cmd.Err}
		}
		return "", fmt.Errorf("officejob: locating %q: %w", c.binary(), cmd.Err)
	}
	// soffice answers an input it cannot open the same way it answers one it
	// cannot parse, so the file is checked here for the missing input to keep
	// reporting itself as missing.
	if _, err := os.Stat(spec.InputPath); err != nil {
		return "", fmt.Errorf("officejob: opening input document: %w", err)
	}
	if err := os.MkdirAll(convertedDir(spec.JobDir), 0o700); err != nil {
		return "", fmt.Errorf("officejob: creating the conversion directory: %w", err)
	}

	output := &outputBuffer{max: maxOutputBytes}
	// Both streams are captured, and into the same writer: os/exec hands the
	// child one file descriptor for the two when Stdout and Stderr are the
	// same value, so this stays a single copy with no interleaving of its own.
	// The worker's real stdout carries the result frame (pkg/worker), which is
	// reason enough for soffice's chatter not to reach it.
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Run(); err != nil {
		if cause := ctx.Err(); cause != nil {
			return "", cause
		}
		exitErr, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			return "", fmt.Errorf("officejob: running %q: %w", c.binary(), err)
		}
		return "", &ConversionError{
			ExitCode: exitErr.ExitCode(),
			Output:   output.String(),
			Err:      err,
		}
	}

	converted := convertedPath(spec.JobDir, spec.InputPath)
	if _, err := os.Stat(converted); err != nil {
		// A clean exit is not a converted document: soffice reports a source
		// it could not load by writing nothing and exiting 0.
		return "", &ConversionError{Path: converted, Output: output.String()}
	}
	return converted, nil
}

// command builds the soffice invocation for spec. It is separate from running
// it so the argument list — the profile isolation above all — is testable
// where LibreOffice is not installed.
func (c Converter) command(ctx context.Context, spec worker.Spec) (*exec.Cmd, error) {
	profile, err := profileURI(spec.JobDir)
	if err != nil {
		return nil, err
	}
	// The context is here for in-process cancellation only; what actually
	// bounds a runaway conversion is the orchestrator's process-group kill.
	return exec.CommandContext(
		ctx,
		c.binary(),
		"-env:UserInstallation="+profile,
		"--headless",
		"--convert-to", "pdf",
		"--outdir", convertedDir(spec.JobDir),
		spec.InputPath,
	), nil
}

// profileURI is the file URL of this job's LibreOffice user profile.
// LibreOffice wants a URL there rather than a path, and the escaping matters:
// a job directory holding a space would otherwise end the argument early.
func profileURI(jobDir string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(jobDir, profileDirName))
	if err != nil {
		return "", fmt.Errorf("officejob: resolving the profile directory: %w", err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String(), nil
}

func convertedDir(jobDir string) string {
	return filepath.Join(jobDir, convertedDirName)
}

// convertedPath is where soffice leaves the PDF: it names its output after the
// input document with the extension replaced, so the input.bin pkg/input
// materializes comes back as input.pdf. The name is derived rather than
// searched for because the directory is this job's alone and a stat of the one
// path it can be says more when it fails than an empty listing would.
func convertedPath(jobDir, inputPath string) string {
	name := filepath.Base(inputPath)
	name = strings.TrimSuffix(name, filepath.Ext(name)) + ".pdf"
	return filepath.Join(convertedDir(jobDir), name)
}

// outputBuffer keeps the first max bytes written to it and drops the rest,
// which bounds what a converter logging in a loop can hold in the worker's
// memory. The head is what is kept: soffice names what went wrong in its first
// line or two and then repeats itself.
//
// Writes never fail. A short write surfaces out of Cmd.Wait, where it would be
// indistinguishable from the conversion itself failing, which a cap on logging
// has no business claiming.
type outputBuffer struct {
	max int
	buf []byte
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *outputBuffer) String() string { return string(b.buf) }
