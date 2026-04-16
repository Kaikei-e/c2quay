package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/lock"
	"github.com/Kaikei-e/c2quay/internal/output"
	"github.com/Kaikei-e/c2quay/internal/release"
)

func newRollbackCommand(rt *runtimeCtx) *cobra.Command {
	var (
		fromSnapshot string
		dryRun       bool
	)
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Restore compose services to a prior snapshot's images",
		Long: `Rollback re-applies the images recorded in a pre-deploy snapshot.

Unlike deploy's auto-rollback (which fires automatically after a failed
deploy), this command is a manual recovery tool: pick a snapshot file from
.c2quay/snapshots/ and re-apply it. record-deployment is NEVER called — the
broker's view is left as-is for the operator to reconcile.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.flags.envName == "" {
				return &ExitError{Code: ExitOperatorError, Err: errors.New("--env is required")}
			}
			if fromSnapshot == "" {
				return &ExitError{Code: ExitOperatorError, Err: errors.New("--from-snapshot is required")}
			}
			return runRollback(cmd.Context(), rt, fromSnapshot, dryRun)
		},
	}
	cmd.Flags().StringVar(&fromSnapshot, "from-snapshot", "", "path to the pre-deploy snapshot JSON to restore from")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without invoking docker compose")
	return cmd
}

func runRollback(ctx context.Context, rt *runtimeCtx, snapshotPath string, dryRun bool) error {
	tw := output.NewText(rt.stdout)
	log := rt.logger

	snap, err := release.LoadSnapshot(snapshotPath)
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf("load snapshot: %w", err)}
	}
	if snap.Env != rt.flags.envName {
		log.Warn("snapshot env does not match --env",
			slog.String("snapshot_env", snap.Env),
			slog.String("flag_env", rt.flags.envName),
		)
	}

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

	adapter := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: rt.cfg.Compose.Files,
		ProjectName:  rt.cfg.Compose.ProjectName,
		Logger:       log,
	})

	// Best-effort read of current images. If this fails, BuildRollbackPlan
	// falls back to "restore everything in the snapshot".
	var current map[string]string
	if rc, rerr := adapter.RenderConfigJSON(ctx); rerr == nil && rc != nil {
		current = rc.ImagesByService()
	} else if rerr != nil {
		log.Warn("render current compose config failed; restoring all services in snapshot",
			slog.String("err", rerr.Error()),
		)
	}

	plan, ok, reason := release.BuildRollbackPlan(snap, current)
	if !ok {
		tw.Warn("rollback", "nothing to do: "+reason)
		return nil
	}
	plan.FromSnapshotFile = snapshotPath

	mode := release.RollbackOn
	if dryRun {
		mode = release.RollbackDryRun
	}
	rep, rerr := release.ExecuteRollback(ctx, release.RollbackDeps{
		Compose:     adapter,
		Logger:      log,
		UI:          tw,
		Progress:    rt.stderr,
		SnapshotDir: release.DefaultSnapshotDir(),
		WaitTimeout: rt.cfg.Deploy.WaitTimeout,
	}, plan, mode)
	if rerr != nil {
		if rep != nil {
			release.RollbackHint{
				Env:             rt.flags.envName,
				FailedAt:        release.FailedStep("manual-rollback"),
				Cause:           rerr,
				PreSnapshot:     snap,
				PreSnapshotFile: snapshotPath,
				Rollback:        rep,
			}.Write(rt.stderr)
		}
		return &ExitError{Code: ExitOperatorError, Err: rerr}
	}
	return nil
}
