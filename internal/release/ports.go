package release

import (
	"context"
	"io"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/composeadapter"
)

// GateChecker is what gate-check logic needs from a Pact Broker client.
// Defined here (not in broker/) so release/ depends only on behaviour,
// per ISP and the "accept interfaces" idiom.
type GateChecker interface {
	CanIDeploy(ctx context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error)
}

// DeployBroker extends GateChecker with what the deploy pipeline needs.
type DeployBroker interface {
	GateChecker
	RecordDeployment(ctx context.Context, in broker.RecordDeploymentInput) error
	HasRelation(rel string) bool
	APICallCount() int
}

// ComposeDeployer is the subset of the Compose adapter used during deploy.
//
// RenderConfigJSON is required so pre-deploy snapshots can capture the
// resolved {service → image} map, which the rollback flow uses to pin
// services back to their previous images.
type ComposeDeployer interface {
	Up(ctx context.Context, opts composeadapter.UpOptions, progress io.Writer) error
	PsJSON(ctx context.Context) ([]composeadapter.ContainerStatus, error)
	RenderConfigJSON(ctx context.Context) (*composeadapter.RenderedConfig, error)
}

// UI is the progressive output surface used by orchestrators. output.Writer
// satisfies it, but release/ does not import output/ directly so tests can
// supply a trivial fake.
type UI interface {
	Step(label, detail string)
	Ok(label, detail string)
	Fail(label, detail string)
	Warn(label, detail string)
}
