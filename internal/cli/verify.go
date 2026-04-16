package cli

import (
	"github.com/spf13/cobra"
)

func newVerifyCommand(_ *runtimeCtx) *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check whether a deploy would be safe according to the Pact Broker",
		RunE: func(*cobra.Command, []string) error {
			return &ExitError{Code: ExitOperatorError, Err: ErrNotImplemented}
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "limit to a single service")
	return cmd
}
