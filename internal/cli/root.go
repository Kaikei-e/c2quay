package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/logging"
)

const (
	ExitOK            = 0
	ExitGateFailed    = 1
	ExitOperatorError = 2
)

var (
	ErrNotImplemented = errors.New("not implemented yet")
)

type globalFlags struct {
	configPath string
	envName    string
	output     string
	logLevel   string
	auditLog   string
}

type runtimeCtx struct {
	cfg    *config.Config
	logger *slog.Logger
	flags  globalFlags
	stdout io.Writer
	stderr io.Writer
}

func newRootCommand() (*cobra.Command, *runtimeCtx) {
	rt := &runtimeCtx{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	root := &cobra.Command{
		Use:           "c2quay",
		Short:         "Contract-gated releases for Docker Compose",
		Long:          "c2quay sits between your Pact Broker and docker compose, gating releases on can-i-deploy and recording successful deploys back to the broker.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVarP(&rt.flags.configPath, "config", "c", "c2quay.yml", "path to c2quay config file")
	root.PersistentFlags().StringVarP(&rt.flags.envName, "env", "e", "", "environment name as defined in config")
	root.PersistentFlags().StringVarP(&rt.flags.output, "output", "o", "text", "output format: text or json")
	root.PersistentFlags().StringVar(&rt.flags.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	root.PersistentFlags().StringVar(&rt.flags.auditLog, "audit-log", "", "append JSON Lines audit log to this file (optional)")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		logger, err := logging.New(logging.Options{
			Level:    rt.flags.logLevel,
			AuditLog: rt.flags.auditLog,
			Stderr:   rt.stderr,
		})
		if err != nil {
			return fmt.Errorf("init logger: %w", err)
		}
		rt.logger = logger

		// doctor と version は config 不要
		if cmd.Name() == "doctor" || cmd.Name() == "version" || cmd.Name() == "init" {
			return nil
		}

		cfg, err := config.Load(rt.flags.configPath)
		if err != nil {
			return fmt.Errorf("load config %q: %w", rt.flags.configPath, err)
		}
		rt.cfg = cfg
		return nil
	}

	root.AddCommand(
		newVersionCommand(rt),
		newInitCommand(rt),
		newDoctorCommand(rt),
		newVerifyCommand(rt),
		newDeployCommand(rt),
		newRollbackCommand(rt),
		newStatusCommand(rt),
	)

	return root, rt
}

// Execute parses argv and runs the appropriate command, returning a process exit code.
func Execute(ctx context.Context) int {
	root, rt := newRootCommand()
	root.SetContext(ctx)

	if err := root.ExecuteContext(ctx); err != nil {
		return classifyError(err, rt.stderr)
	}
	return ExitOK
}

type exitCoder interface {
	ExitCode() int
}

func classifyError(err error, stderr io.Writer) int {
	fmt.Fprintf(stderr, "error: %v\n", err)
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return ExitOperatorError
}

// ExitError wraps an error with an explicit process exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
func (e *ExitError) ExitCode() int { return e.Code }
