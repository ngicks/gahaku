// Package workererr carries a render failure across the worker's stdio
// protocol without losing what kind of failure it was.
//
// A worker frames the failure it stopped on as a class plus whatever fields
// that class carries, and reads neither (pkg/worker), so the translation lives
// here: this is the one package that can import both halves of what a class
// means — the render pipelines that raise the errors, and the protocol they
// cross. Classify runs in the worker subprocess and Restore in the
// orchestrator; they are one table read in both directions, which is what keeps
// them from drifting apart.
//
// pkg/server maps what Restore hands back, so a document a pipeline refused
// gets the status it earned whether it was rendered in process or in a worker.
package workererr

import (
	"encoding/json"
	"errors"

	"github.com/ngicks/gahaku/pkg/render"
	"github.com/ngicks/gahaku/pkg/render/officejob"
	"github.com/ngicks/gahaku/pkg/worker"
)

// The classes that cross the protocol. They are an agreement between the two
// ends of one gahaku binary rather than a published contract: a worker is the
// same executable as the orchestrator that spawned it (D-08), so no caller ever
// sees one and no build ever meets a class from another.
const (
	CodeDecode              = "render.decode"
	CodeUnsupportedFormat   = "render.unsupported_format"
	CodeInvalidPageRange    = "render.invalid_page_range"
	CodePageRangeOutOfRange = "render.page_range_out_of_range"
	CodeConverterMissing    = "officejob.converter_missing"
	CodeConversion          = "officejob.conversion"
)

// details is the structured half of a class: the numbers pkg/server reads back
// out of a failure to say what the caller got wrong. One struct for every
// class, since a class leaves out what it does not set.
type details struct {
	Format        string `json:"format,omitzero"`
	FirstPage     int    `json:"firstPage,omitzero"`
	LastPage      int    `json:"lastPage,omitzero"`
	DocumentPages int    `json:"documentPages,omitzero"`
}

// Classify names the class err crosses the protocol as, and returns nil for a
// failure no class covers — a worker that broke rather than a document it
// refused, which stays an opaque *worker.ExitError and an INTERNAL status.
//
// It is a worker.Classifier: the gahaku package hands it to worker.Handle.
func Classify(err error) *worker.JobError {
	if _, ok := errors.AsType[*render.DecodeError](err); ok {
		return classOf(CodeDecode, err, details{})
	}
	if e, ok := errors.AsType[*render.UnsupportedFormatError](err); ok {
		return classOf(CodeUnsupportedFormat, err, details{Format: string(e.Format)})
	}
	if e, ok := errors.AsType[*render.InvalidPageRangeError](err); ok {
		return classOf(CodeInvalidPageRange, err, details{
			FirstPage: e.FirstPage,
			LastPage:  e.LastPage,
		})
	}
	if e, ok := errors.AsType[*render.PageRangeOutOfRangeError](err); ok {
		return classOf(CodePageRangeOutOfRange, err, details{
			FirstPage:     e.FirstPage,
			DocumentPages: e.DocumentPages,
		})
	}
	if _, ok := errors.AsType[*officejob.ConverterMissingError](err); ok {
		return classOf(CodeConverterMissing, err, details{})
	}
	if _, ok := errors.AsType[*officejob.ConversionError](err); ok {
		return classOf(CodeConversion, err, details{})
	}
	return nil
}

// Restore turns the class a worker reported back into the typed error the
// pipeline raised, so pkg/server's table maps a failure from a subprocess by
// the same branch it maps that failure in process. Anything else — an
// unclassified worker failure, an error that never crossed a pipe — comes back
// unchanged, and so keeps whatever status it already had.
//
// What comes back is the failure's identity, not its account of itself: only
// the fields the status mapping reads cross, and each restored error carries
// the whole reported message as its cause rather than the fields it was built
// from. The readable failure stays on the *worker.JobError, which is what the
// server logs beside the status.
func Restore(err error) error {
	jobErr, ok := errors.AsType[*worker.JobError](err)
	if !ok {
		return err
	}
	var d details
	if len(jobErr.Details) > 0 {
		if unmarshalErr := json.Unmarshal(jobErr.Details, &d); unmarshalErr != nil {
			return err
		}
	}
	reported := errors.New(jobErr.Message)
	switch jobErr.Code {
	case CodeDecode:
		return &render.DecodeError{Err: reported}
	case CodeUnsupportedFormat:
		return &render.UnsupportedFormatError{Format: worker.Format(d.Format)}
	case CodeInvalidPageRange:
		return &render.InvalidPageRangeError{FirstPage: d.FirstPage, LastPage: d.LastPage}
	case CodePageRangeOutOfRange:
		return &render.PageRangeOutOfRangeError{
			FirstPage:     d.FirstPage,
			DocumentPages: d.DocumentPages,
		}
	case CodeConverterMissing:
		return &officejob.ConverterMissingError{Err: reported}
	case CodeConversion:
		return &officejob.ConversionError{Err: reported}
	default:
		return err
	}
}

// classOf builds what crosses the pipe. Fields that will not marshal — which a
// struct of strings and ints cannot produce — are dropped rather than dropping
// the class with them: the class alone still says what kind of failure it was.
func classOf(code string, err error, d details) *worker.JobError {
	jobErr := &worker.JobError{Code: code, Message: err.Error()}
	if d == (details{}) {
		return jobErr
	}
	encoded, marshalErr := json.Marshal(d)
	if marshalErr != nil {
		return jobErr
	}
	jobErr.Details = encoded
	return jobErr
}
