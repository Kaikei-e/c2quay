package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options configures a Client.
type Options struct {
	BaseURL string
	Auth    AuthMethod
	HTTP    *http.Client
	Logger  *slog.Logger
	Timeout time.Duration
}

// Client is a HAL-driven Pact Broker HTTP client. It follows _links from the
// broker's index resource rather than hard-coding URLs, so it continues to
// work as the broker evolves its routes.
type Client struct {
	base  *url.URL
	http  *http.Client
	auth  AuthMethod
	log   *slog.Logger
	index *Index
	calls int
}

// New constructs a Client. Start must be called before any relation-based operation.
func New(opts Options) (*Client, error) {
	base, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse broker base url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("broker base url %q is missing scheme or host", opts.BaseURL)
	}
	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if opts.Timeout > 0 {
		httpClient.Timeout = opts.Timeout
	} else if httpClient.Timeout == 0 {
		httpClient.Timeout = 30 * time.Second
	}
	if opts.Auth == nil {
		opts.Auth = NoAuth{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		base: base,
		http: httpClient,
		auth: opts.Auth,
		log:  logger,
	}, nil
}

// Start fetches the index resource and caches its _links.
func (c *Client) Start(ctx context.Context) error {
	body, _, err := c.do(ctx, http.MethodGet, c.base.String(), nil, nil)
	if err != nil {
		return fmt.Errorf("fetch broker index: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return fmt.Errorf("decode broker index: %w", err)
	}
	c.index = &idx
	return nil
}

// APICallCount returns the number of HTTP requests this client has made to the
// broker since construction. Useful for audit logs.
func (c *Client) APICallCount() int { return c.calls }

// Link returns the HAL link for a relation, or ErrRelationMissing.
func (c *Client) Link(rel string) (Link, error) {
	if c.index == nil {
		return Link{}, errors.New("broker client not started; call Start() first")
	}
	l, ok := c.index.Links[rel]
	if !ok {
		return Link{}, fmt.Errorf("%w: %q", ErrRelationMissing, rel)
	}
	return l, nil
}

// HasRelation reports whether a given relation exists in the index.
func (c *Client) HasRelation(rel string) bool {
	if c.index == nil {
		return false
	}
	_, ok := c.index.Links[rel]
	return ok
}

// getJSON issues a GET and decodes into v.
func (c *Client) getJSON(ctx context.Context, urlStr string, query url.Values, v any) error {
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(urlStr, "?") {
			sep = "&"
		}
		urlStr = urlStr + sep + query.Encode()
	}
	body, _, err := c.do(ctx, http.MethodGet, urlStr, nil, map[string]string{"Accept": "application/hal+json"})
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(body, v)
}

// postJSON issues a POST with a JSON body and decodes into v if non-nil.
func (c *Client) postJSON(ctx context.Context, urlStr string, body any, v any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	respBody, _, err := c.do(ctx, http.MethodPost, urlStr, reader, map[string]string{
		"Accept":       "application/hal+json",
		"Content-Type": "application/json",
	})
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(respBody, v)
}

func (c *Client) do(ctx context.Context, method, urlStr string, body io.Reader, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "c2quay")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.auth.Apply(req)

	c.calls++
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}
	if resp.StatusCode >= 400 {
		return raw, resp.StatusCode, fmt.Errorf("%w: %s %s -> %d: %s", ErrUnexpectedStatus, method, urlStr, resp.StatusCode, snippet(raw))
	}
	return raw, resp.StatusCode, nil
}

func snippet(raw []byte) string {
	const maxLen = 512
	if len(raw) <= maxLen {
		return string(raw)
	}
	return string(raw[:maxLen]) + "...(truncated)"
}
