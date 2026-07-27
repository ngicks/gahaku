package render

import (
	"fmt"

	"github.com/ngicks/gahaku/pkg/worker"
)

// NormalizePageRange resolves p against a document of documentPages pages
// into the inclusive, 1-based first and last page to render. The open ends
// of a worker.PageRange close here: a zero first page means the document's
// first, a zero last page means its last.
//
// A range running past the end is clamped instead of refused — the worker
// reports the document's page count back, which is what lets pkg/server tell
// a short render from a range that selected nothing (D-01). A range starting
// past the end selects nothing at all and is refused.
func NormalizePageRange(p worker.PageRange, documentPages int) (first, last int, err error) {
	if documentPages <= 0 {
		return 0, 0, fmt.Errorf("render: document has no pages to render")
	}
	first = int(p.FirstPage)
	if first == 0 {
		first = 1
	}
	// Ordering is checked against the requested last page, not the resolved
	// one: "from page 2 to the end" of a one-page document is out of range,
	// while an explicit 5-2 is malformed however long the document is.
	if p.LastPage != 0 && int(p.LastPage) < first {
		return 0, 0, &InvalidPageRangeError{FirstPage: first, LastPage: int(p.LastPage)}
	}
	if first > documentPages {
		return 0, 0, &PageRangeOutOfRangeError{FirstPage: first, DocumentPages: documentPages}
	}
	last = documentPages
	if p.LastPage != 0 {
		last = min(int(p.LastPage), documentPages)
	}
	return first, last, nil
}
