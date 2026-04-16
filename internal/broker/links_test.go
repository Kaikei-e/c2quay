package broker_test

import (
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
