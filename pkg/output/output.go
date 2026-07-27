// Package output delivers rendered pages to the destination the request asked
// for.
//
// Two sinks share one shape — WritePage per page in page-range order, then
// Close: ChunkSink frames a page into (page index, chunk, last) messages for
// the response stream, PutSink uploads a page to its presigned PUT URL.
// pkg/server picks one from the request's output spec and can hold either
// behind its own interface.
//
// Nothing here knows about protobuf or gRPC; failures are typed errors that
// pkg/server maps to status codes. Neither sink is safe for concurrent use:
// pages go out one at a time, matching the single response stream they feed.
package output

import (
	"io"
	"net/url"
)

// PageChunk is one framed slice of a rendered page.
type PageChunk struct {
	// PageIndex is the 0-based position within the requested page range.
	PageIndex int
	Chunk     []byte
	// Last marks the final chunk of this page.
	Last bool
}

// PageUploaded reports a finished page upload.
type PageUploaded struct {
	// PageIndex is the 0-based position within the requested page range, which
	// is also the index into the destination urls.
	PageIndex int
	SizeBytes int64
}

// pageSize measures page and leaves it rewound to the start, so a page can be
// framed or re-uploaded from a known length.
func pageSize(page io.ReadSeeker) (int64, error) {
	size, err := page.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := page.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	return size, nil
}

func checkScheme(rawUrl string, allowHttp bool) error {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return &SchemeNotAllowedError{Scheme: ""}
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if allowHttp {
			return nil
		}
	}
	return &SchemeNotAllowedError{Scheme: parsed.Scheme}
}
