package sniff

import "fmt"

// UnsupportedMediaTypeError reports a document no pipeline here renders —
// a format none of the backends handles, or bytes detection could not place at
// all, which arrive as application/octet-stream. Maps to INVALID_ARGUMENT, or
// to UNIMPLEMENTED where the server would rather say "this format, not yet";
// MediaType is what it decides on.
type UnsupportedMediaTypeError struct {
	MediaType string
}

func (e *UnsupportedMediaTypeError) Error() string {
	return fmt.Sprintf("sniff: %q is not a document kind this service renders", e.MediaType)
}
