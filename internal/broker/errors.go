// Package broker talks to a Pact Broker using HAL navigation so that we never
// hard-code URLs. Brokers evolve their routes; c2quay follows _links instead.
package broker

import "errors"

var (
	// ErrRelationMissing indicates the broker's index resource does not
	// advertise a relation we need. Usually means the broker is too old
	// or the feature is disabled.
	ErrRelationMissing = errors.New("broker does not expose required relation")

	// ErrGateFailed means can-i-deploy said no.
	ErrGateFailed = errors.New("deployment gated by can-i-deploy")

	// ErrBrokerUnreachable is returned for transport-layer failures
	// (DNS, TCP, TLS, timeout).
	ErrBrokerUnreachable = errors.New("pact broker is unreachable")

	// ErrUnexpectedStatus wraps any non-2xx the broker returns.
	ErrUnexpectedStatus = errors.New("unexpected broker status")
)
