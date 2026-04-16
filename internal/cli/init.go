package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newInitCommand(rt *runtimeCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold a c2quay.yml in the current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(rt.stdout, "c2quay init is not implemented yet; copy docs/config.example.yml for now.")
			return &ExitError{Code: ExitOperatorError, Err: ErrNotImplemented}
		},
	}
}
