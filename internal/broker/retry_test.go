package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
)

// fastRetry keeps these tests from actually waiting out the (500ms base)
// production backoff.
const fastRetry = time.Millisecond

// TestClient_Retries503ThenSucceeds proves a 5xx response is retried and a
// later success is returned to the caller.
func TestClient_Retries503ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": map[string]any{}})
	}))
	t.Cleanup(srv.Close)

	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: fastRetry})
	require.NoError(t, err)

	require.NoError(t, c.Start(context.Background()))
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "expected exactly 3 attempts (2 failures + 1 success)")
}

// TestClient_Retries429ThenSucceeds proves 429 (rate limited) is retried.
func TestClient_Retries429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": map[string]any{}})
	}))
	t.Cleanup(srv.Close)

	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: fastRetry})
	require.NoError(t, err)

	require.NoError(t, c.Start(context.Background()))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

// TestClient_RetriesNetworkErrorThenSucceeds proves transport-level
// (network) errors are retried, not just non-2xx HTTP responses.
func TestClient_RetriesNetworkErrorThenSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": map[string]any{}})
	}))
	t.Cleanup(srv.Close)

	ft := &flakyTransport{failCount: 2, inner: http.DefaultTransport}
	c, err := broker.New(broker.Options{
		BaseURL:        srv.URL,
		RetryBaseDelay: fastRetry,
		HTTP:           &http.Client{Transport: ft},
	})
	require.NoError(t, err)

	require.NoError(t, c.Start(context.Background()))
	assert.Equal(t, 3, ft.calls, "expected exactly 3 transport attempts (2 network failures + 1 success)")
}

// TestClient_DoesNotRetry400 proves a non-429 4xx is a permanent failure:
// exactly one attempt, no retry loop wasted on a request that will never
// succeed by repeating it.
func TestClient_DoesNotRetry400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	t.Cleanup(srv.Close)

	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: fastRetry})
	require.NoError(t, err)

	err = c.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, broker.ErrUnexpectedStatus)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a 4xx (non-429) must not be retried")
}

// TestClient_ExhaustsRetriesThenFails proves the retry budget is bounded:
// a persistently-failing broker gets exactly 3 attempts, then the caller
// sees the error.
func TestClient_ExhaustsRetriesThenFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: fastRetry})
	require.NoError(t, err)

	err = c.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, broker.ErrUnexpectedStatus)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "expected exactly maxAttempts (3) HTTP calls")
}

// TestClient_HonorsContextCancellationBetweenAttempts proves that a context
// which expires while the retry loop is sleeping between attempts aborts the
// retry loop instead of sleeping it out and trying again.
func TestClient_HonorsContextCancellationBetweenAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	// RetryBaseDelay is large relative to the context timeout, so the first
	// backoff sleep will not complete before ctx expires.
	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: 200 * time.Millisecond})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err = c.Start(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "only the initial attempt should fire before ctx expires during backoff")
}

// TestRecordDeployment_Retries503ThenSucceeds exercises the retry policy
// through the one real POST path this client has (record-deployment),
// proving the "idempotent POST is retried too" half of ADR 0014.
func TestRecordDeployment_Retries503ThenSucceeds(t *testing.T) {
	var postCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pacticipants/api/versions/v1/deployed-versions/environment/production", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&postCalls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": map[string]any{
			"pb:record-deployment": map[string]any{
				"href":      srv.URL + "/pacticipants/{pacticipant}/versions/{version}/deployed-versions/environment/{environment}",
				"templated": true,
			},
		}})
	})

	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: fastRetry})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	err = c.RecordDeployment(context.Background(), broker.RecordDeploymentInput{
		Pacticipant: "api", Version: "v1", Environment: "production",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&postCalls), "expected exactly 3 POST attempts (2 failures + 1 success)")
}

// TestRecordDeployment_DoesNotRetry404 proves the idempotent-POST retry
// policy still excludes permanent client errors.
func TestRecordDeployment_DoesNotRetry404(t *testing.T) {
	var postCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/pacticipants/api/versions/v1/deployed-versions/environment/production", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&postCalls, 1)
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": map[string]any{
			"pb:record-deployment": map[string]any{
				"href":      srv.URL + "/pacticipants/{pacticipant}/versions/{version}/deployed-versions/environment/{environment}",
				"templated": true,
			},
		}})
	})

	c, err := broker.New(broker.Options{BaseURL: srv.URL, RetryBaseDelay: fastRetry})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	err = c.RecordDeployment(context.Background(), broker.RecordDeploymentInput{
		Pacticipant: "api", Version: "v1", Environment: "production",
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&postCalls), "a 404 must not be retried")
}

// flakyTransport simulates a transport that fails at the network level for
// its first N round trips, then delegates to a real transport.
type flakyTransport struct {
	failCount int
	calls     int
	inner     http.RoundTripper
}

func (f *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls <= f.failCount {
		return nil, errors.New("connection reset by peer")
	}
	return f.inner.RoundTrip(req)
}
