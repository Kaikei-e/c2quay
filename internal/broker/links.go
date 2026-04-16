package broker

import (
	"bytes"
	"encoding/json"
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
//
// The HAL spec (draft-kelly-json-hal §4.1.1) allows any relation value to be
// either a single Link object or an array of Link objects, and REQUIRES
// `curies` to be an array. A naive `map[string]Link` blows up on real Pact
// Broker indices because of that single field. We accept both shapes via a
// custom unmarshaler:
//
//   - Single-link relations (the broker's routed endpoints like
//     pb:can-i-deploy, pb:record-deployment) land in Links as before.
//   - Array-valued relations land in Links[rel] as the first element (so
//     existing callers using Links[rel] keep working) AND in MultiLinks[rel]
//     for callers that need the full list.
//   - `curies` is stored separately in Curies; it is never looked up by
//     relation name.
type Index struct {
	Links      map[string]Link   `json:"-"`
	MultiLinks map[string][]Link `json:"-"`
	Curies     []Link            `json:"-"`
}

// UnmarshalJSON implements HAL-tolerant decoding for the `_links` object.
func (i *Index) UnmarshalJSON(data []byte) error {
	var aux struct {
		Links map[string]json.RawMessage `json:"_links"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&aux); err != nil {
		return fmt.Errorf("decode broker index: %w", err)
	}
	i.Links = make(map[string]Link, len(aux.Links))
	i.MultiLinks = make(map[string][]Link)
	for rel, raw := range aux.Links {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		if trimmed[0] == '[' {
			var arr []Link
			if err := json.Unmarshal(raw, &arr); err != nil {
				return fmt.Errorf("decode HAL link array %q: %w", rel, err)
			}
			if rel == "curies" {
				i.Curies = arr
				continue
			}
			i.MultiLinks[rel] = arr
			if len(arr) > 0 {
				i.Links[rel] = arr[0]
			}
			continue
		}
		var single Link
		if err := json.Unmarshal(raw, &single); err != nil {
			return fmt.Errorf("decode HAL link %q: %w", rel, err)
		}
		i.Links[rel] = single
	}
	return nil
}
