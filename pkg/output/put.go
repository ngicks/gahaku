package output

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultMaxAttempts counts the first try plus its retries.
	DefaultMaxAttempts = 3
	// DefaultBackoff is the pause before the second attempt; it doubles after
	// each further failure.
	DefaultBackoff     = 200 * time.Millisecond
	DefaultContentType = "application/octet-stream"
)

// PutSinkConfig configures a PutSink.
type PutSinkConfig struct {
	// Urls are the presigned PUT destinations, one per page of the requested
	// range, in order.
	Urls []string
	// PageCount is the requested page-range length. 0 leaves the range
	// open-ended ("the whole document"), which defers the count check to
	// Close.
	PageCount int
	// Notify receives one completion per uploaded page.
	Notify func(PageUploaded) error
	// ContentType is sent with every upload; empty selects
	// DefaultContentType.
	ContentType string
	// Client performs the uploads. A nil client selects http.DefaultClient, so
	// a caller wanting an address policy on the destinations injects its own.
	Client *http.Client
	// AllowHttp accepts plain http:// destinations; https-only otherwise.
	AllowHttp bool
	// MaxAttempts bounds the tries per page; 0 selects DefaultMaxAttempts.
	MaxAttempts int
	// Backoff is the pause before the second attempt; 0 selects
	// DefaultBackoff.
	Backoff time.Duration
}

// PutSink uploads each rendered page to its presigned PUT URL.
type PutSink struct {
	urls        []string
	written     []bool
	uploaded    int
	failed      bool
	notify      func(PageUploaded) error
	contentType string
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
}

// NewPutSink validates the destination list and returns the sink. It is the
// up-front check pkg/server runs while parsing the request header, so a bad
// output spec fails before a worker is spawned.
func NewPutSink(cfg PutSinkConfig) (*PutSink, error) {
	if err := ValidateUrlCount(cfg.Urls, cfg.PageCount); err != nil {
		return nil, err
	}
	for _, rawUrl := range cfg.Urls {
		if err := checkScheme(rawUrl, cfg.AllowHttp); err != nil {
			return nil, err
		}
	}

	sink := &PutSink{
		urls:        cfg.Urls,
		written:     make([]bool, len(cfg.Urls)),
		notify:      cfg.Notify,
		contentType: cmp.Or(cfg.ContentType, DefaultContentType),
		client:      cfg.Client,
		maxAttempts: cfg.MaxAttempts,
		backoff:     cfg.Backoff,
	}
	if sink.client == nil {
		sink.client = http.DefaultClient
	}
	if sink.maxAttempts <= 0 {
		sink.maxAttempts = DefaultMaxAttempts
	}
	if sink.backoff <= 0 {
		sink.backoff = DefaultBackoff
	}
	return sink, nil
}

// ValidateUrlCount checks a presigned PUT url list against the requested
// page-range length. A pageCount of 0 or less means the range was open-ended,
// so only an empty list can be rejected here; the rest waits for Close.
func ValidateUrlCount(urls []string, pageCount int) error {
	if len(urls) == 0 || (pageCount > 0 && len(urls) != pageCount) {
		return &UrlCountMismatchError{UrlCount: len(urls), PageCount: pageCount}
	}
	return nil
}

// WritePage uploads page to the destination at pageIndex and reports the
// completion through Notify.
func (s *PutSink) WritePage(ctx context.Context, pageIndex int, page io.ReadSeeker) error {
	err := s.writePage(ctx, pageIndex, page)
	if err != nil {
		s.failed = true
	}
	return err
}

func (s *PutSink) writePage(ctx context.Context, pageIndex int, page io.ReadSeeker) error {
	if pageIndex < 0 || pageIndex >= len(s.urls) {
		return fmt.Errorf(
			"output: page %d of %d urls: %w",
			pageIndex, len(s.urls), ErrPageIndexOutOfRange,
		)
	}
	size, err := pageSize(page)
	if err != nil {
		return fmt.Errorf("output: measuring page %d: %w", pageIndex, err)
	}
	if err := s.upload(ctx, pageIndex, page, size); err != nil {
		return err
	}
	if !s.written[pageIndex] {
		s.written[pageIndex] = true
		s.uploaded++
	}
	if s.notify == nil {
		return nil
	}
	if err := s.notify(PageUploaded{PageIndex: pageIndex, SizeBytes: size}); err != nil {
		return fmt.Errorf("output: reporting page %d: %w", pageIndex, err)
	}
	return nil
}

// Close reports whether every destination received a page. Fewer pages than
// urls means the document was shorter than the requested range.
//
// After a failed WritePage the page count says nothing about the document —
// the upload error is the failure worth reporting — so Close then stays
// silent.
func (s *PutSink) Close() error {
	if s.failed || s.uploaded == len(s.urls) {
		return nil
	}
	return &ShortPageCountError{Want: len(s.urls), Got: s.uploaded}
}

func (s *PutSink) upload(ctx context.Context, pageIndex int, page io.ReadSeeker, size int64) error {
	var lastErr error
	for attempt := 1; attempt <= s.maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, s.backoff<<(attempt-2)); err != nil {
				return err
			}
		}
		if _, err := page.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("output: rewinding page %d: %w", pageIndex, err)
		}
		err := s.put(ctx, pageIndex, page, size)
		if err == nil {
			return nil
		}
		if !retryable(err) || ctx.Err() != nil {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf(
		"output: page %d failed %d attempts: %w",
		pageIndex, s.maxAttempts, lastErr,
	)
}

func (s *PutSink) put(ctx context.Context, pageIndex int, page io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.urls[pageIndex], page)
	if err != nil {
		return fmt.Errorf("output: building upload request for page %d: %w", pageIndex, err)
	}
	// Presigned PUTs are signed for a known length, so the body must not be
	// sent chunked.
	req.ContentLength = size
	req.Header.Set("Content-Type", s.contentType)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("output: uploading page %d: %w", pageIndex, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return &UploadStatusError{PageIndex: pageIndex, StatusCode: resp.StatusCode}
	}
	return nil
}

// retryable keeps a rejected presign (403) or any other client error from
// being tried again: only transport failures and server-side refusals can
// succeed later.
func retryable(err error) bool {
	statusErr, ok := errors.AsType[*UploadStatusError](err)
	if !ok {
		return true
	}
	return statusErr.StatusCode >= 500 || statusErr.StatusCode == http.StatusTooManyRequests
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// drainClose lets the transport reuse the connection instead of tearing it
// down when a response body is abandoned early.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	_ = body.Close()
}
