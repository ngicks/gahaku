package commands

import (
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/pkg/worker"
)

func workerOfficeCmd(parent *cobra.Command, flagConfig *string) {
	cmd := &cobra.Command{
		Use:               "office",
		Short:             "Render an MS Office document",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkerOffice(cmd, args, *flagConfig)
		},
	}

	parent.AddCommand(cmd)
}

func runWorkerOffice(cmd *cobra.Command, _ []string, flagConfig string) error {
	return runWorker(cmd, worker.KindOffice, flagConfig)
}
