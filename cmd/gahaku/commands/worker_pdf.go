package commands

import (
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/pkg/worker"
)

func workerPdfCmd(parent *cobra.Command, flagConfig *string) {
	cmd := &cobra.Command{
		Use:               "pdf",
		Short:             "Render a PDF document",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkerPdf(cmd, args, *flagConfig)
		},
	}

	parent.AddCommand(cmd)
}

func runWorkerPdf(cmd *cobra.Command, _ []string, flagConfig string) error {
	return runWorker(cmd, worker.KindPdf, flagConfig)
}
