package render

import (
	"fmt"

	"github.com/ngicks/gahaku/pkg/worker"
)

// UnsupportedFormatError reports a requested output format no encoder here
// produces. Maps to INVALID_ARGUMENT.
type UnsupportedFormatError struct {
	Format worker.Format
}

func (e *UnsupportedFormatError) Error() string {
	return fmt.Sprintf("render: %q is not a supported output format", e.Format)
}

// InvalidPageRangeError reports a page range whose last page precedes its
// first, which selects nothing whatever the document is. Maps to
// INVALID_ARGUMENT.
type InvalidPageRangeError struct {
	FirstPage int
	LastPage  int
}

func (e *InvalidPageRangeError) Error() string {
	return fmt.Sprintf(
		"render: page range %d-%d ends before it starts",
		e.FirstPage, e.LastPage,
	)
}

// PageRangeOutOfRangeError reports a page range starting past the end of the
// document, so no page of it exists to render. A range that merely runs past
// the end is clamped instead; this is the case where nothing is left. Maps to
// OUT_OF_RANGE with a PageRangeOutOfRange detail (D-01).
type PageRangeOutOfRangeError struct {
	FirstPage     int
	DocumentPages int
}

func (e *PageRangeOutOfRangeError) Error() string {
	return fmt.Sprintf(
		"render: page range starts at page %d of a %d page document",
		e.FirstPage, e.DocumentPages,
	)
}

// DecodeError reports input the pipeline could not read as the kind of
// document it was routed to — a truncated file, a codec with no decoder
// here, or bytes that are not the format they claim. Maps to
// INVALID_ARGUMENT.
type DecodeError struct {
	Err error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("render: decoding input document: %s", e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }
