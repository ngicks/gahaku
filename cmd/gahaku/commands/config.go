package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ngicks/gahaku/pkg/gahaku"
	"github.com/ngicks/gahaku/pkg/gahaku/cli"
)

// configLongFmt documents the resolved-config shape so users can write --format
// templates without reading the source. The %s is filled with the shared
// template-helper docs (cli.TemplateFuncHelp). Keep the field list in sync with
// gahaku.Config.
const configLongFmt = `config loads every layer (defaults < file < environment), applies any
explicitly-set flags on top, and prints the fully-resolved configuration. With
no flags it prints indented JSON; with --format it renders a Go text/template
against the config value instead.

The value passed to --format has this shape (Go field name, type, JSON key);
nesting is shown as a tree so deep configs stay readable:

  Config
  ├─ .Listen           string    # gRPC listen address   (listen)
  ├─ .ShutdownTimeout  Duration  # graceful stop budget  (shutdown_timeout)
  ├─ .TempDir          string    # job directory parent  (temp_dir)
  ├─ .Soffice          string    # LibreOffice binary    (soffice)
  ├─ .Input                      # input policy          (input)
  │   ├─ .MaxStreamBytes         int       # streamed input cap
  │   ├─ .MaxFetchBytes          int       # presigned GET cap
  │   ├─ .LocalRoots             []string  # allowed local roots
  │   ├─ .AllowHttp              bool      # accept plain http urls
  │   └─ .BlockPrivateAddresses  bool      # refuse internal addresses
  └─ .Worker                     # worker orchestration  (worker)
      ├─ .Concurrency            int       # workers at once
      └─ .Timeout                Duration  # per-job deadline

Use the Go field names in --format (e.g. {{.Listen}}, or {{.Worker.Timeout}}
for a nested field); the default JSON output uses the lower-case keys shown in
parentheses, a sub-config's under its own (input.allow_http). The template
also sees these helper functions:

%s`

const configExample = `  gahaku config
  gahaku config --format '{{.Listen}}'
  gahaku config --format '{{ json .Input }}'`

func configCmd(parent *cobra.Command, flagConfig *string) {
	var flagFormat string

	cmd := &cobra.Command{
		Use:               "config",
		Short:             "Print the resolved configuration",
		Long:              fmt.Sprintf(configLongFmt, cli.TemplateFuncHelp()),
		Example:           configExample,
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfig(cmd, args, *flagConfig, flagFormat)
		},
	}

	cmd.Flags().StringVarP(
		&flagFormat,
		"format",
		"f",
		"",
		"Go text/template rendered against the resolved config instead of JSON",
	)

	parent.AddCommand(cmd)
}

func runConfig(cmd *cobra.Command, _ []string, flagConfig, flagFormat string) error {
	cfg, err := gahaku.LoadConfig(flagConfig)
	if err != nil {
		return err
	}
	// Presentation (JSON / template rendering) lives in pkg/gahaku/cli; ./cmd
	// only wires it to stdout. cmd.Println would route to stderr.
	return cli.RenderConfig(cmd.OutOrStdout(), cfg, flagFormat)
}
