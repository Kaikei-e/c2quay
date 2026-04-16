package cli

import (
	"github.com/spf13/cobra"
)

func newDeployCommand(_ *runtimeCtx) *cobra.Command {
	var (
		service string
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Gate, deploy via docker compose, then record the deployment",
		RunE: func(*cobra.Command, []string) error {
			return &ExitError{Code: ExitOperatorError, Err: ErrNotImplemented}
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "limit to a single service")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run lock+snapshot+gate only, do not invoke docker compose up")
	return cmd
}
