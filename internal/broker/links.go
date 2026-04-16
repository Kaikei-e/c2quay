package broker

import (
	"fmt"
	"net/url"
	"strings"
)

// Link is a single HAL link.
type Link struct {
	Href      string `json:"href"`
	Templated bool   `json:"templated"`
	Name      string `json:"name,omitempty"`
	Title     string `json:"title,omitempty"`
}

// ExpandTemplate replaces RFC 6570 level-1 variables (`{var}`) with URL-escaped
// values. If the link is not templated we still pass it through unchanged.
func (l Link) ExpandTemplate(vars map[string]string) (string, error) {
	if !l.Templated {
		return l.Href, nil
	}
	out := l.Href
	for k, v := range vars {
		token := "{" + k + "}"
		if !strings.Contains(out, token) {
			continue
		}
		out = strings.ReplaceAll(out, token, url.PathEscape(v))
	}
	if strings.Contains(out, "{") {
		return "", fmt.Errorf("unresolved template variable in %q", l.Href)
	}
	return out, nil
}

// Index represents the root HAL document returned by the broker.
type Index struct {
	Links map[string]Link `json:"_links"`
}
