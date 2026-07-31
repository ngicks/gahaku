package commands

import (
	"time"

	"github.com/ngicks/go-common/contextkey"
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/gahaku"
)

// serveFlags holds what `serve` binds, so the run function can tell which of
// them the caller actually set and overlay only those onto the loaded config.
type serveFlags struct {
	Listen                string
	MaxStreamBytes        int64
	MaxFetchBytes         int64
	AllowLocalRoot        []string
	AllowHttp             bool
	BlockPrivateAddresses bool
	Concurrency           int
	JobTimeout            time.Duration
	ShutdownTimeout       time.Duration
	TempDir               string
	Soffice               string
}

func serveCmd(parent *cobra.Command, flagConfig *string) {
	var flags serveFlags

	cmd := &cobra.Command{
		Use:               "serve",
		Short:             "Run the gahaku gRPC server",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, args, *flagConfig, flags)
		},
	}

	cmd.Flags().StringVar(&flags.Listen, "listen", ":9000", "address the gRPC server binds")
	cmd.Flags().Int64Var(
		&flags.MaxStreamBytes,
		"max-stream-bytes",
		32<<20,
		"cumulative cap on client-streamed input; negative disables it",
	)
	cmd.Flags().Int64Var(
		&flags.MaxFetchBytes,
		"max-fetch-bytes",
		0,
		"cap on a presigned GET body; 0 disables it",
	)
	cmd.Flags().StringArrayVar(
		&flags.AllowLocalRoot,
		"allow-local-root",
		nil,
		"directory local input paths may resolve under; repeatable, none disables local input",
	)
	cmd.Flags().BoolVar(
		&flags.AllowHttp,
		"allow-http",
		false,
		"accept plain http:// presigned urls as well as https",
	)
	cmd.Flags().BoolVar(
		&flags.BlockPrivateAddresses,
		"block-private-addresses",
		true,
		"refuse presigned urls that resolve into the server's own network",
	)
	cmd.Flags().IntVar(
		&flags.Concurrency,
		"concurrency",
		0,
		"render workers allowed to run at once; 0 selects one per CPU",
	)
	cmd.Flags().DurationVar(
		&flags.JobTimeout,
		"job-timeout",
		2*time.Minute,
		"deadline of a single render job",
	)
	cmd.Flags().DurationVar(
		&flags.ShutdownTimeout,
		"shutdown-timeout",
		gahaku.DefaultShutdownTimeout,
		"how long a graceful stop waits for renders in flight",
	)
	cmd.Flags().StringVar(
		&flags.TempDir,
		"temp-dir",
		"",
		"parent of the per-job directories; empty selects the system temp dir",
	)
	cmd.Flags().StringVar(
		&flags.Soffice,
		"soffice",
		"",
		"LibreOffice binary the office pipeline converts with; empty looks up PATH",
	)

	parent.AddCommand(cmd)
}

func runServe(cmd *cobra.Command, _ []string, flagConfig string, flags serveFlags) error {
	cfg, err := gahaku.LoadConfig(flagConfig)
	if err != nil {
		return err
	}
	// Only the flags the caller set: a default would otherwise outrank the
	// config file and the environment it is layered over.
	set := cmd.Flags().Changed
	if set("listen") {
		cfg.Listen = flags.Listen
	}
	if set("max-stream-bytes") {
		cfg.Input.MaxStreamBytes = flags.MaxStreamBytes
	}
	if set("max-fetch-bytes") {
		cfg.Input.MaxFetchBytes = flags.MaxFetchBytes
	}
	if set("allow-local-root") {
		cfg.Input.LocalRoots = flags.AllowLocalRoot
	}
	if set("allow-http") {
		cfg.Input.AllowHttp = flags.AllowHttp
	}
	if set("block-private-addresses") {
		cfg.Input.BlockPrivateAddresses = flags.BlockPrivateAddresses
	}
	if set("concurrency") {
		cfg.Worker.Concurrency = flags.Concurrency
	}
	if set("job-timeout") {
		cfg.Worker.Timeout = flags.JobTimeout
	}
	if set("shutdown-timeout") {
		cfg.ShutdownTimeout = flags.ShutdownTimeout
	}
	if set("temp-dir") {
		cfg.TempDir = flags.TempDir
	}
	if set("soffice") {
		cfg.Soffice = flags.Soffice
	}

	return gahaku.Serve(cmd.Context(), cfg, gahaku.ServeOption{
		Logger: contextkey.ValueSlogLoggerDefault(cmd.Context()),
	})
}
