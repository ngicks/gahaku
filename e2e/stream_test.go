package e2e

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"slices"
	"testing"
	"time"

	// Decoders for the formats these tests ask for, so a page that comes back
	// can be measured.
	_ "image/jpeg"
	_ "image/png"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
)

const (
	// rpcTimeout bounds one render call, so a converter or a wasm runtime that
	// hangs fails the test that started it instead of the whole package.
	rpcTimeout = 2 * time.Minute
	// chunkSize is what these tests slice a streamed input document into; the
	// wire protocol makes no demand of it beyond the cumulative limit.
	chunkSize = 32 << 10
)

// openFunc is the shape of every generated client method, so a test can name
// the RPC it exercises.
type openFunc func(
	ctx context.Context,
	opts ...grpc.CallOption,
) (grpc.BidiStreamingClient[gahakuv1.RenderRequest, gahakuv1.RenderResponse], error)

// exchange runs one render stream to its end and returns everything the server
// sent back. A send that fails because the server already gave up is not the
// failure worth reporting — the status waiting on Recv is.
func exchange(
	t *testing.T,
	open openFunc,
	requests ...*gahakuv1.RenderRequest,
) ([]*gahakuv1.RenderResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), rpcTimeout)
	defer cancel()

	stream, err := open(ctx)
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

func headerRequest(header *gahakuv1.RenderRequestHeader) *gahakuv1.RenderRequest {
	return &gahakuv1.RenderRequest{Payload: &gahakuv1.RenderRequest_Header{Header: header}}
}

// streamRequests is a whole render request: the header, then the document
// sliced into chunk messages.
func streamRequests(
	header *gahakuv1.RenderRequestHeader,
	document []byte,
) []*gahakuv1.RenderRequest {
	requests := []*gahakuv1.RenderRequest{headerRequest(header)}
	for chunk := range slices.Chunk(document, chunkSize) {
		requests = append(requests, &gahakuv1.RenderRequest{
			Payload: &gahakuv1.RenderRequest_Chunk{Chunk: chunk},
		})
	}
	return requests
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

// urlSource points a header at a presigned GET url instead of a byte stream.
func urlSource(header *gahakuv1.RenderRequestHeader, rawUrl string) *gahakuv1.RenderRequestHeader {
	header.Source = &gahakuv1.Source{
		Kind: &gahakuv1.Source_PresignedGetUrl{PresignedGetUrl: rawUrl},
	}
	return header
}

// putOutput sends the rendered pages to presigned PUT urls instead of back
// down the stream.
func putOutput(
	header *gahakuv1.RenderRequestHeader,
	urls ...string,
) *gahakuv1.RenderRequestHeader {
	header.Output = &gahakuv1.OutputSpec{
		Kind: &gahakuv1.OutputSpec_PresignedPut{
			PresignedPut: &gahakuv1.PresignedPutOutput{Urls: urls},
		},
	}
	return header
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

// pageBytes reassembles the chunk frames of a byte-stream render into whole
// pages, checking the framing on the way: chunks of a page arrive contiguously,
// the page ends at the chunk flagged last, and the pages are indexed 0..n-1.
func pageBytes(t *testing.T, responses []*gahakuv1.RenderResponse) [][]byte {
	t.Helper()
	pages := map[uint32][]byte{}
	ended := map[uint32]bool{}
	for _, resp := range responses {
		chunk := resp.GetPageChunk()
		if chunk == nil {
			t.Fatalf("want a page chunk, got %v", resp)
		}
		index := chunk.GetPageIndex()
		if ended[index] {
			t.Fatalf("page %d continued after its last chunk", index)
		}
		pages[index] = append(pages[index], chunk.GetChunk()...)
		ended[index] = chunk.GetLast()
	}

	ordered := make([][]byte, 0, len(pages))
	for index := range uint32(len(pages)) {
		page, ok := pages[index]
		if !ok {
			t.Fatalf("the %d pages returned do not include page %d", len(pages), index)
		}
		if !ended[index] {
			t.Fatalf("page %d never ended", index)
		}
		ordered = append(ordered, page)
	}
	return ordered
}

// uploadedPages returns the completions a presigned-put render reported, in the
// order it reported them.
func uploadedPages(t *testing.T, responses []*gahakuv1.RenderResponse) []*gahakuv1.PageUploaded {
	t.Helper()
	uploaded := make([]*gahakuv1.PageUploaded, 0, len(responses))
	for _, resp := range responses {
		page := resp.GetPageUploaded()
		if page == nil {
			t.Fatalf("want a page upload, got %v", resp)
		}
		uploaded = append(uploaded, page)
	}
	return uploaded
}

type decodedPage struct {
	format string
	config image.Config
}

// decodePage reads back one rendered page as the image it has to be.
func decodePage(t *testing.T, page []byte) decodedPage {
	t.Helper()
	config, format, err := image.DecodeConfig(bytes.NewReader(page))
	if err != nil {
		t.Fatalf("decoding a rendered page of %d bytes: %v", len(page), err)
	}
	return decodedPage{format: format, config: config}
}

func decodePages(t *testing.T, pages [][]byte) []decodedPage {
	t.Helper()
	decoded := make([]decodedPage, 0, len(pages))
	for _, page := range pages {
		decoded = append(decoded, decodePage(t, page))
	}
	return decoded
}

// assertPixels checks one axis of a page rendered at dpi against the size its
// points ask for, allowing the pixel of slack pdfium's ceil of a float64 scale
// costs: a 216 point page at 150 dpi comes out 451 pixels wide, not 450.
func assertPixels(t *testing.T, axis string, got, points, dpi int) {
	t.Helper()
	want := float64(points) * float64(dpi) / 72
	if float64(got) < want-1 || float64(got) > want+1 {
		t.Errorf("%s = %d px, want about %.0f px (%d points at %d dpi)",
			axis, got, want, points, dpi)
	}
}
