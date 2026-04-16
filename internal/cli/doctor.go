package cli

import (
	"github.com/spf13/cobra"

	"github.com/Kaikei-e/c2quay/internal/doctor"
)

func newDoctorCommand(rt *runtimeCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the local environment (Docker daemon, Compose version, hyphen form)",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := doctor.Run(cmd.Context(), nil)
			if err := doctor.Render(rt.stdout, report, rt.flags.output); err != nil {
				return err
			}
			if !report.AllOK() {
				return &ExitError{Code: ExitOperatorError, Err: doctor.ErrFailed}
			}
			return nil
		},
	}
}
