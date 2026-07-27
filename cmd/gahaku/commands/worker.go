package commands

import (
	"github.com/spf13/cobra"
)

// workerCmd groups the subcommands the server re-executes itself as, one per
// render pipeline. They are hidden because nothing but the orchestrator has any
// reason to run one: the job arrives on stdin and the result leaves on stdout in
// a framing only pkg/worker speaks.
func workerCmd(parent *cobra.Command, flagConfig *string) {
	cmd := &cobra.Command{
		Use:    "worker",
		Short:  "Render one job handed over on stdin",
		Hidden: true,
	}

	workerImageCmd(cmd, flagConfig)
	workerPdfCmd(cmd, flagConfig)
	workerOfficeCmd(cmd, flagConfig)

	parent.AddCommand(cmd)
}
