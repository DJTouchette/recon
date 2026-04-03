// Package embedded exports recon's CLI command tree for embedding in other tools.
package embedded

import (
	"github.com/djtouchette/recon/cmd/recon/cli"
	"github.com/spf13/cobra"
)

// NewCommand returns recon's root cobra command.
// Callers can execute it directly or attach it as a subcommand.
func NewCommand(version string) *cobra.Command {
	return cli.NewRootCmd(version)
}
