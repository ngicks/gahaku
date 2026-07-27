package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/worker"
)

// putRecorder stands in for object storage: it records the body of every PUT
// and answers with the status the test asked for.
type putRecorder struct {
	mu       sync.Mutex
	uploads  map[string]string
	status   int
	requests int
}

func (p *putRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	if p.status != 0 {
		w.WriteHeader(p.status)
		return
	}
	p.uploads[r.URL.Path] = string(body)
}

func (p *putRecorder) recorded(path string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.uploads[path]
}

func newPutServer(t *testing.T, status int) (*putRecorder, *httptest.Server) {
	t.Helper()
	rec := &putRecorder{uploads: map[string]string{}, status: status}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return rec, srv
}

// putHeader asks for a render uploaded to urls, over pages 1..len(urls) unless
// the caller sets a range of its own.
func putHeader(urls ...string) *gahakuv1.RenderRequestHeader {
	header := byteStreamHeader()
	header.Output = &gahakuv1.OutputSpec{
		Kind: &gahakuv1.OutputSpec_PresignedPut{
			PresignedPut: &gahakuv1.PresignedPutOutput{Urls: urls},
		},
	}
	return header
}

func TestRenderUploadsPagesToTheirPresignedUrls(t *testing.T) {
	rec, srv := newPutServer(t, 0)
	runner := &fakeRunner{render: renderPages(2, "first", "second page")}
	client := newClient(t, Config{
		Input:  input.Config{AllowHttp: true},
		Runner: runner,
	})

	header := putHeader(srv.URL+"/page-0", srv.URL+"/page-1")
	responses, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pdfDocument))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var uploaded []uint32
	for _, resp := range responses {
		page := resp.GetPageUploaded()
		if page == nil {
			t.Fatalf("want a page upload, got %v", resp)
		}
		uploaded = append(uploaded, page.GetPageIndex())
	}
	if want := []uint32{0, 1}; !slices.Equal(uploaded, want) {
		t.Fatalf("want uploads for pages %v, got %v", want, uploaded)
	}
	if size := responses[1].GetPageUploaded().GetSizeBytes(); size != uint64(len("second page")) {
		t.Fatalf("want the uploaded size, got %d", size)
	}
	if got := rec.recorded("/page-0"); got != "first" {
		t.Fatalf("want the first page at its url, got %q", got)
	}
	if got := rec.recorded("/page-1"); got != "second page" {
		t.Fatalf("want the second page at its url, got %q", got)
	}
}

func TestRenderRejectsAUrlCountThatMissesThePageRange(t *testing.T) {
	runner := &fakeRunner{render: renderPages(2, "a", "b")}
	client := newClient(t, Config{Input: input.Config{AllowHttp: true}, Runner: runner})

	header := putHeader("http://storage.invalid/page-0")
	header.Options = &gahakuv1.RenderOptions{
		Pages: &gahakuv1.PageRange{FirstPage: 1, LastPage: 2},
	}
	_, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pdfDocument))

	requireCode(t, err, codes.InvalidArgument)
	detail := detailOf[*gahakuv1.OutputUrlCountMismatch](t, err)
	if detail.GetUrlCount() != 1 || detail.GetPageCount() != 2 {
		t.Fatalf("want 1 url for 2 pages, got %+v", detail)
	}
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderRejectsAnEmptyUrlList(t *testing.T) {
	runner := &fakeRunner{}
	client := newClient(t, Config{Runner: runner})

	_, err := exchange(t, client.Render, headerRequest(putHeader()), chunkRequest(pdfDocument))

	requireCode(t, err, codes.InvalidArgument)
	detailOf[*gahakuv1.OutputUrlCountMismatch](t, err)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderClosesAnOpenPageRangeToTheUrlCount(t *testing.T) {
	_, srv := newPutServer(t, 0)
	runner := &fakeRunner{render: renderPages(9, "a", "b")}
	client := newClient(t, Config{Input: input.Config{AllowHttp: true}, Runner: runner})

	header := putHeader(srv.URL+"/page-0", srv.URL+"/page-1")
	if _, err := exchange(
		t,
		client.Render,
		headerRequest(header),
		chunkRequest(pdfDocument),
	); err != nil {
		t.Fatalf("Render: %v", err)
	}

	calls := runner.recorded()
	if len(calls) != 1 {
		t.Fatalf("want one worker, got %d", len(calls))
	}
	// Without this the worker would render all nine pages of the document and
	// the last seven would have nowhere to go.
	want := worker.PageRange{FirstPage: 1, LastPage: 2}
	if got := calls[0].spec.Options.Pages; got != want {
		t.Fatalf("want the range closed to %+v, got %+v", want, got)
	}
}

func TestRenderReportsADocumentShorterThanTheRange(t *testing.T) {
	_, srv := newPutServer(t, 0)
	runner := &fakeRunner{render: renderPages(1, "only page")}
	client := newClient(t, Config{Input: input.Config{AllowHttp: true}, Runner: runner})

	header := putHeader(srv.URL+"/page-0", srv.URL+"/page-1", srv.URL+"/page-2")
	_, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pdfDocument))

	requireCode(t, err, codes.OutOfRange)
	detail := detailOf[*gahakuv1.PageRangeOutOfRange](t, err)
	if detail.GetFirstPage() != 1 || detail.GetLastPage() != 3 || detail.GetPageCount() != 1 {
		t.Fatalf("want pages 1-3 of a 1 page document, got %+v", detail)
	}
}

func TestRenderMapsAnUploadRejection(t *testing.T) {
	_, srv := newPutServer(t, http.StatusForbidden)
	runner := &fakeRunner{render: renderPages(1, "page")}
	client := newClient(t, Config{Input: input.Config{AllowHttp: true}, Runner: runner})

	header := putHeader(srv.URL + "/page-0")
	_, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pdfDocument))

	// A 403 is what an expired presign answers, which the caller fixes by
	// presigning again rather than by changing its request.
	requireCode(t, err, codes.FailedPrecondition)
}

func TestRenderRefusesPlainHttpDestinationsByDefault(t *testing.T) {
	runner := &fakeRunner{render: renderPages(1, "page")}
	client := newClient(t, Config{Runner: runner})

	header := putHeader("http://storage.invalid/page-0")
	_, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pdfDocument))

	requireCode(t, err, codes.InvalidArgument)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}
