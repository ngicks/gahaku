// Package commands is the gahaku command tree: it parses flags, layers them
// over the configuration the gahaku package loads, and hands the result to the
// service. Nothing here computes.
package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ngicks/go-common/contextkey"
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/internal/loggerfactory"
)

func Execute(ctx context.Context) error {
	return rootCmd().ExecuteContext(ctx)
}

func rootCmd() *cobra.Command {
	var (
		logConfig   *loggerfactory.Config
		flagVersion bool
		flagConfig  string
	)

	cmd := &cobra.Command{
		Use:           "gahaku",
		Short:         "Render documents into raster images over gRPC",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if err := loggerfactory.ReadEnv(logConfig, "gahaku", os.Environ()); err != nil {
				fmt.Fprintln(os.Stderr, "warning:", err)
			}
			logger := loggerfactory.BuildLogger(logConfig)
			slog.SetDefault(logger)
			cmd.SetContext(contextkey.WithSlogLogger(cmd.Context(), logger))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagVersion {
				return runVersion(cmd, args)
			}
			return runRoot(cmd, args)
		},
	}

	logConfig = loggerfactory.RegisterFlags(cmd)
	cmd.Flags().BoolVar(&flagVersion, "version", false, "alias for the version subcommand")
	cmd.PersistentFlags().StringVar(
		&flagConfig,
		"config",
		"",
		"config file path; overrides the default location",
	)

	versionCmd(cmd)
	configCmd(cmd, &flagConfig)
	serveCmd(cmd, &flagConfig)
	workerCmd(cmd, &flagConfig)

	return cmd
}

func runRoot(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}
