package broker

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// CanIDeploySelector is one candidate (pacticipant, version) tuple in an
// aggregate matrix query. Several selectors go together in a single request
// so the broker evaluates them as a set — the matrix endpoint then reports
// whether they are mutually compatible, rather than each being checked
// against whatever is currently deployed.
type CanIDeploySelector struct {
	Pacticipant string
	Version     string
}

// CanIDeployMatrixRow is one row of the matrix response body. Each row
// describes a consumer/provider pact and the latest verification result
// between them.
type CanIDeployMatrixRow struct {
	ConsumerName    string
	ConsumerVersion string
	ProviderName    string
	ProviderVersion string
	Verified        bool
	Success         bool
	VerificationURL string
	PactURL         string
}

// CanIDeploySetResult captures the aggregate verdict plus per-row details
// so callers can report which pacticipant is responsible when the set is
// not deployable.
type CanIDeploySetResult struct {
	Deployable bool
	Reason     string
	Unknown    int
	Rows       []CanIDeployMatrixRow
	BrokerURL  string
}

// PacticipantVerdict is one pacticipant's slice of the aggregate result,
// derived from the matrix rows in which it appears as consumer or provider.
type PacticipantVerdict struct {
	Pacticipant string
	Deployable  bool
	Reason      string
	VerifyURL   string
}

// CanIDeployMany queries the broker's matrix endpoint for a set of
// candidates. The broker evaluates the set together and its summary
// represents the deploy-as-a-group verdict — this is the fix for the
// monolithic rollout case where individual `can-i-deploy` calls wrongly
// assume every other service stays on its currently-deployed version.
//
// Requires the generic pb:can-i-deploy relation. The scope-specific
// pb:can-i-deploy-pacticipant-version-to-environment relation is a single-
// pacticipant shortcut and cannot represent a multi-selector query, so we
// do not try to use it here.
func (c *Client) CanIDeployMany(ctx context.Context, env string, selectors []CanIDeploySelector) (*CanIDeploySetResult, error) {
	if len(selectors) == 0 {
		return nil, fmt.Errorf("can-i-deploy many: no selectors supplied")
	}
	if !c.HasRelation(RelCanIDeployGeneric) {
		return nil, fmt.Errorf("%w: %q", ErrRelationMissing, RelCanIDeployGeneric)
	}
	link, err := c.Link(RelCanIDeployGeneric)
	if err != nil {
		return nil, err
	}

	base := stripQueryTemplate(link.Href)

	q := url.Values{}
	for _, s := range selectors {
		q.Add("q[][pacticipant]", s.Pacticipant)
		q.Add("q[][version]", s.Version)
	}
	q.Set("latestby", "cvp")
	q.Set("environment", env)

	var doc struct {
		Summary struct {
			Deployable *bool  `json:"deployable"`
			Reason     string `json:"reason"`
			Unknown    int    `json:"unknown"`
		} `json:"summary"`
		Matrix []struct {
			Consumer struct {
				Name    string `json:"name"`
				Version struct {
					Number string `json:"number"`
				} `json:"version"`
			} `json:"consumer"`
			Provider struct {
				Name    string `json:"name"`
				Version struct {
					Number string `json:"number"`
				} `json:"version"`
			} `json:"provider"`
			VerificationResult *struct {
				Success bool `json:"success"`
				Links   struct {
					Self Link `json:"self"`
				} `json:"_links"`
			} `json:"verificationResult"`
			Pact *struct {
				Links struct {
					Self Link `json:"self"`
				} `json:"_links"`
			} `json:"pact"`
		} `json:"matrix"`
	}
	if err := c.getJSON(ctx, base, q, &doc); err != nil {
		return nil, fmt.Errorf("can-i-deploy many -> %s: %w", env, err)
	}

	res := &CanIDeploySetResult{
		Reason:    doc.Summary.Reason,
		Unknown:   doc.Summary.Unknown,
		BrokerURL: base + "?" + q.Encode(),
	}
	if doc.Summary.Deployable != nil && *doc.Summary.Deployable {
		res.Deployable = true
	}
	for _, m := range doc.Matrix {
		row := CanIDeployMatrixRow{
			ConsumerName:    m.Consumer.Name,
			ConsumerVersion: m.Consumer.Version.Number,
			ProviderName:    m.Provider.Name,
			ProviderVersion: m.Provider.Version.Number,
		}
		if m.VerificationResult != nil {
			row.Verified = true
			row.Success = m.VerificationResult.Success
			row.VerificationURL = m.VerificationResult.Links.Self.Href
		}
		if m.Pact != nil {
			row.PactURL = m.Pact.Links.Self.Href
		}
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}

// PerPacticipantVerdicts attributes the set-level result to each selector.
// A pacticipant is deployable only if every matrix row it participates in
// has a successful verification. If no row mentions it, we report a missing
// verification record — safer to gate than to trust summary.deployable
// alone, because the operator still expects c2quay to show which candidate
// has unverified integrations.
func PerPacticipantVerdicts(res *CanIDeploySetResult, selectors []CanIDeploySelector) map[string]PacticipantVerdict {
	out := make(map[string]PacticipantVerdict, len(selectors))
	for _, s := range selectors {
		v := PacticipantVerdict{Pacticipant: s.Pacticipant}
		rows := rowsFor(res.Rows, s.Pacticipant)
		if len(rows) == 0 {
			v.Deployable = false
			v.Reason = fmt.Sprintf("no verification record between %s@%s and its integration partners", s.Pacticipant, s.Version)
			out[s.Pacticipant] = v
			continue
		}
		allOK := true
		var firstBad CanIDeployMatrixRow
		for _, r := range rows {
			if !r.Verified || !r.Success {
				allOK = false
				firstBad = r
				break
			}
		}
		if allOK {
			v.Deployable = true
			v.Reason = "verified against integration partners in this candidate set"
		} else {
			v.Deployable = false
			v.Reason = summariseBadRow(s.Pacticipant, firstBad)
			v.VerifyURL = firstBad.VerificationURL
		}
		out[s.Pacticipant] = v
	}
	return out
}

func rowsFor(rows []CanIDeployMatrixRow, name string) []CanIDeployMatrixRow {
	var out []CanIDeployMatrixRow
	for _, r := range rows {
		if r.ConsumerName == name || r.ProviderName == name {
			out = append(out, r)
		}
	}
	return out
}

func summariseBadRow(pacticipant string, r CanIDeployMatrixRow) string {
	var counterpart, role string
	if r.ConsumerName == pacticipant {
		role = "consumer"
		counterpart = fmt.Sprintf("%s@%s", r.ProviderName, r.ProviderVersion)
	} else {
		role = "provider"
		counterpart = fmt.Sprintf("%s@%s", r.ConsumerName, r.ConsumerVersion)
	}
	if !r.Verified {
		return fmt.Sprintf("no verification result as %s against %s", role, counterpart)
	}
	return fmt.Sprintf("failed verification as %s against %s", role, counterpart)
}

// stripQueryTemplate removes an RFC 6570 level-3 query form like
// "{?pacticipant,version,environment}" from a templated href. Our generic
// ExpandTemplate only handles level-1 (path vars), and the matrix endpoint
// advertises the query variables as a template the broker expects the
// client to fill in. We build the query ourselves, so the query template
// is informational — we only need the base path.
func stripQueryTemplate(href string) string {
	if base, _, found := strings.Cut(href, "{?"); found {
		return base
	}
	return href
}
