package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/worker"
)

// The magic bytes of the formats these tests route on. A pipeline never runs
// here, so a prefix detection accepts is a whole document as far as the server
// is concerned.
const (
	pngDocument = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	pdfDocument = "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"
	// oddDocument is bytes detection cannot place, which is what a typed RPC
	// is for and what the untyped one refuses.
	oddDocument = "\x01\x02\x03\x04gahaku not a document\x05\x06"
)

// fakeRunner stands in for the subprocess orchestrator: it records what it was
// asked to render and answers with pages a test laid out, so the server's flow
// is exercised without spawning anything.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []runnerCall
	render func(spec worker.Spec) (worker.Result, error)
	err    error
}

type runnerCall struct {
	kind worker.Kind
	spec worker.Spec
}

func (f *fakeRunner) Run(
	_ context.Context,
	kind worker.Kind,
	spec worker.Spec,
) (worker.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{kind: kind, spec: spec})
	f.mu.Unlock()
	if f.err != nil {
		return worker.Result{}, f.err
	}
	if f.render != nil {
		return f.render(spec)
	}
	return worker.Result{}, nil
}

func (f *fakeRunner) recorded() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// renderPages answers a job with pages left in its directory, the way a worker
// reports back: contents in page-range order, plus the page count of the whole
// document.
func renderPages(documentPages int, contents ...string) func(worker.Spec) (worker.Result, error) {
	return func(spec worker.Spec) (worker.Result, error) {
		result := worker.Result{DocumentPages: documentPages}
		for i, content := range contents {
			path := filepath.Join(spec.JobDir, fmt.Sprintf("page-%04d.png", i))
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				return worker.Result{}, err
			}
			result.Pages = append(result.Pages, worker.Page{PageIndex: i, Path: path})
		}
		return result, nil
	}
}

// newClient serves cfg over an in-memory listener and returns a client of it.
func newClient(t *testing.T, cfg Config) gahakuv1.GahakuServiceClient {
	t.Helper()
	if cfg.TempDir == "" {
		cfg.TempDir = t.TempDir()
	}
	service, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	gahakuv1.RegisterGahakuServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
	})
	return gahakuv1.NewGahakuServiceClient(conn)
}

// openFunc is the shape of every generated client method, so a test can name
// the RPC it exercises.
type openFunc func(
	ctx context.Context,
	opts ...grpc.CallOption,
) (grpc.BidiStreamingClient[gahakuv1.RenderRequest, gahakuv1.RenderResponse], error)

func headerRequest(header *gahakuv1.RenderRequestHeader) *gahakuv1.RenderRequest {
	return &gahakuv1.RenderRequest{Payload: &gahakuv1.RenderRequest_Header{Header: header}}
}

func chunkRequest(chunk string) *gahakuv1.RenderRequest {
	return &gahakuv1.RenderRequest{Payload: &gahakuv1.RenderRequest_Chunk{Chunk: []byte(chunk)}}
}

// exchange runs one render stream to its end and returns everything the server
// sent back. A send that fails because the server already gave up is not the
// failure worth reporting — the status waiting on Recv is.
func exchange(
	t *testing.T,
	open openFunc,
	requests ...*gahakuv1.RenderRequest,
) ([]*gahakuv1.RenderResponse, error) {
	t.Helper()
	stream, err := open(t.Context())
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	for _, req := range requests {
		if err := stream.Send(req); err != nil {
			break
		}
	}
	_ = stream.CloseSend()

	var responses []*gahakuv1.RenderResponse
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return responses, nil
		}
		if err != nil {
			return responses, err
		}
		responses = append(responses, resp)
	}
}

// byteStreamHeader is the common header: bytes in, bytes out, defaults for the
// rest.
func byteStreamHeader() *gahakuv1.RenderRequestHeader {
	return &gahakuv1.RenderRequestHeader{
		Source: &gahakuv1.Source{
			Kind: &gahakuv1.Source_ByteStream{ByteStream: &gahakuv1.ByteStreamSource{}},
		},
		Output: &gahakuv1.OutputSpec{
			Kind: &gahakuv1.OutputSpec_ByteStream{ByteStream: &gahakuv1.ByteStreamOutput{}},
		},
	}
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got no error", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("want %s, got %s: %v", want, got, err)
	}
}

// detailOf returns the request detail of type T attached to err's status.
func detailOf[T proto.Message](t *testing.T, err error) T {
	t.Helper()
	var zero T
	for _, detail := range status.Convert(err).Details() {
		if typed, ok := detail.(T); ok {
			return typed
		}
	}
	t.Fatalf("no %T detail on %v", zero, err)
	return zero
}

