package gahaku

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
	"github.com/ngicks/gahaku/pkg/input"
	"github.com/ngicks/gahaku/pkg/server"
	"github.com/ngicks/gahaku/pkg/worker"
)

// ServeOption carries what a caller supplies beside the configuration.
type ServeOption struct {
	// Listener is an already-open listener to serve on; nil listens on
	// cfg.Listen. A test hands its own in rather than claiming a port.
	Listener net.Listener
	// Logger receives what the statuses returned to callers leave out. nil
	// discards them.
	Logger *slog.Logger
	// GrpcOptions are passed to the grpc.Server.
	GrpcOptions []grpc.ServerOption
}

// Serve runs the gahaku gRPC server until ctx is canceled or serving fails.
//
// Cancellation is a graceful stop: the listener closes, renders already running
// are given cfg.ShutdownTimeout to finish, and whatever is still going when it
// passes is cut off. A stop that completes is not an error, so a server asked to
// shut down returns nil.
func Serve(ctx context.Context, cfg Config, opt ServeOption) error {
	service, err := server.New(server.Config{
		Input: input.Config{
			MaxStreamBytes:        cfg.Input.MaxStreamBytes,
			MaxFetchBytes:         cfg.Input.MaxFetchBytes,
			LocalRoots:            cfg.Input.LocalRoots,
			AllowHttp:             cfg.Input.AllowHttp,
			BlockPrivateAddresses: cfg.Input.BlockPrivateAddresses,
		},
		Worker: worker.Config{
			MaxConcurrent: cfg.Worker.Concurrency,
			Timeout:       cfg.Worker.Timeout,
			// The orchestrator starts a worker with no arguments of its own,
			// so the environment is the only channel a resolved soffice path
			// can travel down.
			ExtraEnv: sofficeEnv(cfg.Soffice),
		},
		TempDir: cfg.TempDir,
		Logger:  opt.Logger,
	})
	if err != nil {
		return err
	}

	listener := opt.Listener
	if listener == nil {
		listener, err = net.Listen("tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("gahaku: listening on %q: %w", cfg.Listen, err)
		}
	}

	grpcServer := grpc.NewServer(opt.GrpcOptions...)
	gahakuv1.RegisterGahakuServiceServer(grpcServer, service)

	// serving ends either way — on cancellation, so the stop begins, and on
	// Serve returning by itself, so the stopper does not outlive it.
	serving, stopServing := context.WithCancel(ctx)
	defer stopServing()

	group := new(errgroup.Group)
	group.Go(func() error {
		<-serving.Done()
		if ctx.Err() == nil {
			return nil
		}
		// GracefulStop waits for the renders in flight; the timer is what
		// bounds that wait for the ones that will not finish.
		timer := time.AfterFunc(cfg.ShutdownTimeout, grpcServer.Stop)
		defer timer.Stop()
		grpcServer.GracefulStop()
		return nil
	})
	group.Go(func() error {
		defer stopServing()
		if err := grpcServer.Serve(listener); err != nil {
			return fmt.Errorf("gahaku: serving: %w", err)
		}
		return nil
	})
	return group.Wait()
}

// sofficeEnv is the environment a worker inherits the converter path through,
// empty when the deployment did not name one and the worker's own PATH lookup
// stands.
func sofficeEnv(soffice string) []string {
	if soffice == "" {
		return nil
	}
	return []string{SofficeEnvVar + "=" + soffice}
}
