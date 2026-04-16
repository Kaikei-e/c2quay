package broker_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
)

func TestLink_ExpandTemplate(t *testing.T) {
	l := broker.Link{Href: "https://b/x/{pacticipant}/versions/{version}/env/{environment}", Templated: true}
	out, err := l.ExpandTemplate(map[string]string{
		"pacticipant": "api/v2",
		"version":     "sha256:abc",
		"environment": "production",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://b/x/api%2Fv2/versions/sha256:abc/env/production", out)
}

func TestLink_ExpandTemplate_Unresolved(t *testing.T) {
	l := broker.Link{Href: "https://b/{missing}", Templated: true}
	_, err := l.ExpandTemplate(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved template")
}

func TestLink_NotTemplated(t *testing.T) {
	l := broker.Link{Href: "https://b/plain", Templated: false}
	out, err := l.ExpandTemplate(nil)
	require.NoError(t, err)
	assert.Equal(t, "https://b/plain", out)
}

// A real Pact Broker index response always includes curies as an array. Prior
// to the custom UnmarshalJSON, this payload caused the JSON decoder to fail
// at the map decode level because curies cannot fit into map[string]Link.
func TestIndex_UnmarshalJSON_CuriesArrayAndSingletonLinks(t *testing.T) {
	raw := []byte(`{
		"_links": {
			"self": {"href": "https://b/", "templated": false},
			"pb:can-i-deploy": {"href": "https://b/can-i-deploy{?pacticipant,version,environment}", "templated": true},
			"pb:record-deployment": {"href": "https://b/record-deployment/{pacticipant}/{version}/{environment}", "templated": true},
			"curies": [
				{"name": "pb", "href": "https://b/rels/{rel}", "templated": true},
				{"name": "beta", "href": "https://b/beta-rels/{rel}", "templated": true}
			]
		}
	}`)
	var idx broker.Index
	require.NoError(t, json.Unmarshal(raw, &idx))

	// Single-link relations are still map-accessible, unchanged behaviour.
	self, ok := idx.Links["self"]
	require.True(t, ok)
	assert.Equal(t, "https://b/", self.Href)

	cid, ok := idx.Links["pb:can-i-deploy"]
	require.True(t, ok)
	assert.True(t, cid.Templated)

	// Curies are kept off the Links map (HAL never looks them up by rel name).
	_, curiesInLinks := idx.Links["curies"]
	assert.False(t, curiesInLinks, "curies must not leak into the single-link map")

	require.Len(t, idx.Curies, 2)
	assert.Equal(t, "pb", idx.Curies[0].Name)
	assert.Equal(t, "beta", idx.Curies[1].Name)
}

// Defensive: a non-curie array value (some broker builds emit one-element
// arrays for routed rels) is flattened into Links[rel][0] for compatibility
// and also preserved in full via MultiLinks.
func TestIndex_UnmarshalJSON_ArrayValuedRelation(t *testing.T) {
	raw := []byte(`{
		"_links": {
			"pb:environments": [
				{"href": "https://b/environments/1", "name": "production"},
				{"href": "https://b/environments/2", "name": "staging"}
			]
		}
	}`)
	var idx broker.Index
	require.NoError(t, json.Unmarshal(raw, &idx))

	l, ok := idx.Links["pb:environments"]
	require.True(t, ok, "array-valued rel should still be accessible via Links[rel]")
	assert.Equal(t, "production", l.Name)

	all, ok := idx.MultiLinks["pb:environments"]
	require.True(t, ok)
	assert.Len(t, all, 2)
}

// An unknown scalar or bad shape should surface as an error, not silently drop.
func TestIndex_UnmarshalJSON_RejectsMalformed(t *testing.T) {
	raw := []byte(`{"_links": {"pb:broken": 42}}`)
	var idx broker.Index
	require.Error(t, json.Unmarshal(raw, &idx))
}
