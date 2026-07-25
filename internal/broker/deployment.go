package broker

import (
	"context"
	"fmt"
)

// RecordDeploymentInput is what we POST to pb:record-deployment.
//
// Per the Pact Broker API docs, the environment is already resolved into the
// URL path (via either the legacy root-level template or the nested link
// under pb:pacticipant-version). The request body carries deployment state,
// not the environment itself.
type RecordDeploymentInput struct {
	Pacticipant         string
	Version             string
	Environment         string
	ApplicationInstance string
	// ReplacedPreviousDeployedVersion maps to the broker field of the same
	// name. nil means "let the broker use its default", which is true — the
	// broker automatically marks the previous version as undeployed. This is
	// the behaviour ADR 0004 relies on, so the default matches our model.
	ReplacedPreviousDeployedVersion *bool
}

// Relation names used by the record-deployment flow.
const (
	RelRecordDeployment   = "pb:record-deployment"
	RelPacticipantVersion = "pb:pacticipant-version"
)

// RecordDeployment marks `version` of `pacticipant` as deployed to `environment`.
// Must only be called after a successful deploy. Calling it prematurely will
// auto-undeploy the previous version on the broker (Pact's documented behavior).
//
// The Pact Broker's root index does not expose a top-level
// `pb:record-deployment` in current releases — the link lives on each
// pacticipant-version resource. We therefore traverse `pb:pacticipant-version`
// first, fetch the resolved document, and POST to the `pb:record-deployment`
// link found there. If the broker still publishes the legacy top-level link
// (older self-hosted forks), we use that directly.
func (c *Client) RecordDeployment(ctx context.Context, in RecordDeploymentInput) error {
	href, err := c.resolveRecordDeploymentURL(ctx, in)
	if err != nil {
		return fmt.Errorf("record-deployment %s@%s -> %s: %w", in.Pacticipant, in.Version, in.Environment, err)
	}
	body := map[string]any{}
	if in.ApplicationInstance != "" {
		body["applicationInstance"] = in.ApplicationInstance
	}
	if in.ReplacedPreviousDeployedVersion != nil {
		body["replacedPreviousDeployedVersion"] = *in.ReplacedPreviousDeployedVersion
	}
	if err := c.postJSON(ctx, href, body, nil); err != nil {
		return fmt.Errorf("record-deployment %s@%s -> %s: %w", in.Pacticipant, in.Version, in.Environment, err)
	}
	return nil
}

// resolveRecordDeploymentURL picks the legacy root-level link when the broker
// exposes it; otherwise it performs the HAL two-step via pb:pacticipant-version.
func (c *Client) resolveRecordDeploymentURL(ctx context.Context, in RecordDeploymentInput) (string, error) {
	vars := map[string]string{
		"pacticipant": in.Pacticipant,
		"version":     in.Version,
		"environment": in.Environment,
	}

	if c.HasRelation(RelRecordDeployment) {
		link, err := c.Link(RelRecordDeployment)
		if err != nil {
			return "", err
		}
		return link.ExpandTemplate(vars)
	}

	if !c.HasRelation(RelPacticipantVersion) {
		return "", fmt.Errorf("%w: neither %q nor %q on broker index",
			ErrRelationMissing, RelRecordDeployment, RelPacticipantVersion)
	}

	pvLink, err := c.Link(RelPacticipantVersion)
	if err != nil {
		return "", err
	}
	pvURL, err := pvLink.ExpandTemplate(vars)
	if err != nil {
		return "", err
	}

	// Fetch the pacticipant-version resource and follow its
	// pb:record-deployment link. That link typically embeds {environment}
	// as a URI template so the environment still needs expansion here.
	// Reusing Index lets us survive array-shaped HAL link values and curies,
	// same as for the root index.
	var resource Index
	if err := c.getJSON(ctx, pvURL, nil, &resource); err != nil {
		return "", fmt.Errorf("resolve %s: %w", RelPacticipantVersion, err)
	}
	nested, ok := resource.Links[RelRecordDeployment]
	if !ok {
		return "", fmt.Errorf("%w: %q under pacticipant-version resource %s",
			ErrRelationMissing, RelRecordDeployment, pvURL)
	}
	return nested.ExpandTemplate(vars)
}
