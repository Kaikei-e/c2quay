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

// CanIDeploy asks the broker whether it's safe to ship the given pacticipant
// version to the given environment.
func (c *Client) CanIDeploy(ctx context.Context, in CanIDeployInput) (*CanIDeployResult, error) {
	link, err := c.Link("pb:can-i-deploy")
	if err != nil {
		return nil, err
	}
	href, err := link.ExpandTemplate(map[string]string{
		"pacticipant": in.Pacticipant,
		"version":     in.Version,
		"environment": in.Environment,
	})
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("pacticipant", in.Pacticipant)
	q.Set("version", in.Version)
	q.Set("environment", in.Environment)

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
