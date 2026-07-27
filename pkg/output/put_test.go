package output

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// putRecorder stands in for object storage: it records every PUT and answers
// with the statuses the test queued.
type putRecorder struct {
	mu       sync.Mutex
	requests []putRequest
	statuses []int
}

type putRequest struct {
	path          string
	body          string
	contentType   string
	contentLength int64
}

func (p *putRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, putRequest{
		path:          r.URL.Path,
		body:          string(body),
		contentType:   r.Header.Get("Content-Type"),
		contentLength: r.ContentLength,
	})
	status := http.StatusOK
	if len(p.statuses) > 0 {
		status = p.statuses[0]
		if len(p.statuses) > 1 {
			p.statuses = p.statuses[1:]
		}
	}
	w.WriteHeader(status)
}

func (p *putRecorder) recorded() []putRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]putRequest(nil), p.requests...)
}

func newPutServer(t *testing.T, statuses ...int) (*putRecorder, *httptest.Server) {
	t.Helper()
	rec := &putRecorder{statuses: statuses}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return rec, srv
}

func TestValidateUrlCount(t *testing.T) {
	for _, tt := range []struct {
		name      string
		urls      []string
		pageCount int
		wantErr   bool
	}{
		{name: "one url per page", urls: []string{"a", "b"}, pageCount: 2},
		{name: "too few urls", urls: []string{"a"}, pageCount: 2, wantErr: true},
		{name: "too many urls", urls: []string{"a", "b", "c"}, pageCount: 2, wantErr: true},
		{name: "no urls", urls: nil, pageCount: 2, wantErr: true},
		{name: "open page range accepts any count", urls: []string{"a"}, pageCount: 0},
		{name: "open page range still needs a url", urls: nil, pageCount: 0, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUrlCount(tt.urls, tt.pageCount)
			mismatch, ok := errors.AsType[*UrlCountMismatchError](err)
			switch {
			case tt.wantErr && !ok:
				t.Fatalf("err = %v, want *UrlCountMismatchError", err)
			case !tt.wantErr && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case !tt.wantErr:
				return
			}
			if mismatch.UrlCount != len(tt.urls) || mismatch.PageCount != tt.pageCount {
				t.Errorf(
					"error = %+v, want url count %d page count %d",
					mismatch, len(tt.urls), tt.pageCount,
				)
			}
		})
	}
}

func TestNewPutSinkValidatesUpFront(t *testing.T) {
	t.Run("url count mismatch", func(t *testing.T) {
		_, err := NewPutSink(PutSinkConfig{
			Urls:      []string{"https://example.test/0"},
			PageCount: 2,
		})
		if _, ok := errors.AsType[*UrlCountMismatchError](err); !ok {
			t.Fatalf("err = %v, want *UrlCountMismatchError", err)
		}
	})

	t.Run("plain http is rejected by default", func(t *testing.T) {
		_, err := NewPutSink(PutSinkConfig{Urls: []string{"http://example.test/0"}})
		if _, ok := errors.AsType[*SchemeNotAllowedError](err); !ok {
			t.Fatalf("err = %v, want *SchemeNotAllowedError", err)
		}
	})

	t.Run("plain http is accepted when opted in", func(t *testing.T) {
		if _, err := NewPutSink(PutSinkConfig{
			Urls:      []string{"http://example.test/0"},
			AllowHttp: true,
		}); err != nil {
			t.Fatalf("NewPutSink: %v", err)
		}
	})
}