// pageBytes reassembles the chunk frames of one page and reports whether their
// last flags framed exactly one page.
func pageBytes(t *testing.T, responses []*gahakuv1.RenderResponse) map[uint32]string {
	t.Helper()
	pages := map[uint32]string{}
	ended := map[uint32]bool{}
	for _, resp := range responses {
		chunk := resp.GetPageChunk()
		if chunk == nil {
			t.Fatalf("want a page chunk, got %v", resp)
		}
		if ended[chunk.GetPageIndex()] {
			t.Fatalf("page %d continued after its last chunk", chunk.GetPageIndex())
		}
		pages[chunk.GetPageIndex()] += string(chunk.GetChunk())
		ended[chunk.GetPageIndex()] = chunk.GetLast()
	}
	for index, done := range ended {
		if !done {
			t.Fatalf("page %d never ended", index)
		}
	}
	return pages
}

func TestNewRefusesAnUnusableJobDirectory(t *testing.T) {
	_, err := New(Config{TempDir: filepath.Join(t.TempDir(), "not-there")})

	if err == nil {
		t.Fatal("want a startup failure, got a server that would fail every job")
	}
}

func TestRenderRejectsAChunkBeforeTheHeader(t *testing.T) {
	runner := &fakeRunner{}
	client := newClient(t, Config{Runner: runner})

	_, err := exchange(t, client.Render, chunkRequest(pngDocument))

	requireCode(t, err, codes.InvalidArgument)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderRejectsAHeaderlessStream(t *testing.T) {
	client := newClient(t, Config{Runner: &fakeRunner{}})

	_, err := exchange(t, client.Render)

	requireCode(t, err, codes.InvalidArgument)
}

func TestRenderRejectsASecondHeader(t *testing.T) {
	runner := &fakeRunner{}
	client := newClient(t, Config{Runner: runner})

	_, err := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		headerRequest(byteStreamHeader()),
	)

	requireCode(t, err, codes.InvalidArgument)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderRejectsAMissingSource(t *testing.T) {
	client := newClient(t, Config{Runner: &fakeRunner{}})

	header := byteStreamHeader()
	header.Source = nil
	_, err := exchange(t, client.Render, headerRequest(header))

	requireCode(t, err, codes.InvalidArgument)
}

func TestRenderRejectsAMissingOutputSpec(t *testing.T) {
	client := newClient(t, Config{Runner: &fakeRunner{}})

	header := byteStreamHeader()
	header.Output = nil
	_, err := exchange(t, client.Render, headerRequest(header))

	requireCode(t, err, codes.InvalidArgument)
}

func TestRenderRejectsInputOverTheStreamLimit(t *testing.T) {
	runner := &fakeRunner{render: renderPages(1, "page")}
	client := newClient(t, Config{
		Input:  input.Config{MaxStreamBytes: 8},
		Runner: runner,
	})

	_, err := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		chunkRequest("0123456789abcdef"),
	)

	requireCode(t, err, codes.ResourceExhausted)
	detail := detailOf[*gahakuv1.InputSizeLimitExceeded](t, err)
	if detail.GetLimitBytes() != 8 {
		t.Errorf("want limit 8, got %d", detail.GetLimitBytes())
	}
	if detail.GetReceivedBytes() != 16 {
		t.Errorf("want received 16, got %d", detail.GetReceivedBytes())
	}
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderStreamsPagesBackAsChunks(t *testing.T) {
	runner := &fakeRunner{render: renderPages(2, "first page", "second")}
	client := newClient(t, Config{Runner: runner, ChunkSize: 4})

	responses, err := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		chunkRequest(pngDocument),
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	pages := pageBytes(t, responses)
	want := map[uint32]string{0: "first page", 1: "second"}
	if !maps.Equal(pages, want) {
		t.Fatalf("want %v, got %v", want, pages)
	}
	// "first page" is 10 bytes at 4 per frame, "second" 6: three frames then
	// two, in that order, with nothing interleaved.
	var indexes []uint32
	for _, resp := range responses {
		indexes = append(indexes, resp.GetPageChunk().GetPageIndex())
	}
	if want := []uint32{0, 0, 0, 1, 1}; !slices.Equal(indexes, want) {
		t.Fatalf("want frames for pages %v, got %v", want, indexes)
	}
}

func TestRenderStreamsAnEmptyPageAsOneFrame(t *testing.T) {
	client := newClient(t, Config{Runner: &fakeRunner{render: renderPages(1, "")}})

	responses, err := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		chunkRequest(pngDocument),
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(responses) != 1 || !responses[0].GetPageChunk().GetLast() {
		t.Fatalf("want one final frame, got %v", responses)
	}
}

func TestRenderPassesTheRequestedOptionsToTheWorker(t *testing.T) {
	runner := &fakeRunner{render: renderPages(3, "a", "b")}
	client := newClient(t, Config{Runner: runner})

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{
		Format: gahakuv1.ImageFormat_IMAGE_FORMAT_JPEG,
		Resize: &gahakuv1.ResizeSpec{MaxWidth: 800, MaxHeight: 600},
		Dpi:    200,
		Pages:  &gahakuv1.PageRange{FirstPage: 2, LastPage: 3},
	}
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
	want := worker.Options{
		Format: worker.FormatJpeg,
		Resize: worker.Resize{MaxWidth: 800, MaxHeight: 600},
		Dpi:    200,
		Pages:  worker.PageRange{FirstPage: 2, LastPage: 3},
	}
	if got := calls[0].spec.Options; got != want {
		t.Fatalf("want %+v, got %+v", want, got)
	}
	if calls[0].kind != worker.KindPdf {
		t.Fatalf("want a pdf job, got %s", calls[0].kind)
	}
}

func TestRenderRejectsAnUnsupportedFormat(t *testing.T) {
	runner := &fakeRunner{}
	client := newClient(t, Config{Runner: runner})

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{Format: gahakuv1.ImageFormat(99)}
	_, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pngDocument))

	requireCode(t, err, codes.InvalidArgument)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderRejectsABackwardsPageRange(t *testing.T) {
	runner := &fakeRunner{}
	client := newClient(t, Config{Runner: runner})

	header := byteStreamHeader()
	header.Options = &gahakuv1.RenderOptions{
		Pages: &gahakuv1.PageRange{FirstPage: 5, LastPage: 2},
	}
	_, err := exchange(t, client.Render, headerRequest(header), chunkRequest(pdfDocument))

	requireCode(t, err, codes.InvalidArgument)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestRenderSniffsTheDocumentKind(t *testing.T) {
	for _, tt := range []struct {
		name     string
		document string
		want     worker.Kind
	}{
		{name: "png routes to the image pipeline", document: pngDocument, want: worker.KindImage},
		{name: "pdf routes to the pdf pipeline", document: pdfDocument, want: worker.KindPdf},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{render: renderPages(1, "page")}
			client := newClient(t, Config{Runner: runner})

			_, err := exchange(t, client.Render,
				headerRequest(byteStreamHeader()),
				chunkRequest(tt.document),
			)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			calls := runner.recorded()
			if len(calls) != 1 || calls[0].kind != tt.want {
				t.Fatalf("want one %s job, got %+v", tt.want, calls)
			}
		})
	}
}

