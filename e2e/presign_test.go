package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
)

// objectStore stands in for the object storage a caller presigns against: it
// serves what a test put in it and keeps what a render uploads. The urls carry
// no signature — what is under test here is gahaku's half of the exchange, the
// plain GET and PUT it performs; versitygw_test.go runs the same round trip
// against real presigned urls.
type objectStore struct {
	url string

	mu      sync.Mutex
	objects map[string][]byte
}

func newObjectStore(t *testing.T) *objectStore {
	t.Helper()
	store := &objectStore{objects: map[string][]byte{}}
	srv := httptest.NewServer(store)
	t.Cleanup(srv.Close)
	store.url = srv.URL
	return store
}

func (s *objectStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		object, ok := s.get(r.URL.Path)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(object)
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.put(r.URL.Path, body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *objectStore) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	return object, ok
}

func (s *objectStore) put(key string, object []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = object
}

// require returns the object at key, failing the test when the render never
// wrote it.
func (s *objectStore) require(t *testing.T, key string) []byte {
	t.Helper()
	object, ok := s.get(key)
	if !ok {
		t.Fatalf("nothing was stored at %s", key)
	}
	return object
}

// startUrlServer runs a server that may talk to the loopback addresses these
// tests' object stores listen on: an httptest server is plain http, and the
// deny-by-default address policy would otherwise refuse it.
func startUrlServer(t *testing.T) *gahakuServer {
	t.Helper()
	return startServer(t, "--allow-http", "--block-private-addresses=false")
}

// Bytes in, urls out: the pages never travel back down the stream, only the
// completions do, and the pages themselves land in the store in range order.
func TestRenderUploadsPagesToPresignedPutUrls(t *testing.T) {
	store := newObjectStore(t)
	client := startUrlServer(t).client(t)

	header := putOutput(byteStreamHeader(), store.url+"/page-0", store.url+"/page-1")
	header.Options = &gahakuv1.RenderOptions{
		Dpi:   72,
		Pages: &gahakuv1.PageRange{FirstPage: 1, LastPage: 2},
	}
	responses, err := exchange(t, client.RenderPdf,
		streamRequests(header, buildPDF(pdfFixture))...)
	if err != nil {
		t.Fatalf("RenderPdf: %v", err)
	}

	uploaded := uploadedPages(t, responses)
	if len(uploaded) != 2 {
		t.Fatalf("want a completion per page, got %d", len(uploaded))
	}
	for i, page := range uploaded {
		if page.GetPageIndex() != uint32(i) {
			t.Errorf("completion %d reports page %d", i, page.GetPageIndex())
		}
		object := store.require(t, fmt.Sprintf("/page-%d", i))
		if page.GetSizeBytes() != uint64(len(object)) {
			t.Errorf("page %d reports %d bytes, %d were stored",
				i, page.GetSizeBytes(), len(object))
		}
		// At 72 dpi a page is as many pixels as it is points wide, so the width
		// says which document page landed at this url.
		decoded := decodePage(t, object)
		assertPixels(t, "stored page width", decoded.config.Width, pdfFixture[i].width, 72)
	}
}

// A url in, bytes out: the server fetches the document itself and the stream
// carries nothing but the header on the way up.
func TestRenderFetchesAPresignedSourceUrl(t *testing.T) {
	store := newObjectStore(t)
	store.put("/input.pdf", buildPDF(pdfFixture))
	client := startUrlServer(t).client(t)

	header := urlSource(byteStreamHeader(), store.url+"/input.pdf")
	header.Options = &gahakuv1.RenderOptions{Dpi: 72}
	responses, err := exchange(t, client.RenderPdf, headerRequest(header))
	if err != nil {
		t.Fatalf("RenderPdf: %v", err)
	}

	pages := decodePages(t, pageBytes(t, responses))
	if len(pages) != len(pdfFixture) {
		t.Fatalf("rendered %d pages, want the document's %d", len(pages), len(pdfFixture))
	}
	for i, source := range pdfFixture {
		assertPixels(t, "page width", pages[i].config.Width, source.width, 72)
	}
}

// The page range is explicit: an open-ended one is closed to the url count
// instead of being refused, so only a stated range can disagree with it.
func TestRenderRejectsAUrlCountThatMissesThePageRange(t *testing.T) {
	store := newObjectStore(t)
	client := startUrlServer(t).client(t)

	header := putOutput(byteStreamHeader(), store.url+"/page-0")
	header.Options = &gahakuv1.RenderOptions{
		Dpi:   72,
		Pages: &gahakuv1.PageRange{FirstPage: 1, LastPage: 2},
	}
	_, err := exchange(t, client.RenderPdf, streamRequests(header, buildPDF(pdfFixture))...)

	requireCode(t, err, codes.InvalidArgument)
	detail := detailOf[*gahakuv1.OutputUrlCountMismatch](t, err)
	if detail.GetUrlCount() != 1 || detail.GetPageCount() != 2 {
		t.Fatalf("want 1 url for 2 pages, got %+v", detail)
	}
	if _, ok := store.get("/page-0"); ok {
		t.Error("a page was uploaded although the request was refused")
	}
}
