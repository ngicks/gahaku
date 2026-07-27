package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/worker"
)

// localHeader asks for a render of the file at path, streamed back as bytes.
func localHeader(path string) *gahakuv1.RenderRequestHeader {
	header := byteStreamHeader()
	header.Source = &gahakuv1.Source{Kind: &gahakuv1.Source_LocalPath{LocalPath: path}}
	return header
}

// urlHeader asks for a render of the document at rawUrl.
func urlHeader(rawUrl string) *gahakuv1.RenderRequestHeader {
	header := byteStreamHeader()
	header.Source = &gahakuv1.Source{
		Kind: &gahakuv1.Source_PresignedGetUrl{PresignedGetUrl: rawUrl},
	}
	return header
}

// documentServer serves one document, or the status a test asked for instead.
func documentServer(t *testing.T, status int, document string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(document))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRenderReadsAnAllowlistedLocalPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "document.png")
	if err := os.WriteFile(path, []byte(pngDocument), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runner := &fakeRunner{render: renderPages(1, "rendered")}
	client := newClient(t, Config{
		Input:  input.Config{LocalRoots: []string{root}},
		Runner: runner,
	})

	responses, err := exchange(t, client.Render, headerRequest(localHeader(path)))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if got := pageBytes(t, responses)[0]; got != "rendered" {
		t.Fatalf("want the rendered page, got %q", got)
	}
	calls := runner.recorded()
	if len(calls) != 1 || calls[0].kind != worker.KindImage {
		t.Fatalf("want one image job, got %+v", calls)
	}
	// The document is copied into the job directory rather than handed to the
	// worker where it lies.
	if filepath.Dir(calls[0].spec.InputPath) != calls[0].spec.JobDir {
		t.Fatalf("want the input inside the job dir, got %q", calls[0].spec.InputPath)
	}
}

func TestRenderRefusesLocalPathsOutsideTheAllowlist(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte(pngDocument), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for _, tt := range []struct {
		name  string
		roots []string
	}{
		{name: "a path outside every root", roots: []string{root}},
		{name: "any path at all when local input is off", roots: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			client := newClient(t, Config{
				Input:  input.Config{LocalRoots: tt.roots},
				Runner: runner,
			})

			_, err := exchange(t, client.Render, headerRequest(localHeader(outside)))

			requireCode(t, err, codes.PermissionDenied)
			if message := status.Convert(err).Message(); strings.Contains(message, outside) {
				t.Fatalf("the status leaked the requested path: %s", message)
			}
			if calls := runner.recorded(); len(calls) != 0 {
				t.Fatalf("want no worker, got %d", len(calls))
			}
		})
	}
}

func TestRenderFetchesAPresignedSourceUrl(t *testing.T) {
	srv := documentServer(t, http.StatusOK, pdfDocument)
	runner := &fakeRunner{render: renderPages(1, "rendered")}
	client := newClient(t, Config{
		Input:  input.Config{AllowHttp: true},
		Runner: runner,
	})

	responses, err := exchange(t, client.Render, headerRequest(urlHeader(srv.URL)))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if got := pageBytes(t, responses)[0]; got != "rendered" {
		t.Fatalf("want the rendered page, got %q", got)
	}
	calls := runner.recorded()
	if len(calls) != 1 || calls[0].kind != worker.KindPdf {
		t.Fatalf("want one pdf job, got %+v", calls)
	}
	fetched, err := os.ReadFile(calls[0].spec.InputPath)
	if err == nil && string(fetched) != pdfDocument {
		t.Fatalf("want the fetched document, got %q", fetched)
	}
}

func TestRenderMapsSourceFetchFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		want   codes.Code
	}{
		{name: "an expired presign", status: http.StatusForbidden, want: codes.FailedPrecondition},
		{name: "a missing object", status: http.StatusNotFound, want: codes.NotFound},
		{name: "a broken store", status: http.StatusBadGateway, want: codes.Unavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := documentServer(t, tt.status, "")
			runner := &fakeRunner{}
			client := newClient(t, Config{
				Input:  input.Config{AllowHttp: true},
				Runner: runner,
			})

			_, err := exchange(t, client.Render, headerRequest(urlHeader(srv.URL)))

			requireCode(t, err, tt.want)
			if calls := runner.recorded(); len(calls) != 0 {
				t.Fatalf("want no worker, got %d", len(calls))
			}
		})
	}
}

func TestRenderRefusesAPlainHttpSourceByDefault(t *testing.T) {
	srv := documentServer(t, http.StatusOK, pdfDocument)
	client := newClient(t, Config{Runner: &fakeRunner{}})

	_, err := exchange(t, client.Render, headerRequest(urlHeader(srv.URL)))

	requireCode(t, err, codes.InvalidArgument)
}
