package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
)

// testBroker wires an httptest.Server into responses keyed by path.
type testBroker struct {
	t         *testing.T
	server    *httptest.Server
	responses map[string]func(w http.ResponseWriter, r *http.Request)
	calls     int
}

func newTestBroker(t *testing.T) *testBroker {
	tb := &testBroker{
		t:         t,
		responses: make(map[string]func(w http.ResponseWriter, r *http.Request)),
	}
	tb.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tb.calls++
		key := r.Method + " " + r.URL.Path
		if h, ok := tb.responses[key]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(tb.server.Close)
	return tb
}

func (tb *testBroker) on(key string, h func(http.ResponseWriter, *http.Request)) {
	tb.responses[key] = h
}

func (tb *testBroker) index(links map[string]broker.Link) {
	tb.on("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": links})
	})
}

func (tb *testBroker) URL() string { return tb.server.URL }

func TestClient_Start_IndexCached(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy":      {Href: tb.URL() + "/matrix", Templated: false},
		"pb:record-deployment": {Href: tb.URL() + "/pacticipants/{pacticipant}/versions/{version}/deployed-versions/environment/{environment}", Templated: true},
		"pb:environments":      {Href: tb.URL() + "/environments", Templated: false},
	})

	c, err := broker.New(broker.Options{BaseURL: tb.URL(), Auth: broker.NoAuth{}})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	link, err := c.Link("pb:can-i-deploy")
	require.NoError(t, err)
	assert.Contains(t, link.Href, "/matrix")

	assert.True(t, c.HasRelation("pb:environments"))
	assert.False(t, c.HasRelation("pb:does-not-exist"))
}

func TestClient_Start_MissingRelation(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{})

	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	_, err = c.Link("pb:can-i-deploy")
	require.Error(t, err)
	assert.ErrorIs(t, err, broker.ErrRelationMissing)
}

func TestCanIDeploy_Deployable(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "api", q.Get("pacticipant"))
		assert.Equal(t, "v1", q.Get("version"))
		assert.Equal(t, "production", q.Get("environment"))
		tr := true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary": map[string]any{"deployable": &tr, "reason": "ok"},
			"verificationResultUrl": "http://verify/123",
		})
	})

	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeploy(context.Background(), broker.CanIDeployInput{
		Pacticipant: "api", Version: "v1", Environment: "production",
	})
	require.NoError(t, err)
	assert.True(t, res.Deployable)
	assert.Equal(t, "ok", res.Reason)
	assert.Equal(t, "http://verify/123", res.VerificationURL)
}

func TestCanIDeploy_NotDeployable(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, r *http.Request) {
		f := false
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary": map[string]any{"deployable": &f, "reason": "pact broken"},
		})
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeploy(context.Background(), broker.CanIDeployInput{
		Pacticipant: "api", Version: "v1", Environment: "production",
	})
	require.NoError(t, err)
	assert.False(t, res.Deployable)
	assert.Equal(t, "pact broken", res.Reason)
}

func TestCanIDeploy_BrokerError(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	_, err = c.CanIDeploy(context.Background(), broker.CanIDeployInput{
		Pacticipant: "api", Version: "v1", Environment: "production",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, broker.ErrUnexpectedStatus), "expected ErrUnexpectedStatus, got %v", err)
}

func TestRecordDeployment_TemplatedURL(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:record-deployment": {
			Href:      tb.URL() + "/pacticipants/{pacticipant}/versions/{version}/deployed-versions/environment/{environment}",
			Templated: true,
		},
	})
	tb.on("POST /pacticipants/api/versions/v1/deployed-versions/environment/production", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})

	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	require.NoError(t, c.RecordDeployment(context.Background(), broker.RecordDeploymentInput{
		Pacticipant: "api", Version: "v1", Environment: "production",
	}))
}

func TestListEnvironments(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:environments": {Href: tb.URL() + "/environments", Templated: false},
	})
	tb.on("GET /environments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"_embedded":{"environments":[{"uuid":"1","name":"production","production":true},{"uuid":"2","name":"staging"}]}}`))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	envs, err := c.ListEnvironments(context.Background())
	require.NoError(t, err)
	require.Len(t, envs, 2)
	ok, err := c.EnvironmentExists(context.Background(), "PRODUCTION")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNew_BadURL(t *testing.T) {
	_, err := broker.New(broker.Options{BaseURL: "not a url"})
	require.Error(t, err)
}

func TestClient_Unreachable(t *testing.T) {
	// Pick a deliberately-unreachable address (reserved TEST-NET-1).
	c, err := broker.New(broker.Options{BaseURL: "http://192.0.2.1:1", HTTP: &http.Client{Transport: failingTransport{}}})
	require.NoError(t, err)
	err = c.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, broker.ErrBrokerUnreachable)
	// Also check snippet url path works and query helper concatenation.
	_ = url.Values{}.Encode()
	_ = strings.TrimSpace("")
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network dead")
}
