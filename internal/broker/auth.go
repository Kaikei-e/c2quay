package broker

import "net/http"

// AuthMethod attaches credentials to an outbound request.
type AuthMethod interface {
	Apply(r *http.Request)
}

type NoAuth struct{}

func (NoAuth) Apply(*http.Request) {}

type BasicAuth struct {
	Username, Password string
}

func (b BasicAuth) Apply(r *http.Request) { r.SetBasicAuth(b.Username, b.Password) }

type BearerAuth struct {
	Token string
}

func (b BearerAuth) Apply(r *http.Request) {
	r.Header.Set("Authorization", "Bearer "+b.Token)
}

// ResolveAuth picks an AuthMethod from environment-sourced credentials.
// Bearer wins over Basic if both are present.
func ResolveAuth(username, password, token string) AuthMethod {
	if token != "" {
		return BearerAuth{Token: token}
	}
	if username != "" || password != "" {
		return BasicAuth{Username: username, Password: password}
	}
	return NoAuth{}
}
