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
	renderComposeState(tw, rt.cfg.Compose.ProjectName, cs)

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
	if err := checkBrokerEnvironment(ctx, tw, bc, rt.flags.envName); err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	return nil
}

// renderComposeState prints one line per container: Ok when running and
// either healthy or health-unset, Warn otherwise (or when there are no
// containers at all). Extracted from runStatus so the formatting logic is
// unit-testable without a live docker daemon.
func renderComposeState(tw output.Writer, projectName string, cs []composeadapter.ContainerStatus) {
	if len(cs) == 0 {
		tw.Warn("compose state", "no containers for project "+projectName)
		return
	}
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

// environmentChecker is the subset of *broker.Client that
// checkBrokerEnvironment needs. Declared here (not in broker/) so tests can
// substitute a fake without a live broker.
type environmentChecker interface {
	HasRelation(rel string) bool
	EnvironmentExists(ctx context.Context, name string) (bool, error)
}

// checkBrokerEnvironment confirms envName is registered with the broker,
// printing the outcome via tw. It returns a non-nil error only when the
// EnvironmentExists call itself fails (a broker-reachability problem); an
// environment that is simply not registered is reported via tw.Fail but is
// not, by itself, a command failure — this mirrors runStatus's pre-extraction
// behaviour exactly. Extracted so it is unit-testable with a fake broker.
func checkBrokerEnvironment(ctx context.Context, tw output.Writer, bc environmentChecker, envName string) error {
	if !bc.HasRelation("pb:environments") {
		tw.Warn("broker environment", "broker does not expose pb:environments")
		return nil
	}
	exists, err := bc.EnvironmentExists(ctx, envName)
	if err != nil {
		tw.Fail("broker environment check", err.Error())
		return err
	}
	if exists {
		tw.Ok("broker environment", envName+" is known to the broker")
	} else {
		tw.Fail("broker environment", envName+" is NOT registered; run `pact-broker create-environment`")
	}
	return nil
}
