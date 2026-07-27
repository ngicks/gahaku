package commands

import (
	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/internal/versioninfo"
	"github.com/ngicks/gahaku/pkg/gahaku"
)

func versionCmd(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE:  runVersion,
	}

	parent.AddCommand(cmd)
}

func runVersion(cmd *cobra.Command, _ []string) error {
	info := versioninfo.ReadVersionInfo(gahaku.Version)
	cmd.Printf("version:     %s\n", info.Version)
	if info.Commit != "" {
		modified := ""
		if info.Modified {
			modified = " (modified)"
		}
		cmd.Printf("commit:      %s%s\n", info.Commit, modified)
	}
	if info.CommitTime != "" {
		cmd.Printf("commit time: %s\n", info.CommitTime)
	}
	if info.GoVersion != "" {
		cmd.Printf("go version:  %s\n", info.GoVersion)
	}
	return nil
}
