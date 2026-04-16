package broker_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Kaikei-e/c2quay/internal/broker"
)

func TestResolveAuth_TokenWinsOverBasic(t *testing.T) {
	a := broker.ResolveAuth("u", "p", "tok")
	r := httptest.NewRequest("GET", "http://x/", nil)
	a.Apply(r)
	assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
}

func TestResolveAuth_Basic(t *testing.T) {
	a := broker.ResolveAuth("u", "p", "")
	r := httptest.NewRequest("GET", "http://x/", nil)
	a.Apply(r)
	u, p, ok := r.BasicAuth()
	assert.True(t, ok)
	assert.Equal(t, "u", u)
	assert.Equal(t, "p", p)
}

func TestResolveAuth_None(t *testing.T) {
	a := broker.ResolveAuth("", "", "")
	r := httptest.NewRequest("GET", "http://x/", nil)
	a.Apply(r)
	assert.Empty(t, r.Header.Get("Authorization"))
}
