package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/output"
	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/render/officejob"
	"github.com/ngicks/gahaku/pkg/render/workererr"
	"github.com/ngicks/gahaku/pkg/sniff"
	"github.com/ngicks/gahaku/pkg/worker"
)

// The protocol violations, which are statuses from the start: there is no
// package below this one that could have a typed error for them.
var (
	errHeaderExpected = status.Error(
		codes.InvalidArgument,
		"the first message of a render stream must carry a header",
	)
	errChunkExpected = status.Error(
		codes.InvalidArgument,
		"every message after the header must carry an input chunk",
	)
	errSourceRequired = status.Error(
		codes.InvalidArgument,
		"the request header must select an input source",
	)
	errOutputRequired = status.Error(
		codes.InvalidArgument,
		"the request header must select an output spec",
	)
)

// KindMismatchError reports a document that is not the kind the typed RPC it
// arrived on asserts. Maps to INVALID_ARGUMENT.
type KindMismatchError struct {
	// Asserted is the kind the RPC stands for.
	Asserted worker.Kind
	// Detected is what the document's own bytes say it is.
	Detected worker.Kind
}

func (e *KindMismatchError) Error() string {
	return fmt.Sprintf(
		"server: the document is a %s, not the %s this rpc renders",
		e.Detected, e.Asserted,
	)
}

// ShortDocumentError reports a document with fewer pages than the requested
// range, which leaves presigned PUT destinations unwritten. It carries what
// pkg/output cannot know — which pages were asked for and how many the document
// had. Maps to OUT_OF_RANGE with a PageRangeOutOfRange detail.
type ShortDocumentError struct {
	FirstPage     uint32
	LastPage      uint32
	DocumentPages int
	// Err is the sink's *output.ShortPageCountError.
	Err error
}

func (e *ShortDocumentError) Error() string {
	return fmt.Sprintf(
		"server: pages %d-%d of a %d page document: %s",
		e.FirstPage, e.LastPage, e.DocumentPages, e.Err,
	)
}

func (e *ShortDocumentError) Unwrap() error { return e.Err }

// statusCarrier is an error that already knows its own status, which is either
// one of the sentinels above or something gRPC itself produced — a canceled
// stream, a client that went away mid-response.
type statusCarrier interface {
	error
	GRPCStatus() *status.Status
}

// fail turns a job failure into the status the caller sees and logs what that
// status leaves out: an internal error's message is deliberately vague, but the
// worker's exit status, the converter's output and the paths involved are what
// the operator needs.
func (s *Service) fail(ctx context.Context, err error) error {
	st := statusOf(err)
	level := slog.LevelInfo
	if st.Code() == codes.Internal || st.Code() == codes.Unknown {
		level = slog.LevelError
	}
	s.logger.Log(ctx, level, "render job failed",
		slog.String("code", st.Code().String()),
		slog.Any("error", err),
	)
	return st.Err()
}

