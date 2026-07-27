package input

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// bodyServer stands in for object storage: it answers every GET with body.
func bodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchUrlOverHttps(t *testing.T) {
	const body = "presigned payload"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// srv.Client() trusts the test server's certificate; the resolver's own
	// scheme policy still applies.
	r := newResolver(t, Config{HttpClient: srv.Client()})

	in, err := r.FetchUrl(t.Context(), dir, srv.URL)
	if err != nil {
		t.Fatalf("FetchUrl: %v", err)
	}
	if in.Path != filepath.Join(dir, inputFileName) {
		t.Errorf("Path = %q, want it inside the job dir %q", in.Path, dir)
	}
	if in.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", in.Size, len(body))
	}
	got, err := os.ReadFile(in.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != body {
		t.Errorf("downloaded %q, want %q", got, body)
	}
}

func TestFetchUrlScheme(t *testing.T) {
	srv := bodyServer(t, "payload")

	t.Run("plain http is rejected by default", func(t *testing.T) {
		r := newResolver(t, Config{})
		_, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL)
		schemeErr, ok := errors.AsType[*SchemeNotAllowedError](err)
		if !ok {
			t.Fatalf("err = %v, want *SchemeNotAllowedError", err)
		}
		if schemeErr.Scheme != "http" {
			t.Errorf("Scheme = %q, want %q", schemeErr.Scheme, "http")
		}
	})

	t.Run("plain http is accepted when opted in", func(t *testing.T) {
		r := newResolver(t, Config{AllowHttp: true})
		if _, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL); err != nil {
			t.Fatalf("FetchUrl: %v", err)
		}
	})

	t.Run("other schemes stay rejected", func(t *testing.T) {
		r := newResolver(t, Config{AllowHttp: true})
		_, err := r.FetchUrl(t.Context(), t.TempDir(), "file:///etc/passwd")
		if _, ok := errors.AsType[*SchemeNotAllowedError](err); !ok {
			t.Fatalf("err = %v, want *SchemeNotAllowedError", err)
		}
	})
}

// TestFetchUrlRejectsRedirectDowngrade covers the hop a scheme check on the
// caller's url alone would miss: an https source that redirects to plain http.
func TestFetchUrlRejectsRedirectDowngrade(t *testing.T) {
	plain := bodyServer(t, "payload")
	redirector := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL, http.StatusFound)
		}),
	)
	defer redirector.Close()

	r := newResolver(t, Config{HttpClient: redirector.Client()})
	_, err := r.FetchUrl(t.Context(), t.TempDir(), redirector.URL)
	schemeErr, ok := errors.AsType[*SchemeNotAllowedError](err)
	if !ok {
		t.Fatalf("err = %v, want *SchemeNotAllowedError", err)
	}
	if schemeErr.Scheme != "http" {
		t.Errorf("Scheme = %q, want %q", schemeErr.Scheme, "http")
	}
}

func TestFetchUrlSizeCap(t *testing.T) {
	const body = "0123456789"
	srv := bodyServer(t, body)

	for _, tt := range []struct {
		name    string
		limit   int64
		wantErr bool
	}{
		{name: "under the cap", limit: int64(len(body)) + 1},
		{name: "exactly at the cap", limit: int64(len(body))},
		{name: "over the cap", limit: int64(len(body)) - 1, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newResolver(t, Config{AllowHttp: true, MaxFetchBytes: tt.limit})
			in, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL)
			limitErr, ok := errors.AsType[*LimitExceededError](err)
			switch {
			case tt.wantErr && !ok:
				t.Fatalf("err = %v, want *LimitExceededError", err)
			case tt.wantErr:
				if limitErr.Limit != tt.limit {
					t.Errorf("Limit = %d, want %d", limitErr.Limit, tt.limit)
				}
			case err != nil:
				t.Fatalf("FetchUrl: %v", err)
			case in.Size != int64(len(body)):
				t.Errorf("Size = %d, want %d", in.Size, len(body))
			}
		})
	}
}

// TestFetchUrlSizeCapUnknownLength covers the streaming check: without a
// Content-Length header the cap can only trip while the body is read.
func TestFetchUrlSizeCapUnknownLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		for range 4 {
			_, _ = w.Write([]byte("0123456789"))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	r := newResolver(t, Config{AllowHttp: true, MaxFetchBytes: 15})
	_, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL)
	if _, ok := errors.AsType[*LimitExceededError](err); !ok {
		t.Fatalf("err = %v, want *LimitExceededError", err)
	}
}

func TestFetchUrlBlocksPrivateAddresses(t *testing.T) {
	srv := bodyServer(t, "payload")

	t.Run("blocked when the policy is on", func(t *testing.T) {
		r := newResolver(t, Config{AllowHttp: true, BlockPrivateAddresses: true})
		_, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL)
		blockErr, ok := errors.AsType[*AddressBlockedError](err)
		if !ok {
			t.Fatalf("err = %v, want *AddressBlockedError", err)
		}
		if !blockErr.Address.IsLoopback() {
			t.Errorf("Address = %s, want the loopback address", blockErr.Address)
		}
	})

	t.Run("reachable when the policy is off", func(t *testing.T) {
		r := newResolver(t, Config{AllowHttp: true})
		if _, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL); err != nil {
			t.Fatalf("FetchUrl: %v", err)
		}
	})
}

func TestFetchUrlStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired", http.StatusForbidden)
	}))
	defer srv.Close()

	r := newResolver(t, Config{AllowHttp: true})
	_, err := r.FetchUrl(t.Context(), t.TempDir(), srv.URL)
	statusErr, ok := errors.AsType[*FetchStatusError](err)
	if !ok {
		t.Fatalf("err = %v, want *FetchStatusError", err)
	}
	if statusErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, http.StatusForbidden)
	}
}
