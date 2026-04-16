package broker

import (
	"context"
	"fmt"
)

// RecordDeploymentInput is what we POST to pb:record-deployment.
type RecordDeploymentInput struct {
	Pacticipant         string
	Version             string
	Environment         string
	ApplicationInstance string
}

// RecordDeployment marks `version` of `pacticipant` as deployed to `environment`.
// Must only be called after a successful deploy. Calling it prematurely will
// auto-undeploy the previous version on the broker (Pact's documented behavior).
func (c *Client) RecordDeployment(ctx context.Context, in RecordDeploymentInput) error {
	link, err := c.Link("pb:record-deployment")
	if err != nil {
		return err
	}
	href, err := link.ExpandTemplate(map[string]string{
		"pacticipant": in.Pacticipant,
		"version":     in.Version,
		"environment": in.Environment,
	})
	if err != nil {
		return err
	}
	body := map[string]string{
		"environment": in.Environment,
	}
	if in.ApplicationInstance != "" {
		body["applicationInstance"] = in.ApplicationInstance
	}
	if err := c.postJSON(ctx, href, body, nil); err != nil {
		return fmt.Errorf("record-deployment %s@%s -> %s: %w", in.Pacticipant, in.Version, in.Environment, err)
	}
	return nil
}
