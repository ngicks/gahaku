package input

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// FetchUrl downloads a presigned GET URL into dir.
//
// The scheme is checked before any connection is attempted; the configured
// address policy is applied by the client's dialer, which also covers hosts
// that only resolve to a blocked address at dial time.
func (r *Resolver) FetchUrl(ctx context.Context, dir, rawUrl string) (Input, error) {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return Input{}, fmt.Errorf("input: parsing source url: %w", err)
	}
	if err := checkScheme(parsed.Scheme, r.allowHttp); err != nil {
		return Input{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawUrl, nil)
	if err != nil {
		return Input{}, fmt.Errorf("input: building source request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return Input{}, fmt.Errorf("input: fetching source: %w", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode/100 != 2 {
		return Input{}, &FetchStatusError{StatusCode: resp.StatusCode}
	}
	if r.maxFetchBytes > 0 && resp.ContentLength > r.maxFetchBytes {
		return Input{}, &LimitExceededError{
			Limit:    r.maxFetchBytes,
			Received: resp.ContentLength,
		}
	}

	dst, err := createInputFile(dir)
	if err != nil {
		return Input{}, err
	}
	defer dst.Close()

	n, err := copyLimited(ctx, dst, resp.Body, r.maxFetchBytes)
	if err != nil {
		return Input{}, fmt.Errorf("input: downloading source: %w", err)
	}
	if err := dst.Close(); err != nil {
		return Input{}, fmt.Errorf("input: closing input file: %w", err)
	}
	return Input{Path: dst.Name(), Size: n}, nil
}

// checkScheme guards both the caller's url and every redirect hop.
func checkScheme(scheme string, allowHttp bool) error {
	switch scheme {
	case "https":
		return nil
	case "http":
		if allowHttp {
			return nil
		}
	}
	return &SchemeNotAllowedError{Scheme: scheme}
}

// drainClose lets the transport reuse the connection instead of tearing it
// down when a body is abandoned early.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	_ = body.Close()
}
