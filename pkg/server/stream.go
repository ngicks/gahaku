package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"google.golang.org/grpc"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/output"
	"github.com/ngicks/gahaku/pkg/sniff"
	"github.com/ngicks/gahaku/pkg/worker"
)

// renderStream is the wire every RPC of this service speaks; the generated
// per-RPC server types are all aliases of it.
type renderStream = grpc.BidiStreamingServer[gahakuv1.RenderRequest, gahakuv1.RenderResponse]

// pageSink is the half of pkg/output this handler uses, so a ChunkSink and a
// PutSink are interchangeable once the output spec has picked one.
type pageSink interface {
	WritePage(ctx context.Context, pageIndex int, page io.ReadSeeker) error
	Close() error
}

// handle runs one render job end to end.
//
// The order of the steps is the point: everything that can be judged from the
// header alone — the options, the output spec, the page range — is judged
// before a byte of input is read, and the input is fully materialized before a
// worker is spawned for it.
func (s *Service) handle(stream renderStream, asserted worker.Kind) error {
	ctx := stream.Context()

	header, err := recvHeader(stream)
	if err != nil {
		return s.fail(ctx, err)
	}
	options, err := renderOptions(header.GetOptions())
	if err != nil {
		return s.fail(ctx, err)
	}
	sink, pages, err := s.openSink(stream, header.GetOutput(), options.Pages)
	if err != nil {
		return s.fail(ctx, err)
	}
	options.Pages = pages

	jobDir, err := os.MkdirTemp(s.tempDir, "gahaku-job-")
	if err != nil {
		return s.fail(ctx, fmt.Errorf("server: creating the job directory: %w", err))
	}
	// The worker leaves the rendered pages in here, so the directory outlives
	// the job only until they have been delivered.
	defer func() { _ = os.RemoveAll(jobDir) }()

	document, err := s.resolveInput(ctx, stream, jobDir, header.GetSource())
	if err != nil {
		return s.fail(ctx, err)
	}
	kind, err := resolveKind(asserted, document.Path)
	if err != nil {
		return s.fail(ctx, err)
	}

	result, err := s.runner.Run(ctx, kind, worker.Spec{
		InputPath: document.Path,
		JobDir:    jobDir,
		Options:   options,
	})
	if err != nil {
		return s.fail(ctx, err)
	}
	if err := deliver(ctx, sink, result, options.Pages); err != nil {
		return s.fail(ctx, err)
	}
	return nil
}

// recvHeader reads the message that opens a render stream.
func recvHeader(stream renderStream) (*gahakuv1.RenderRequestHeader, error) {
	req, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errHeaderExpected
		}
		return nil, err
	}
	header := req.GetHeader()
	if header == nil {
		return nil, errHeaderExpected
	}
	return header, nil
}

// openSink builds the destination the rendered pages go to and reports the page
// range the job runs with.
//
// Presigned PUT output closes an open-ended range: the destination list says how
// many pages the caller expects, and a document with more pages than that would
// otherwise render pages there is nowhere to put.
func (s *Service) openSink(
	stream renderStream,
	spec *gahakuv1.OutputSpec,
	pages worker.PageRange,
) (pageSink, worker.PageRange, error) {
	first, last, err := resolveRange(pages)
	if err != nil {
		return nil, pages, err
	}

	switch kind := spec.GetKind().(type) {
	case *gahakuv1.OutputSpec_ByteStream:
		return output.NewChunkSink(chunkSender(stream), s.chunkSize),
			worker.PageRange{FirstPage: first, LastPage: last},
			nil
	case *gahakuv1.OutputSpec_PresignedPut:
		urls := kind.PresignedPut.GetUrls()
		if len(urls) == 0 {
			return nil, pages, &output.UrlCountMismatchError{PageCount: pageCount(first, last)}
		}
		if last == 0 {
			last = first + uint32(len(urls)) - 1
		}
		sink, err := output.NewPutSink(output.PutSinkConfig{
			Urls:      urls,
			PageCount: pageCount(first, last),
			Notify:    uploadNotifier(stream),
			Client:    s.client,
			AllowHttp: s.allowHttp,
		})
		if err != nil {
			return nil, pages, err
		}
		return sink, worker.PageRange{FirstPage: first, LastPage: last}, nil
	default:
		return nil, pages, errOutputRequired
	}
}

