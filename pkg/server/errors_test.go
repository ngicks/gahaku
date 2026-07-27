package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/render/officejob"
	"github.com/ngicks/gahaku/pkg/worker"
)

// renderWithRunnerError runs a job whose worker fails with err and returns the
// status the caller ends up with.
func renderWithRunnerError(t *testing.T, err error) error {
	t.Helper()
	client := newClient(t, Config{Runner: &fakeRunner{err: err}})
	_, got := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		chunkRequest(pdfDocument),
	)
	return got
}

func TestJobFailuresMapToStatusCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{
			name: "a job killed by its deadline",
			err:  &worker.KilledError{Cause: context.DeadlineExceeded, Timeout: time.Second},
			want: codes.DeadlineExceeded,
		},
		{
			name: "a job killed by the caller hanging up",
			err:  &worker.KilledError{Cause: context.Canceled, Timeout: time.Second},
			want: codes.Canceled,
		},
		{
			name: "a worker the kernel reaped",
			err:  &worker.OOMKilledError{},
			want: codes.ResourceExhausted,
		},
		{
			name: "a worker that exited nonzero",
			err:  &worker.ExitError{ExitCode: 1},
			want: codes.Internal,
		},
		{
			name: "a worker that left no result frame",
			err:  &worker.ProtocolError{Err: errors.New("no result frame on stdout")},
			want: codes.Internal,
		},
		{
			name: "a failure class this build does not know",
			err:  &worker.JobError{Code: "render.from_another_build", Message: "who knows"},
			want: codes.Internal,
		},
		{
			name: "a deployment without LibreOffice",
			err:  &officejob.ConverterMissingError{Binary: "soffice", Err: errors.New("not found")},
			want: codes.Unimplemented,
		},
		{
			name: "a document soffice could not convert",
			err:  &officejob.ConversionError{ExitCode: 1},
			want: codes.InvalidArgument,
		},
		{
			name: "a document the pipeline could not decode",
			err:  &render.DecodeError{Err: errors.New("unexpected EOF")},
			want: codes.InvalidArgument,
		},
		{
			name: "a page range starting past the end",
			err:  &render.PageRangeOutOfRangeError{FirstPage: 5, DocumentPages: 2},
			want: codes.OutOfRange,
		},
		{
			name: "an output format nothing encodes",
			err:  &render.UnsupportedFormatError{Format: "xyz"},
			want: codes.InvalidArgument,
		},
		{
			name: "anything this table does not know",
			err:  errors.New("something gave way"),
			want: codes.Internal,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := renderWithRunnerError(t, tt.err)
			requireCode(t, err, tt.want)
		})
	}
}

func TestAPageRangePastTheEndCarriesItsDetail(t *testing.T) {
	err := renderWithRunnerError(t, &render.PageRangeOutOfRangeError{
		FirstPage:     5,
		DocumentPages: 2,
	})

	detail := detailOf[*gahakuv1.PageRangeOutOfRange](t, err)
	if detail.GetFirstPage() != 5 || detail.GetPageCount() != 2 {
		t.Fatalf("want page 5 of a 2 page document, got %+v", detail)
	}
}

func TestAnInternalStatusKeepsTheWorkersLogToItself(t *testing.T) {
	const leak = "/tmp/gahaku-job-2187/soffice-profile: permission denied"

	err := renderWithRunnerError(t, &worker.ExitError{ExitCode: 1, Stderr: leak})

	requireCode(t, err, codes.Internal)
	if message := status.Convert(err).Message(); strings.Contains(message, leak) {
		t.Fatalf("the status leaked the worker's log: %s", message)
	}
}

// TestAWorkerReportedFailureKeepsItsStatus renders through a worker
// subprocess, so what it asserts is the whole crossing: the class the worker
// gave its failure, the result frame it wrote, and the error the orchestrator
// restored from it. A failure the render packages named gets the status it
// would have got in process; one they did not stays INTERNAL.
func TestAWorkerReportedFailureKeepsItsStatus(t *testing.T) {
	for _, tt := range []struct {
		name    string
		failure string
		want    codes.Code
	}{
		{
			name:    "a document the pipeline could not decode",
			failure: "decode",
			want:    codes.InvalidArgument,
		},
		{
			name:    "a page range starting past the end",
			failure: "page-range",
			want:    codes.OutOfRange,
		},
		{
			name:    "a deployment without LibreOffice",
			failure: "converter-missing",
			want:    codes.Unimplemented,
		},
		{
			name:    "a failure no class covers",
			failure: "unclassified",
			want:    codes.Internal,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := renderWithFailingWorker(t, tt.failure)

			requireCode(t, err, tt.want)
		})
	}
}

func TestAWorkerReportedPageRangeCarriesItsDetail(t *testing.T) {
	err := renderWithFailingWorker(t, "page-range")

	detail := detailOf[*gahakuv1.PageRangeOutOfRange](t, err)
	if detail.GetFirstPage() != 5 || detail.GetPageCount() != 2 {
		t.Fatalf("want page 5 of a 2 page document, got %+v", detail)
	}
}

func TestAWorkerReportedFailureKeepsItsLogToItself(t *testing.T) {
	err := renderWithFailingWorker(t, "unclassified")

	requireCode(t, err, codes.Internal)
	if message := status.Convert(err).Message(); strings.Contains(message, "gave way") {
		t.Fatalf("the status leaked what the worker reported: %s", message)
	}
}
