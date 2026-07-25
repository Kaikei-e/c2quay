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

// AggregateGateChecker extends GateChecker with the multi-selector matrix
// query used for all_or_nothing environments. Pipelines may type-assert on
// this to decide whether to fan out or batch into one request. Declared as
// a separate interface so legacy test fakes keep compiling.
type AggregateGateChecker interface {
	GateChecker
	CanIDeployMany(ctx context.Context, env string, selectors []broker.CanIDeploySelector) (*broker.CanIDeploySetResult, error)
}

// DeployBroker extends GateChecker with what the deploy pipeline needs.
type DeployBroker interface {
	AggregateGateChecker
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
	Pull(ctx context.Context, services []string, progress io.Writer) error
	Up(ctx context.Context, opts composeadapter.UpOptions, progress io.Writer) error
	PsJSON(ctx context.Context) ([]composeadapter.ContainerStatus, error)
	RenderConfigJSON(ctx context.Context) (*composeadapter.RenderedConfig, error)
	// ConfigServices backs plan-time gate_only coverage validation. See
	// ADR 0013.
	ConfigServices(ctx context.Context) ([]string, error)
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
