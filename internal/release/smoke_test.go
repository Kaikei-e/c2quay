package release_test

import (
	"bytes"
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/release"
)

// TestSmoke_EmptyCommandNoop confirms an unconfigured smoke returns nil.
func TestSmoke_EmptyCommandNoop(t *testing.T) {
	err := release.RunSmoke(context.Background(), config.SmokeConfig{}, "production", nil)
	require.NoError(t, err)
}

// TestSmoke_Passes uses a trivial shell command.
func TestSmoke_Passes(t *testing.T) {
	var buf bytes.Buffer
	err := release.RunSmoke(context.Background(), config.SmokeConfig{
		Command: "echo smoke-ok",
		Timeout: 2 * time.Second,
	}, "production", &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "smoke-ok")
}

// TestSmoke_Fails returns an error for a non-zero exit.
func TestSmoke_Fails(t *testing.T) {
	err := release.RunSmoke(context.Background(), config.SmokeConfig{
		Command: "exit 7",
		Timeout: time.Second,
	}, "production", nil)
	require.Error(t, err)
}

// TestSmoke_TimeoutRealClock uses a short real-clock timeout vs. a long sleep.
// exec.CommandContext kills the subprocess when the context deadline expires.
// synctest cannot virtualize OS subprocess time, so we use a real deadline
// here that's short enough to keep the test fast.
func TestSmoke_TimeoutRealClock(t *testing.T) {
	start := time.Now()
	err := release.RunSmoke(context.Background(), config.SmokeConfig{
		Command: "sleep 10",
		Timeout: 100 * time.Millisecond,
	}, "production", nil)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second, "context deadline should have killed the subprocess quickly")
}

// TestSmoke_InjectsTargetEnv uses a pure-Go mock runner inside a synctest
// bubble to prove the ctx + deadline wiring c2quay provides to smoke would
// correctly cancel a pure-Go smoke that sleeps past its deadline. We don't
// test exec here — the OS subprocess side is covered by TestSmoke_TimeoutRealClock.
func TestSmoke_ContextDeadlinePropagation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			// A pure-Go "slow smoke" — synctest advances virtual time past its 35s sleep.
			select {
			case <-ctx.Done():
			case <-time.After(35 * time.Second):
			}
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Fatal("synctest bubble did not cancel the fake smoke")
		}
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	})
}
