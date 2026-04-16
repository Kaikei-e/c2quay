package broker

import (
	"context"
	"fmt"
	"strings"
)

// Environment mirrors a single entry from the broker's environments list.
type Environment struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Production  bool   `json:"production"`
}

type environmentsDoc struct {
	Embedded struct {
		Environments []Environment `json:"environments"`
	} `json:"_embedded"`
}

// ListEnvironments returns all environments known to the broker.
func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	rel := "pb:environments"
	link, err := c.Link(rel)
	if err != nil {
		return nil, err
	}
	href, err := link.ExpandTemplate(nil)
	if err != nil {
		return nil, err
	}
	var doc environmentsDoc
	if err := c.getJSON(ctx, href, nil, &doc); err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return doc.Embedded.Environments, nil
}

// EnvironmentExists returns true if the broker has an environment with the
// exact name given.
func (c *Client) EnvironmentExists(ctx context.Context, name string) (bool, error) {
	envs, err := c.ListEnvironments(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range envs {
		if strings.EqualFold(e.Name, name) {
			return true, nil
		}
	}
	return false, nil
}
