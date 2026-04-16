package broker

import (
	"context"
	"fmt"
	"net/url"
)

// CanIDeployInput describes what we want to ship where. A single tuple of
// pacticipant + version + environment is checked per call.
type CanIDeployInput struct {
	Pacticipant string
	Version     string
	Environment string
}

// CanIDeployResult captures the broker's verdict plus metadata we surface
// in logs, output, and rollback hints.
type CanIDeployResult struct {
	Deployable      bool
	Reason          string
	VerificationURL string
	BrokerURL       string
}

// Relation names exposed by the Pact Broker index. Modern brokers split
// can-i-deploy into scope-specific relations; older / self-hosted forks
// still expose the generic one. We try the scoped relation first.
const (
	RelCanIDeployToEnvironment = "pb:can-i-deploy-pacticipant-version-to-environment"
	RelCanIDeployGeneric       = "pb:can-i-deploy"
)

// CanIDeploy asks the broker whether it's safe to ship the given pacticipant
// version to the given environment.
//
// Two broker shapes are supported:
//
//   - Modern: the index exposes `pb:can-i-deploy-pacticipant-version-to-environment`
//     with a URI template that embeds {pacticipant}/{version}/{environment} in
//     the path. We expand the template and GET without query parameters.
//   - Legacy: the index exposes `pb:can-i-deploy` as a matrix endpoint that
//     takes the three values as query parameters. We send them that way.
//
// If neither relation is present, ErrRelationMissing is returned.
func (c *Client) CanIDeploy(ctx context.Context, in CanIDeployInput) (*CanIDeployResult, error) {
	href, useQuery, err := c.resolveCanIDeployURL(in)
	if err != nil {
		return nil, err
	}

	var q url.Values
	if useQuery {
		q = url.Values{}
		q.Set("pacticipant", in.Pacticipant)
		q.Set("version", in.Version)
		q.Set("environment", in.Environment)
	}

	var doc struct {
		Summary struct {
			Deployable *bool  `json:"deployable"`
			Reason     string `json:"reason"`
		} `json:"summary"`
		VerificationResultURL string `json:"verificationResultUrl"`
	}
	if err := c.getJSON(ctx, href, q, &doc); err != nil {
		return nil, fmt.Errorf("can-i-deploy %s@%s -> %s: %w", in.Pacticipant, in.Version, in.Environment, err)
	}
	res := &CanIDeployResult{
		Reason:          doc.Summary.Reason,
		VerificationURL: doc.VerificationResultURL,
		BrokerURL:       href,
	}
	if doc.Summary.Deployable != nil && *doc.Summary.Deployable {
		res.Deployable = true
	}
	return res, nil
}

// resolveCanIDeployURL picks whichever can-i-deploy relation the broker
// publishes and returns the ready-to-GET URL plus whether the legacy
// query-string form is in play.
func (c *Client) resolveCanIDeployURL(in CanIDeployInput) (string, bool, error) {
	vars := map[string]string{
		"pacticipant": in.Pacticipant,
		"version":     in.Version,
		"environment": in.Environment,
	}
	if c.HasRelation(RelCanIDeployToEnvironment) {
		link, err := c.Link(RelCanIDeployToEnvironment)
		if err != nil {
			return "", false, err
		}
		href, err := link.ExpandTemplate(vars)
		if err != nil {
			return "", false, err
		}
		return href, false, nil
	}
	if c.HasRelation(RelCanIDeployGeneric) {
		link, err := c.Link(RelCanIDeployGeneric)
		if err != nil {
			return "", false, err
		}
		href, err := link.ExpandTemplate(vars)
		if err != nil {
			return "", false, err
		}
		return href, true, nil
	}
	return "", false, fmt.Errorf("%w: %q or %q", ErrRelationMissing, RelCanIDeployToEnvironment, RelCanIDeployGeneric)
}