func TestPutSinkUploadsPageToItsOwnUrl(t *testing.T) {
	rec, srv := newPutServer(t)
	pages := []string{"page zero", "page one"}

	var uploaded []PageUploaded
	sink, err := NewPutSink(PutSinkConfig{
		Urls:        []string{srv.URL + "/0.png", srv.URL + "/1.png"},
		PageCount:   len(pages),
		AllowHttp:   true,
		ContentType: "image/png",
		Notify: func(u PageUploaded) error {
			uploaded = append(uploaded, u)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewPutSink: %v", err)
	}

	for i, page := range pages {
		if err := sink.WritePage(t.Context(), i, strings.NewReader(page)); err != nil {
			t.Fatalf("WritePage(%d): %v", i, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := rec.recorded()
	if len(got) != len(pages) {
		t.Fatalf("requests = %d, want %d", len(got), len(pages))
	}
	for i, req := range got {
		wantPath := []string{"/0.png", "/1.png"}[i]
		if req.path != wantPath || req.body != pages[i] {
			t.Errorf("request %d = %s %q, want %s %q", i, req.path, req.body, wantPath, pages[i])
		}
		if req.contentType != "image/png" {
			t.Errorf("request %d content type = %q, want image/png", i, req.contentType)
		}
		if req.contentLength != int64(len(pages[i])) {
			t.Errorf("request %d length = %d, want %d", i, req.contentLength, len(pages[i]))
		}
	}
	for i, u := range uploaded {
		if u.PageIndex != i || u.SizeBytes != int64(len(pages[i])) {
			t.Errorf("upload %d = %+v, want index %d size %d", i, u, i, len(pages[i]))
		}
	}
	if len(uploaded) != len(pages) {
		t.Errorf("completions = %d, want %d", len(uploaded), len(pages))
	}
}

func TestPutSinkRetriesServerErrors(t *testing.T) {
	rec, srv := newPutServer(
		t,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusOK,
	)

	sink := newTestSink(t, srv.URL+"/0.png", 3)
	if err := sink.WritePage(t.Context(), 0, strings.NewReader("page")); err != nil {
		t.Fatalf("WritePage: %v", err)
	}

	got := rec.recorded()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3", len(got))
	}
	for i, req := range got {
		// Every retry must resend the whole page, which only holds if the sink
		// rewinds between attempts.
		if req.body != "page" {
			t.Errorf("attempt %d body = %q, want %q", i, req.body, "page")
		}
	}
}

func TestPutSinkGivesUpAfterMaxAttempts(t *testing.T) {
	rec, srv := newPutServer(t, http.StatusInternalServerError)

	sink := newTestSink(t, srv.URL+"/0.png", 2)
	err := sink.WritePage(t.Context(), 0, strings.NewReader("page"))
	statusErr, ok := errors.AsType[*UploadStatusError](err)
	if !ok {
		t.Fatalf("err = %v, want *UploadStatusError", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", statusErr.StatusCode)
	}
	if got := len(rec.recorded()); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
	// The upload error is the failure to report; Close must not relabel it as
	// a short document.
	if closeErr := sink.Close(); closeErr != nil {
		t.Errorf("Close err = %v, want nil after a failed upload", closeErr)
	}
}

func TestPutSinkDoesNotRetryForbidden(t *testing.T) {
	rec, srv := newPutServer(t, http.StatusForbidden)

	sink := newTestSink(t, srv.URL+"/0.png", 3)
	err := sink.WritePage(t.Context(), 0, strings.NewReader("page"))
	statusErr, ok := errors.AsType[*UploadStatusError](err)
	if !ok {
		t.Fatalf("err = %v, want *UploadStatusError", err)
	}
	if statusErr.StatusCode != http.StatusForbidden || statusErr.PageIndex != 0 {
		t.Errorf("error = %+v, want page 0 status 403", statusErr)
	}
	// An expired presign never becomes valid again, so a 403 must not be
	// retried.
	if got := len(rec.recorded()); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestPutSinkCloseReportsMissingPages(t *testing.T) {
	_, srv := newPutServer(t)

	sink, err := NewPutSink(PutSinkConfig{
		Urls:      []string{srv.URL + "/0", srv.URL + "/1", srv.URL + "/2"},
		PageCount: 3,
		AllowHttp: true,
	})
	if err != nil {
		t.Fatalf("NewPutSink: %v", err)
	}
	for i := range 2 {
		if err := sink.WritePage(t.Context(), i, bytes.NewReader([]byte("page"))); err != nil {
			t.Fatalf("WritePage(%d): %v", i, err)
		}
	}

	shortErr, ok := errors.AsType[*ShortPageCountError](sink.Close())
	if !ok {
		t.Fatalf("Close err = %v, want *ShortPageCountError", sink.Close())
	}
	if shortErr.Want != 3 || shortErr.Got != 2 {
		t.Errorf("error = %+v, want want 3 got 2", shortErr)
	}
}

func TestPutSinkRejectsPageBeyondTheRange(t *testing.T) {
	_, srv := newPutServer(t)

	sink := newTestSink(t, srv.URL+"/0", 1)
	err := sink.WritePage(t.Context(), 1, strings.NewReader("page"))
	if !errors.Is(err, ErrPageIndexOutOfRange) {
		t.Fatalf("err = %v, want ErrPageIndexOutOfRange", err)
	}
}

func TestPutSinkDefaultContentType(t *testing.T) {
	rec, srv := newPutServer(t)

	sink := newTestSink(t, srv.URL+"/0", 1)
	if err := sink.WritePage(t.Context(), 0, strings.NewReader("page")); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	if got := rec.recorded()[0].contentType; got != DefaultContentType {
		t.Errorf("content type = %q, want %q", got, DefaultContentType)
	}
}

func newTestSink(t *testing.T, url string, maxAttempts int) *PutSink {
	t.Helper()
	sink, err := NewPutSink(PutSinkConfig{
		Urls:        []string{url},
		PageCount:   1,
		AllowHttp:   true,
		MaxAttempts: maxAttempts,
		Backoff:     time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewPutSink: %v", err)
	}
	return sink
}
