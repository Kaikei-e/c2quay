package cli

import (
	"github.com/spf13/cobra"
)

func newStatusCommand(_ *runtimeCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show compose project state and broker environment visibility for this env",
		RunE: func(*cobra.Command, []string) error {
			return &ExitError{Code: ExitOperatorError, Err: ErrNotImplemented}
		},
	}
}