// resolveInput materializes the request's source into the job directory.
func (s *Service) resolveInput(
	ctx context.Context,
	stream renderStream,
	jobDir string,
	source *gahakuv1.Source,
) (input.Input, error) {
	switch kind := source.GetKind().(type) {
	case *gahakuv1.Source_ByteStream:
		return s.receiveByteStream(ctx, stream, jobDir)
	case *gahakuv1.Source_PresignedGetUrl:
		// The remaining messages are not waited for: the protocol says a
		// URI-sourced request half-closes after its header, and a client that
		// keeps the stream open instead would otherwise hang here rather than
		// be told it sent something it should not have.
		return s.resolver.FetchUrl(ctx, jobDir, kind.PresignedGetUrl)
	case *gahakuv1.Source_LocalPath:
		return s.resolver.ResolveLocalPath(ctx, jobDir, kind.LocalPath)
	default:
		return input.Input{}, errSourceRequired
	}
}

// receiveByteStream pumps the stream's chunk messages into the job's input
// file, which is where the cumulative size limit trips — while the upload is
// still arriving and before anything has been spawned for it.
func (s *Service) receiveByteStream(
	ctx context.Context,
	stream renderStream,
	jobDir string,
) (input.Input, error) {
	writer, err := s.resolver.OpenByteStream(jobDir)
	if err != nil {
		return input.Input{}, err
	}
	defer func() { _ = writer.Close() }()

	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return input.Input{}, err
		}
		chunk, ok := req.GetPayload().(*gahakuv1.RenderRequest_Chunk)
		if !ok {
			return input.Input{}, errChunkExpected
		}
		if _, err := writer.Write(chunk.Chunk); err != nil {
			return input.Input{}, err
		}
	}
	return writer.Finish()
}

// resolveKind decides which pipeline renders the document at path.
//
// A typed RPC asserts the kind, and detection is only allowed to contradict it:
// a format whose marker sits past the sniffed prefix reads as its container type
// (sniff.PrefixSize), which is exactly the case the typed RPCs exist for, so an
// input detection cannot place is left to the caller's word.
func resolveKind(asserted worker.Kind, path string) (worker.Kind, error) {
	detected, err := sniff.DetectFile(path)
	if asserted == "" {
		return detected, err
	}
	if err != nil {
		if _, ok := errors.AsType[*sniff.UnsupportedMediaTypeError](err); ok {
			return asserted, nil
		}
		return "", err
	}
	if detected != asserted {
		return "", &KindMismatchError{Asserted: asserted, Detected: detected}
	}
	return asserted, nil
}

// deliver hands the rendered pages to the sink in page order and closes it.
func deliver(
	ctx context.Context,
	sink pageSink,
	result worker.Result,
	pages worker.PageRange,
) error {
	for _, page := range result.Pages {
		if err := writePage(ctx, sink, page); err != nil {
			return err
		}
	}
	if err := sink.Close(); err != nil {
		if _, ok := errors.AsType[*output.ShortPageCountError](err); ok {
			// The sink knows how many pages it wanted, not which pages those
			// were nor how long the document turned out to be, which is what
			// the caller needs to correct its request.
			return &ShortDocumentError{
				FirstPage:     pages.FirstPage,
				LastPage:      pages.LastPage,
				DocumentPages: result.DocumentPages,
				Err:           err,
			}
		}
		return err
	}
	return nil
}

func writePage(ctx context.Context, sink pageSink, page worker.Page) error {
	file, err := os.Open(page.Path)
	if err != nil {
		return fmt.Errorf("server: opening rendered page %d: %w", page.PageIndex, err)
	}
	defer func() { _ = file.Close() }()
	return sink.WritePage(ctx, page.PageIndex, file)
}

// chunkSender frames a page slice into a response message.
func chunkSender(stream renderStream) output.SendFunc {
	return func(chunk output.PageChunk) error {
		return stream.Send(&gahakuv1.RenderResponse{
			Payload: &gahakuv1.RenderResponse_PageChunk{
				PageChunk: &gahakuv1.PageChunk{
					PageIndex: uint32(chunk.PageIndex),
					Chunk:     chunk.Chunk,
					Last:      chunk.Last,
				},
			},
		})
	}
}

// uploadNotifier reports a finished page upload as a response message.
func uploadNotifier(stream renderStream) func(output.PageUploaded) error {
	return func(uploaded output.PageUploaded) error {
		return stream.Send(&gahakuv1.RenderResponse{
			Payload: &gahakuv1.RenderResponse_PageUploaded{
				PageUploaded: &gahakuv1.PageUploaded{
					PageIndex: uint32(uploaded.PageIndex),
					SizeBytes: uint64(uploaded.SizeBytes),
				},
			},
		})
	}
}
