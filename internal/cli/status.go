package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/output"
)

func newStatusCommand(rt *runtimeCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show compose project state and broker environment visibility for this env",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.flags.envName == "" {
				return &ExitError{Code: ExitOperatorError, Err: errors.New("--env is required")}
			}
			return runStatus(cmd.Context(), rt)
		},
	}
}

func runStatus(ctx context.Context, rt *runtimeCtx) error {
	tw := output.NewText(rt.stdout)

	adapter := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: rt.cfg.Compose.Files,
		ProjectName:  rt.cfg.Compose.ProjectName,
		Logger:       rt.logger,
	})
	cs, err := adapter.PsJSON(ctx)
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	if len(cs) == 0 {
		tw.Warn("compose state", "no containers for project "+rt.cfg.Compose.ProjectName)
	} else {
		for _, c := range cs {
			label := fmt.Sprintf("container %s (service %s)", c.Name, c.Service)
			detail := fmt.Sprintf("state=%s health=%s status=%s", c.State, c.Health, c.Status)
			if c.State == "running" && (c.Health == "healthy" || c.Health == "") {
				tw.Ok(label, detail)
			} else {
				tw.Warn(label, detail)
			}
		}
	}

	// Confirm the environment exists in the broker. Useful for catching typos.
	bc, err := broker.New(broker.Options{
		BaseURL: rt.cfg.Broker.BaseURL,
		Auth:    broker.ResolveAuth(rt.cfg.Broker.Username(), rt.cfg.Broker.Password(), rt.cfg.Broker.Token()),
		Logger:  rt.logger,
	})
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	if err := bc.Start(ctx); err != nil {
		tw.Fail("broker reachable", err.Error())
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	if bc.HasRelation("pb:environments") {
		exists, err := bc.EnvironmentExists(ctx, rt.flags.envName)
		if err != nil {
			tw.Fail("broker environment check", err.Error())
			return &ExitError{Code: ExitOperatorError, Err: err}
		}
		if exists {
			tw.Ok("broker environment", rt.flags.envName+" is known to the broker")
		} else {
			tw.Fail("broker environment", rt.flags.envName+" is NOT registered; run `pact-broker create-environment`")
		}
	} else {
		tw.Warn("broker environment", "broker does not expose pb:environments")
	}
	return nil
}
