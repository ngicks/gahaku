package commands

import (
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/pkg/worker"
)

func workerImageCmd(parent *cobra.Command, flagConfig *string) {
	cmd := &cobra.Command{
		Use:               "image",
		Short:             "Render an image document",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkerImage(cmd, args, *flagConfig)
		},
	}

	parent.AddCommand(cmd)
}

func runWorkerImage(cmd *cobra.Command, _ []string, flagConfig string) error {
	return runWorker(cmd, worker.KindImage, flagConfig)
}
