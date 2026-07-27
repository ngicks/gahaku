// Package server implements the GahakuService gRPC surface.
//
// One handler core runs all four RPCs, which differ only in how the input kind
// is decided: Render sniffs it (pkg/sniff), RenderImage, RenderPdf and
// RenderOffice take the caller's word for it. Everything else is the same job —
// materialize the input (pkg/input), spawn a worker for it (pkg/worker), and
// deliver the pages it rendered (pkg/output).
//
// This package is where protobuf meets the plain-Go packages below it: it
// translates RenderOptions into worker.Options on the way in and every typed
// error into a gRPC status on the way out (errors.go), so nothing underneath
// has to know about gRPC.
//
// Serving is not its job. A Service takes its dependencies and implements the
// generated server interface; the caller owns the listener and the grpc.Server.
package server

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/worker"
)

// Runner renders one job in a worker subprocess. It is the half of
// *worker.Runner this package uses, named here so a caller — a test above all —
// can render without spawning anything.
type Runner interface {
	Run(ctx context.Context, kind worker.Kind, spec worker.Spec) (worker.Result, error)
}

// Config configures a Service. The zero value is usable: it denies local
// paths, requires https, re-executes the running binary as the worker and
// renders jobs under the operating system's temp directory.
type Config struct {
	// Input configures how source documents are resolved. Its AllowHttp also
	// governs presigned PUT destinations, so one setting covers both
	// directions of a job's object-storage traffic.
	Input input.Config
	// Worker configures the subprocess orchestrator Runner is built from when
	// Runner is nil.
	Worker worker.Config
	// Runner renders jobs. nil builds one from Worker.
	Runner Runner
	// TempDir is where per-job directories are created. Empty selects the
	// operating system's temp directory.
	TempDir string
	// ChunkSize is the payload size of one response frame. 0 or less selects
	// output.DefaultChunkSize.
	ChunkSize int
	// Logger receives the failures behind the statuses this service returns:
	// a status message says what the caller can act on, the log line carries
	// the paths, exit statuses and converter output it must not. nil discards
	// them.
	Logger *slog.Logger
}

// Service implements gahakuv1.GahakuServiceServer.
type Service struct {
	gahakuv1.UnimplementedGahakuServiceServer

	resolver  *input.Resolver
	runner    Runner
	client    *http.Client
	tempDir   string
	chunkSize int
	allowHttp bool
	logger    *slog.Logger
}

// New validates cfg and returns the service.
func New(cfg Config) (*Service, error) {
	resolver, err := input.New(cfg.Input)
	if err != nil {
		return nil, err
	}
	if err := checkTempDir(cfg.TempDir); err != nil {
		return nil, err
	}
	runner := cfg.Runner
	if runner == nil {
		runner = worker.New(cfg.Worker)
	}
	return &Service{
		resolver: resolver,
		runner:   runner,
		// The resolver's client, rather than a second one: a presigned PUT
		// destination is as much of an SSRF vector as a presigned GET source,
		// and this client already carries the scheme and address policy.
		client:    resolver.HttpClient(),
		tempDir:   cfg.TempDir,
		chunkSize: cfg.ChunkSize,
		allowHttp: cfg.Input.AllowHttp,
		logger:    cmp.Or(cfg.Logger, slog.New(slog.DiscardHandler)),
	}, nil
}

// checkTempDir creates and removes a job directory once, at startup: nothing
// else reads the setting until a job needs a directory, and a temp dir that is
// missing or unwritable would otherwise be a server that starts fine and fails
// every request.
func checkTempDir(dir string) error {
	if dir == "" {
		return nil
	}
	probe, err := os.MkdirTemp(dir, "gahaku-probe-")
	if err != nil {
		return fmt.Errorf("server: job directory: %w", err)
	}
	return os.Remove(probe)
}

// Render renders a document of a kind detected from its own bytes.
func (s *Service) Render(stream gahakuv1.GahakuService_RenderServer) error {
	return s.handle(stream, "")
}

// RenderImage renders a document the caller asserts is an image.
func (s *Service) RenderImage(stream gahakuv1.GahakuService_RenderImageServer) error {
	return s.handle(stream, worker.KindImage)
}

// RenderPdf renders a document the caller asserts is a PDF.
func (s *Service) RenderPdf(stream gahakuv1.GahakuService_RenderPdfServer) error {
	return s.handle(stream, worker.KindPdf)
}

// RenderOffice renders a document the caller asserts is an Office file.
func (s *Service) RenderOffice(stream gahakuv1.GahakuService_RenderOfficeServer) error {
	return s.handle(stream, worker.KindOffice)
}
