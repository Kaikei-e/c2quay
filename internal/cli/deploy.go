package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/lock"
	"github.com/Kaikei-e/c2quay/internal/output"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

func newDeployCommand(rt *runtimeCtx) *cobra.Command {
	var (
		service          string
		dryRun           bool
		autoRollbackFlag string
	)
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Gate, deploy via docker compose, then record the deployment",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.flags.envName == "" {
				return &ExitError{Code: ExitOperatorError, Err: errors.New("--env is required")}
			}
			mode, err := release.ParseRollbackMode(autoRollbackFlag)
			if err != nil {
				return &ExitError{Code: ExitOperatorError, Err: err}
			}
			return runDeploy(cmd.Context(), rt, service, dryRun, mode)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "limit to a single service")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run lock+snapshot+gate only, do not invoke docker compose up")
	cmd.Flags().StringVar(&autoRollbackFlag, "auto-rollback", "on",
		"on failure, restore pre-deploy images. values: on (default) | off | dry-run")
	return cmd
}

func runDeploy(ctx context.Context, rt *runtimeCtx, onlyService string, dryRun bool, rollbackMode release.RollbackMode) error {
	tw := output.NewText(rt.stdout)
	log := rt.logger

	// Acquire the environment lock BEFORE any side-effect.
	lockPath := filepath.Join(".c2quay", "locks", rt.flags.envName+".lock")
	envLock, err := lock.Acquire(lockPath)
	if err != nil {
		log.Error("lock acquire failed", slog.String("err", err.Error()))
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	defer func() {
		if rerr := envLock.Release(); rerr != nil {
			log.Warn("lock release failed", slog.String("err", rerr.Error()))
		}
	}()
	tw.Ok("environment lock", lockPath)
	log.Info("step completed", slog.String("step", "lock"), slog.String("path", lockPath))

	adapter := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: rt.cfg.Compose.Files,
		ProjectName:  rt.cfg.Compose.ProjectName,
		Logger:       log,
	})
	strat, err := versioning.Factory(rt.cfg, adapter)
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}

	bc, err := broker.New(broker.Options{
		BaseURL: rt.cfg.Broker.BaseURL,
		Auth:    broker.ResolveAuth(rt.cfg.Broker.Username(), rt.cfg.Broker.Password(), rt.cfg.Broker.Token()),
		Logger:  log,
	})
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	if err := bc.Start(ctx); err != nil {
		log.Error("broker contact failed", slog.String("url", rt.cfg.Broker.BaseURL), slog.String("err", err.Error()))
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	if env, ok := rt.cfg.LookupEnvironment(rt.flags.envName); ok && env.AllOrNothing && !bc.HasRelation(broker.RelCanIDeployGeneric) {
		return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf(
			"environment %q sets all_or_nothing: true but broker does not expose %q (required for aggregate can-i-deploy)",
			rt.flags.envName, broker.RelCanIDeployGeneric,
		)}
	}
	log.Info("step completed", slog.String("step", "broker-start"), slog.String("url", rt.cfg.Broker.BaseURL))

	report, derr := release.Deploy(ctx, rt.cfg, rt.flags.envName, onlyService, dryRun, release.DeployDeps{
		Broker:       bc,
		Compose:      adapter,
		Strategy:     strat,
		Logger:       log,
		UI:           tw,
		Progress:     rt.stderr,
		Stderr:       rt.stderr,
		SnapshotDir:  release.DefaultSnapshotDir(),
		RollbackMode: rollbackMode,
	})
	if derr != nil {
		if report != nil && report.FailedAtStep != "" {
			release.RollbackHint{
				Env:             rt.flags.envName,
				FailedAt:        report.FailedAtStep,
				Cause:           report.FailedCause,
				PreSnapshot:     report.Pre,
				PreSnapshotFile: report.PreSnapshotFile,
				PostSnapshot:    report.Post,
				Rollback:        report.Rollback,
			}.Write(rt.stderr)
		}
		return classifyDeployError(derr, report)
	}
	return nil
}

func classifyDeployError(err error, report *release.DeployReport) error {
	if report != nil && report.FailedAtStep == release.StepGate && errors.Is(err, broker.ErrGateFailed) {
		return &ExitError{Code: ExitGateFailed, Err: err}
	}
	return &ExitError{Code: ExitOperatorError, Err: err}
}
