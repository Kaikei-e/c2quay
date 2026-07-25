package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/output"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

func newVerifyCommand(rt *runtimeCtx) *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check whether a deploy would be safe according to the Pact Broker",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.flags.envName == "" {
				return &ExitError{Code: ExitOperatorError, Err: errors.New("--env is required")}
			}
			return runVerify(cmd.Context(), rt, service)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "limit to a single service")
	return cmd
}

func runVerify(ctx context.Context, rt *runtimeCtx, onlyService string) error {
	adapter := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: rt.cfg.Compose.Files,
		ProjectName:  rt.cfg.Compose.ProjectName,
		Logger:       rt.logger,
	})
	strat, err := versioning.Factory(rt.cfg, adapter)
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}

	bc, err := broker.New(broker.Options{
		BaseURL: rt.cfg.Broker.BaseURL,
		Auth:    broker.ResolveAuth(rt.cfg.Broker.Username(), rt.cfg.Broker.Password(), rt.cfg.Broker.Token()),
		Logger:  rt.logger,
	})
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}
	if err := bc.Start(ctx); err != nil {
		return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf("contact broker: %w", err)}
	}
	if !bc.HasRelation(broker.RelCanIDeployToEnvironment) && !bc.HasRelation(broker.RelCanIDeployGeneric) {
		return &ExitError{Code: ExitOperatorError, Err: fmt.Errorf(
			"broker lacks can-i-deploy relation (tried %q and %q)",
			broker.RelCanIDeployToEnvironment, broker.RelCanIDeployGeneric,
		)}
	}

	report, err := release.Verify(ctx, rt.cfg, rt.flags.envName, onlyService, release.VerifyDeps{
		Broker:   bc,
		Strategy: strat,
		// Compose is wired so verify opportunistically runs the same
		// coverage check deploy does (ADR 0013 "Verify parity"). This is
		// best-effort: an unreachable compose CLI degrades to a printed
		// notice (see release.VerifyReport.CoverageNotice) rather than
		// failing verify outright, so it stays usable on broker-only boxes.
		Compose: adapter,
	})
	if err != nil {
		return &ExitError{Code: ExitOperatorError, Err: err}
	}

	if rt.flags.output == "json" {
		if err := emitVerifyJSON(rt, report); err != nil {
			return err
		}
	} else {
		emitVerifyText(rt, report)
	}

	if !report.AllPassed() {
		if firstErr := report.FirstError(); firstErr != nil {
			return &ExitError{Code: ExitOperatorError, Err: firstErr}
		}
		return &ExitError{Code: ExitGateFailed, Err: broker.ErrGateFailed}
	}
	return nil
}

func emitVerifyText(rt *runtimeCtx, r *release.VerifyReport) {
	w := output.NewText(rt.stdout)
	w.Step(fmt.Sprintf("Resolving versions for %s", r.Plan.Env), joinServices(r.Plan))
	pass, fail := 0, 0
	if r.CoverageNotice != "" {
		w.Warn("compose-coverage", r.CoverageNotice)
	}
	if r.CoverageErr != nil {
		w.Fail("compose-coverage", r.CoverageErr.Error())
		fail++
	}
	for _, o := range r.Outcomes {
		label := fmt.Sprintf("can-i-deploy: %s@%s → %s", o.Pacticipant, o.Release.Version, r.Plan.Env)
		switch {
		case o.Err != nil:
			w.Fail(label, o.Err.Error())
			fail++
		case o.Deployable:
			w.Ok(label, "safe")
			pass++
		default:
			detail := o.Reason
			if o.VerifyURL != "" {
				detail = fmt.Sprintf("%s (see %s)", detail, o.VerifyURL)
			}
			w.Fail(label, detail)
			fail++
		}
	}
	w.Summary(pass, fail)
}

func emitVerifyJSON(rt *runtimeCtx, r *release.VerifyReport) error {
	report := output.Report{Env: r.Plan.Env, Command: "verify", ComposeCoverageNotice: r.CoverageNotice}
	if r.CoverageErr != nil {
		report.Results = append(report.Results, output.ServiceResult{
			Service: "compose-coverage",
			Verdict: "error",
			Reason:  r.CoverageErr.Error(),
		})
		report.Summary.Fail++
	}
	for _, o := range r.Outcomes {
		verdict := "pass"
		reason := o.Reason
		switch {
		case o.Err != nil:
			verdict = "error"
			reason = o.Err.Error()
		case !o.Deployable:
			verdict = "fail"
		}
		report.Results = append(report.Results, output.ServiceResult{
			Service:     o.Service,
			Pacticipant: o.Pacticipant,
			Version:     o.Release.Version,
			ImageRef:    o.Release.ImageRef,
			Verdict:     verdict,
			Reason:      reason,
			BrokerURL:   o.VerifyURL,
		})
		if verdict == "pass" {
			report.Summary.Pass++
		} else {
			report.Summary.Fail++
		}
	}
	return output.WriteJSON(rt.stdout, report)
}

func joinServices(plan *release.Plan) string {
	var b strings.Builder
	for i, s := range plan.Services {
		if i > 0 {
			b.WriteString(", ")
		}
		rel := plan.Releases[s]
		fmt.Fprintf(&b, "%s@%s", s, rel.Version)
	}
	return b.String()
}
