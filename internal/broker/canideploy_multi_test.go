package broker_test

import (
	"context"
	"encoding/json"
	"net/http"
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

func TestCanIDeployMany_BuildsBracketArrayQuery(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		"pb:can-i-deploy": {Href: tb.URL() + "/matrix", Templated: false},
	})
	tb.on("GET /matrix", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Order must match the selector order so consumer/version pairing
		// by index is preserved through Rack-style bracket arrays.
		assert.Equal(t, []string{"api", "web"}, q["q[][pacticipant]"])
		assert.Equal(t, []string{"v1", "v2"}, q["q[][version]"])
		assert.Equal(t, "cvp", q.Get("latestby"))
		assert.Equal(t, "production", q.Get("environment"))
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

func TestCanIDeployMany_MissingRelation(t *testing.T) {
	tb := newTestBroker(t)
	tb.index(map[string]broker.Link{
		// Only the scoped relation exists. Aggregate requires the generic
		// matrix endpoint.
		"pb:can-i-deploy-pacticipant-version-to-environment": {
			Href: tb.URL() + "/can-i-deploy/provider/{pacticipant}/version/{version}/to-environment/{environment}", Templated: true,
		},
	})
	c, err := broker.New(broker.Options{BaseURL: tb.URL()})
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))

	_, err = c.CanIDeployMany(context.Background(), "production", []broker.CanIDeploySelector{
		{Pacticipant: "api", Version: "v1"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, broker.ErrRelationMissing)
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