func TestRenderRejectsAnUnsupportedMediaType(t *testing.T) {
	runner := &fakeRunner{}
	client := newClient(t, Config{Runner: runner})

	_, err := exchange(t, client.Render,
		headerRequest(byteStreamHeader()),
		chunkRequest(oddDocument),
	)

	requireCode(t, err, codes.InvalidArgument)
	if calls := runner.recorded(); len(calls) != 0 {
		t.Fatalf("want no worker, got %d", len(calls))
	}
}

func TestTypedRpcsAssertTheKind(t *testing.T) {
	for _, tt := range []struct {
		name     string
		open     func(gahakuv1.GahakuServiceClient) openFunc
		document string
		want     worker.Kind
		wantErr  bool
	}{
		{
			name:     "an image is rendered as one",
			open:     func(c gahakuv1.GahakuServiceClient) openFunc { return c.RenderImage },
			document: pngDocument,
			want:     worker.KindImage,
		},
		{
			name:     "a pdf sent to RenderImage is refused",
			open:     func(c gahakuv1.GahakuServiceClient) openFunc { return c.RenderImage },
			document: pdfDocument,
			wantErr:  true,
		},
		{
			name:     "an undetectable document is taken at the caller's word",
			open:     func(c gahakuv1.GahakuServiceClient) openFunc { return c.RenderOffice },
			document: oddDocument,
			want:     worker.KindOffice,
		},
		{
			name:     "a pdf is rendered as one",
			open:     func(c gahakuv1.GahakuServiceClient) openFunc { return c.RenderPdf },
			document: pdfDocument,
			want:     worker.KindPdf,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{render: renderPages(1, "page")}
			client := newClient(t, Config{Runner: runner})

			_, err := exchange(t, tt.open(client),
				headerRequest(byteStreamHeader()),
				chunkRequest(tt.document),
			)

			if tt.wantErr {
				requireCode(t, err, codes.InvalidArgument)
				if calls := runner.recorded(); len(calls) != 0 {
					t.Fatalf("want no worker, got %d", len(calls))
				}
				return
			}
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			calls := runner.recorded()
			if len(calls) != 1 || calls[0].kind != tt.want {
				t.Fatalf("want one %s job, got %+v", tt.want, calls)
			}
		})
	}
}
