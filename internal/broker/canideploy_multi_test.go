package broker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
)

// matrixResponse is the shape the tests feed the fake broker with. Built
// with map[string]any so individual tests can omit fields without a
// bespoke struct per scenario.
func matrixResponse(deployable *bool, reason string, rows ...map[string]any) map[string]any {
	return map[string]any{
		"summary": map[string]any{"deployable": deployable, "reason": reason},
		"matrix":  rows,
	}
}

func pactRow(consumer, cver, provider, pver string, verified *bool, verifyURL string) map[string]any {
	row := map[string]any{
		"consumer": map[string]any{"name": consumer, "version": map[string]any{"number": cver}},
		"provider": map[string]any{"name": provider, "version": map[string]any{"number": pver}},
	}
	if verified != nil {
		row["verificationResult"] = map[string]any{
			"success": *verified,
			"_links":  map[string]any{"self": map[string]any{"href": verifyURL}},
		}
	}
	return row
}

func TestCanIDeployMany_BuildsInterleavedBracketArrayQuery(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, r *http.Request) {
		// Values decode correctly on the server side regardless of
		// interleave, so cover both: (1) the parsed form is right,
		// (2) the raw query preserves pair-wise order. Pact Broker
		// returns 400 if pacticipant and version are not interleaved
		// because the Rack q[] parser pairs them by URL position.
		q := r.URL.Query()
		assert.Equal(t, []string{"api", "web"}, q["q[][pacticipant]"])
		assert.Equal(t, []string{"v1", "v2"}, q["q[][version]"])
		assert.Equal(t, "cvp", q.Get("latestby"))
		assert.Equal(t, "production", q.Get("environment"))

		raw := r.URL.RawQuery
		// Positions must go: pacticipant=A < version=v1 < pacticipant=B < version=v2.
		iAPact := strings.Index(raw, "pacticipant%5D=api")
		iAVer := strings.Index(raw, "version%5D=v1")
		iBPact := strings.Index(raw, "pacticipant%5D=web")
		iBVer := strings.Index(raw, "version%5D=v2")
		require.True(t, iAPact >= 0 && iAVer > iAPact && iBPact > iAVer && iBVer > iBPact,
			"raw query must interleave pairs; got: %s", raw)

		tr := true
		_ = json.NewEncoder(w).Encode(matrixResponse(&tr, "ok"))
	})

	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeployMany(context.Background(), "production", []broker.CanIDeploySelector{
		{Pacticipant: "api", Version: "v1"},
		{Pacticipant: "web", Version: "v2"},
	})
	require.NoError(t, err)
	assert.True(t, res.Deployable)
}

func TestCanIDeployMany_Deployable(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, _ *http.Request) {
		tr := true
		_ = json.NewEncoder(w).Encode(matrixResponse(&tr, "ok",
			pactRow("acolyte", "cd2c3499f", "news-creator", "cd2c3499f", &tr, "http://v/1"),
			pactRow("web", "cd2c3499f", "api", "cd2c3499f", &tr, "http://v/2"),
		))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeployMany(context.Background(), "production", []broker.CanIDeploySelector{
		{Pacticipant: "acolyte", Version: "cd2c3499f"},
		{Pacticipant: "news-creator", Version: "cd2c3499f"},
	})
	require.NoError(t, err)
	assert.True(t, res.Deployable)
	require.Len(t, res.Rows, 2)
	assert.True(t, res.Rows[0].Verified)
	assert.True(t, res.Rows[0].Success)

	verdicts := broker.PerPacticipantVerdicts(res, []broker.CanIDeploySelector{
		{Pacticipant: "acolyte", Version: "cd2c3499f"},
		{Pacticipant: "news-creator", Version: "cd2c3499f"},
	})
	assert.True(t, verdicts["acolyte"].Deployable)
	assert.True(t, verdicts["news-creator"].Deployable)
}

func TestCanIDeployMany_RowLevelFailure(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, _ *http.Request) {
		tr, fl := true, false
		_ = json.NewEncoder(w).Encode(matrixResponse(&fl, "verification failed",
			pactRow("acolyte", "cd2c3499f", "news-creator", "cd2c3499f", &fl, "http://verify/bad"),
			pactRow("web", "cd2c3499f", "api", "cd2c3499f", &tr, "http://verify/good"),
		))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	selectors := []broker.CanIDeploySelector{
		{Pacticipant: "acolyte", Version: "cd2c3499f"},
		{Pacticipant: "news-creator", Version: "cd2c3499f"},
		{Pacticipant: "web", Version: "cd2c3499f"},
		{Pacticipant: "api", Version: "cd2c3499f"},
	}
	res, err := c.CanIDeployMany(context.Background(), "production", selectors)
	require.NoError(t, err)
	assert.False(t, res.Deployable)

	verdicts := broker.PerPacticipantVerdicts(res, selectors)
	// The failing row points at both acolyte (consumer) and news-creator
	// (provider); both should be flagged because the broker said their
	// pair is not verified in the candidate set.
	assert.False(t, verdicts["acolyte"].Deployable)
	assert.False(t, verdicts["news-creator"].Deployable)
	assert.Equal(t, "http://verify/bad", verdicts["acolyte"].VerifyURL)
	assert.Contains(t, verdicts["acolyte"].Reason, "news-creator")

	// The unrelated pair stayed green.
	assert.True(t, verdicts["web"].Deployable)
	assert.True(t, verdicts["api"].Deployable)
}

