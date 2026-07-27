package output

import (
	"errors"
	"fmt"
)

// ErrPageIndexOutOfRange reports a page offered to a sink that has no
// destination for it, i.e. the document produced more pages than the requested
// range covers. Maps to INTERNAL: pkg/server clamps the range before
// rendering.
var ErrPageIndexOutOfRange = errors.New("output: page index outside the requested page range")

// UrlCountMismatchError reports a presigned PUT url list whose length differs
// from the requested page-range length. Maps to INVALID_ARGUMENT with an
// OutputUrlCountMismatch detail.
type UrlCountMismatchError struct {
	UrlCount  int
	PageCount int
}

func (e *UrlCountMismatchError) Error() string {
	return fmt.Sprintf(
		"output: %d presigned put urls for a %d page range",
		e.UrlCount, e.PageCount,
	)
}

// ShortPageCountError reports a document that yielded fewer pages than the
// requested range, leaving destination urls unwritten. Maps to OUT_OF_RANGE
// with a PageRangeOutOfRange detail.
type ShortPageCountError struct {
	// Want is the number of destination urls, i.e. the page-range length.
	Want int
	// Got is the number of pages the document actually produced.
	Got int
}

func (e *ShortPageCountError) Error() string {
	return fmt.Sprintf("output: document produced %d of %d requested pages", e.Got, e.Want)
}

// UploadStatusError reports a non-2xx answer to a page upload. 403 most often
// means the presign expired, which pkg/server maps to FAILED_PRECONDITION.
type UploadStatusError struct {
	PageIndex  int
	StatusCode int
}

func (e *UploadStatusError) Error() string {
	return fmt.Sprintf("output: uploading page %d returned status %d", e.PageIndex, e.StatusCode)
}

// SchemeNotAllowedError reports a destination url whose scheme the sink
// refuses: anything but https, or http when it is not opted in. Maps to
// INVALID_ARGUMENT.
type SchemeNotAllowedError struct {
	Scheme string
}

func (e *SchemeNotAllowedError) Error() string {
	return fmt.Sprintf("output: url scheme %q is not allowed", e.Scheme)
}
