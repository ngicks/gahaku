package commands

import (
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/pkg/gahaku"
	"github.com/ngicks/gahaku/pkg/worker"
)

// runWorker is what every worker subcommand comes down to. The kind is the only
// thing that differs between them; the configuration arrives the same way for
// all three, through the environment the server exported it into.
func runWorker(cmd *cobra.Command, kind worker.Kind, flagConfig string) error {
	cfg, err := gahaku.LoadConfig(flagConfig)
	if err != nil {
		return err
	}
	return gahaku.Work(cmd.Context(), kind, gahaku.WorkOption{
		Stdin:   cmd.InOrStdin(),
		Stdout:  cmd.OutOrStdout(),
		Soffice: cfg.Soffice,
	})
}