// statusOf is the one place a typed error becomes a gRPC status. The mapping
// follows the "Maps to" note each errors.go carries; the request-detail
// messages of the proto are attached where the caller can act on the numbers.
func statusOf(err error) *status.Status {
	// A failure a worker reported crosses its stdio protocol as a class rather
	// than as the error itself, so restoring the error the pipeline raised is
	// what lets one table below map it whichever side of the pipe it came from.
	err = workererr.Restore(err)
	if carrier, ok := errors.AsType[statusCarrier](err); ok {
		return carrier.GRPCStatus()
	}
	if st, ok := inputStatus(err); ok {
		return st
	}
	if st, ok := outputStatus(err); ok {
		return st
	}
	if st, ok := workerStatus(err); ok {
		return st
	}
	if st, ok := renderStatus(err); ok {
		return st
	}
	// A context error that reached here without a worker in between — a client
	// that hung up while its input was being fetched, or a deadline that passed
	// waiting for a worker slot.
	if errors.Is(err, context.Canceled) {
		return status.New(codes.Canceled, "the render was canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.New(codes.DeadlineExceeded, "the render ran out of time")
	}
	return status.New(codes.Internal, "rendering the document failed")
}

// inputStatus maps the failures of pkg/input.
func inputStatus(err error) (*status.Status, bool) {
	if e, ok := errors.AsType[*input.LimitExceededError](err); ok {
		return detailed(
			codes.ResourceExhausted,
			fmt.Sprintf("the input document exceeds the %d byte limit", e.Limit),
			&gahakuv1.InputSizeLimitExceeded{
				LimitBytes:    uint64(e.Limit),
				ReceivedBytes: uint64(e.Received),
			},
		), true
	}
	if errors.Is(err, input.ErrLocalInputDisabled) {
		return status.New(codes.PermissionDenied, "local path input is disabled"), true
	}
	if _, ok := errors.AsType[*input.PathNotAllowedError](err); ok {
		// Without the path: which paths exist and which are readable is what
		// the allowlist is there not to tell a caller.
		return status.New(codes.PermissionDenied, "the local path is not allowed"), true
	}
	if e, ok := errors.AsType[*input.SchemeNotAllowedError](err); ok {
		return status.Newf(
			codes.InvalidArgument, "the source url scheme %q is not allowed", e.Scheme,
		), true
	}
	if _, ok := errors.AsType[*input.AddressBlockedError](err); ok {
		return status.New(
			codes.InvalidArgument, "the source url resolves to a blocked address",
		), true
	}
	if e, ok := errors.AsType[*input.FetchStatusError](err); ok {
		return httpStatus("fetching the source document", e.StatusCode), true
	}
	return nil, false
}

// outputStatus maps the failures of pkg/output.
func outputStatus(err error) (*status.Status, bool) {
	if e, ok := errors.AsType[*output.UrlCountMismatchError](err); ok {
		return detailed(
			codes.InvalidArgument,
			fmt.Sprintf(
				"%d presigned put urls for a %d page range",
				e.UrlCount, e.PageCount,
			),
			&gahakuv1.OutputUrlCountMismatch{
				UrlCount:  uint32(e.UrlCount),
				PageCount: uint32(e.PageCount),
			},
		), true
	}
	if e, ok := errors.AsType[*ShortDocumentError](err); ok {
		return detailed(
			codes.OutOfRange,
			fmt.Sprintf(
				"pages %d-%d were requested of a %d page document",
				e.FirstPage, e.LastPage, e.DocumentPages,
			),
			&gahakuv1.PageRangeOutOfRange{
				FirstPage: e.FirstPage,
				LastPage:  e.LastPage,
				PageCount: uint32(e.DocumentPages),
			},
		), true
	}
	if e, ok := errors.AsType[*output.ShortPageCountError](err); ok {
		return detailed(
			codes.OutOfRange,
			fmt.Sprintf("the document produced %d of %d requested pages", e.Got, e.Want),
			&gahakuv1.PageRangeOutOfRange{PageCount: uint32(e.Got)},
		), true
	}
	if e, ok := errors.AsType[*output.UploadStatusError](err); ok {
		return httpStatus(
			fmt.Sprintf("uploading page %d", e.PageIndex), e.StatusCode,
		), true
	}
	if e, ok := errors.AsType[*output.SchemeNotAllowedError](err); ok {
		return status.Newf(
			codes.InvalidArgument, "the destination url scheme %q is not allowed", e.Scheme,
		), true
	}
	if errors.Is(err, output.ErrPageIndexOutOfRange) {
		return status.New(codes.Internal, "rendering the document failed"), true
	}
	return nil, false
}

// workerStatus maps the failures of pkg/worker.
//
// An *ExitError is what is left once the classified failures have been restored
// to the errors the pipeline raised: a worker that broke rather than a document
// it refused, which is INTERNAL. So is a *worker.JobError of a class this build
// does not know, which falls past every table here.
func workerStatus(err error) (*status.Status, bool) {
	if e, ok := errors.AsType[*worker.KilledError](err); ok {
		if errors.Is(e.Cause, context.Canceled) {
			return status.New(codes.Canceled, "the render was canceled"), true
		}
		return status.Newf(
			codes.DeadlineExceeded, "the render did not finish within %s", e.Timeout,
		), true
	}
	if _, ok := errors.AsType[*worker.OOMKilledError](err); ok {
		return status.New(
			codes.ResourceExhausted, "rendering the document ran out of memory",
		), true
	}
	if _, ok := errors.AsType[*worker.ExitError](err); ok {
		return status.New(codes.Internal, "rendering the document failed"), true
	}
	if _, ok := errors.AsType[*worker.ProtocolError](err); ok {
		return status.New(codes.Internal, "rendering the document failed"), true
	}
	return nil, false
}

// renderStatus maps the failures of pkg/render and pkg/sniff — the ones about
// the document itself.
func renderStatus(err error) (*status.Status, bool) {
	if e, ok := errors.AsType[*render.UnsupportedFormatError](err); ok {
		return status.Newf(
			codes.InvalidArgument, "%s is not a supported output format", e.Format,
		), true
	}
	if e, ok := errors.AsType[*render.InvalidPageRangeError](err); ok {
		return status.Newf(
			codes.InvalidArgument,
			"the page range %d-%d ends before it starts",
			e.FirstPage, e.LastPage,
		), true
	}
	if e, ok := errors.AsType[*render.PageRangeOutOfRangeError](err); ok {
		return detailed(
			codes.OutOfRange,
			fmt.Sprintf(
				"the page range starts at page %d of a %d page document",
				e.FirstPage, e.DocumentPages,
			),
			&gahakuv1.PageRangeOutOfRange{
				FirstPage: uint32(e.FirstPage),
				PageCount: uint32(e.DocumentPages),
			},
		), true
	}
	if _, ok := errors.AsType[*render.DecodeError](err); ok {
		return status.New(
			codes.InvalidArgument, "the input document could not be decoded",
		), true
	}
	if _, ok := errors.AsType[*officejob.ConverterMissingError](err); ok {
		return status.New(
			codes.Unimplemented, "this deployment renders no office documents",
		), true
	}
	if _, ok := errors.AsType[*officejob.ConversionError](err); ok {
		// Without the converter's output, which names paths inside the job
		// directory and whatever LibreOffice felt like logging.
		return status.New(
			codes.InvalidArgument, "the office document could not be converted",
		), true
	}
	if e, ok := errors.AsType[*sniff.UnsupportedMediaTypeError](err); ok {
		return status.Newf(
			codes.InvalidArgument, "%s is not a document kind this service renders",
			e.MediaType,
		), true
	}
	if e, ok := errors.AsType[*KindMismatchError](err); ok {
		return status.Newf(
			codes.InvalidArgument, "the document is a %s, not a %s", e.Detected, e.Asserted,
		), true
	}
	return nil, false
}

// httpStatus classifies an answer from object storage. A 403 is far more often
// an expired presign than a wrong one, and that is something the caller fixes by
// presigning again, so it is a precondition rather than a bad argument.
func httpStatus(what string, code int) *status.Status {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return status.Newf(
			codes.FailedPrecondition,
			"%s was refused with status %d; the presigned url may have expired",
			what, code,
		)
	case code == http.StatusNotFound || code == http.StatusGone:
		return status.Newf(codes.NotFound, "%s returned status %d", what, code)
	case code >= 500:
		return status.Newf(codes.Unavailable, "%s returned status %d", what, code)
	default:
		return status.Newf(codes.InvalidArgument, "%s returned status %d", what, code)
	}
}

// detailed attaches a request-detail message to a status, falling back to the
// bare status when the detail will not marshal.
func detailed(code codes.Code, message string, detail proto.Message) *status.Status {
	st := status.New(code, message)
	withDetail, err := st.WithDetails(protoadapt.MessageV1Of(detail))
	if err != nil {
		return st
	}
	return withDetail
}