func TestCanIDeployMany_UnknownPacticipant(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, _ *http.Request) {
		tr := true
		_ = json.NewEncoder(w).Encode(matrixResponse(&tr, "ok",
			pactRow("api", "v1", "web", "v2", &tr, "http://v/1"),
		))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	selectors := []broker.CanIDeploySelector{
		{Pacticipant: "api", Version: "v1"},
		{Pacticipant: "web", Version: "v2"},
		{Pacticipant: "orphan", Version: "v3"}, // no row mentions this one
	}
	res, err := c.CanIDeployMany(context.Background(), "production", selectors)
	require.NoError(t, err)

	verdicts := broker.PerPacticipantVerdicts(res, selectors)
	assert.True(t, verdicts["api"].Deployable)
	assert.True(t, verdicts["web"].Deployable)
	assert.False(t, verdicts["orphan"].Deployable)
	assert.Contains(t, verdicts["orphan"].Reason, "no verification record")
	assert.Contains(t, verdicts["orphan"].Reason, "orphan@v3")
}

// Current Pact Broker releases advertise the matrix endpoint as
// pb:matrix. The aggregate path must prefer this over the legacy
// pb:can-i-deploy.
func TestCanIDeployMany_PrefersPbMatrix(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:matrix":       {Href: tb.URL() + "/matrix-v2", Templated: false},
		"pb:can-i-deploy": {Href: tb.URL() + "/should-not-be-used", Templated: false},
	})
	tb.on("GET /matrix-v2", func(w http.ResponseWriter, _ *http.Request) {
		tr := true
		_ = json.NewEncoder(w).Encode(matrixResponse(&tr, "ok"))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeployMany(context.Background(), "production", []broker.CanIDeploySelector{
		{Pacticipant: "api", Version: "v1"},
	})
	require.NoError(t, err)
	assert.True(t, res.Deployable)
}

// Modern brokers expose only the scoped per-pacticipant relation in
// their index, yet the /matrix endpoint is a long-standing Pact Broker
// URL. Aggregate mode therefore falls back to constructing the URL from
// the broker base when no relation is advertised.
func TestCanIDeployMany_FallsBackToDirectMatrix(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy-pacticipant-version-to-environment": {
			Href:      tb.URL() + "/can-i-deploy/provider/{pacticipant}/version/{version}/to-environment/{environment}",
			Templated: true,
		},
	})
	var called bool
	tb.on("GET /matrix", func(w http.ResponseWriter, r *http.Request) {
		called = true
		// Query is still bracket-array form even without a HAL template
		// to guide us.
		assert.Equal(t, []string{"api"}, r.URL.Query()["q[][pacticipant]"])
		assert.Equal(t, "cvp", r.URL.Query().Get("latestby"))
		tr := true
		_ = json.NewEncoder(w).Encode(matrixResponse(&tr, "ok"))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeployMany(context.Background(), "production", []broker.CanIDeploySelector{
		{Pacticipant: "api", Version: "v1"},
	})
	require.NoError(t, err)
	assert.True(t, res.Deployable)
	assert.True(t, called, "direct /matrix URL must be hit when no HAL relation points to it")
}

func TestCanIDeployMany_TemplatedQueryHref(t *testing.T) {
	tb := newTestBroker(t)
	// Modern brokers advertise the matrix endpoint with an RFC 6570
	// level-3 query template. Our client should strip the `{?...}` tail
	// and build the query itself.
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {
			Href:      tb.URL() + "/matrix{?pacticipant,version,environment,latestby}",
			Templated: true,
		},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, r *http.Request) {
		// The path must be /matrix with no template leftover.
		assert.Equal(t, "/matrix", r.URL.Path)
		assert.NotContains(t, r.URL.RawQuery, "{")
		tr := true
		_ = json.NewEncoder(w).Encode(matrixResponse(&tr, "ok"))
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	res, err := c.CanIDeployMany(context.Background(), "production", []broker.CanIDeploySelector{
		{Pacticipant: "api", Version: "v1"},
	})
	require.NoError(t, err)
	assert.True(t, res.Deployable)
}
