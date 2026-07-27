package officejob

import "fmt"

// ConverterMissingError reports that the soffice binary is not there to run,
// so this deployment converts no Office document at all.
//
// Maps to UNIMPLEMENTED: an image without LibreOffice does not support the
// Office kind, which is something a caller can act on by sending PDF or images
// instead. FAILED_PRECONDITION would suggest a state that could be brought
// about, and no caller can install LibreOffice into the server.
type ConverterMissingError struct {
	// Binary is the executable that was looked for.
	Binary string
	// Err is what the PATH lookup reported.
	Err error
}

func (e *ConverterMissingError) Error() string {
	return fmt.Sprintf("officejob: no LibreOffice to run as %q: %s", e.Binary, e.Err)
}

func (e *ConverterMissingError) Unwrap() error { return e.Err }

// ConversionError reports a soffice run that produced no PDF, either by
// failing outright or by exiting cleanly and writing nothing.
//
// Maps to INVALID_ARGUMENT. A document soffice cannot load is by far the
// likeliest cause and it arrives as both shapes — current LibreOffice exits
// nonzero on an unloadable source, older ones exit 0 having converted nothing.
// A broken installation looks the same from here, which is why Output travels
// with the error: the operator reading it can tell the two apart, the caller
// cannot.
type ConversionError struct {
	// ExitCode is soffice's exit status, 0 when it exited fine and still left
	// no document behind.
	ExitCode int
	// Path is the document soffice was expected to leave behind, set when it
	// exited cleanly without one. soffice names what it wrote in its own
	// output, so having both makes a naming this package guessed wrong
	// readable as what it is.
	Path string
	// Output is the bounded head of what soffice logged. Both of its streams
	// land here because which one carries the failure differs between
	// versions.
	Output string
	// Err is the process failure, nil when soffice exited 0 without producing
	// a document.
	Err error
}

func (e *ConversionError) Error() string {
	what := fmt.Sprintf("soffice produced no pdf at %q", e.Path)
	if e.Err != nil {
		what = fmt.Sprintf("soffice failed: %s", e.Err)
	}
	if e.Output == "" {
		return "officejob: " + what
	}
	return fmt.Sprintf("officejob: %s; output: %s", what, e.Output)
}

func (e *ConversionError) Unwrap() error { return e.Err }
