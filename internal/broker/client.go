package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
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
	// RetryBaseDelay is the base delay used for exponential backoff between
	// retry attempts in c.do. Defaults to 500ms. Exposed mainly so tests can
	// shrink it; production callers should normally leave this unset.
	RetryBaseDelay time.Duration
}

// maxAttempts bounds the total number of HTTP attempts c.do makes for a
// single logical call (1 initial + up to maxAttempts-1 retries).
const maxAttempts = 3

// defaultRetryBaseDelay is the base for exponential backoff when
// Options.RetryBaseDelay is unset.
const defaultRetryBaseDelay = 500 * time.Millisecond

// Client is a HAL-driven Pact Broker HTTP client. It follows _links from the
// broker's index resource rather than hard-coding URLs, so it continues to
// work as the broker evolves its routes.
type Client struct {
	base           *url.URL
	http           *http.Client
	auth           AuthMethod
	log            *slog.Logger
	index          *Index
	calls          int
	retryBaseDelay time.Duration
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
	retryBaseDelay := opts.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = defaultRetryBaseDelay
	}
	return &Client{
		base:           base,
		http:           httpClient,
		auth:           opts.Auth,
		log:            logger,
		retryBaseDelay: retryBaseDelay,
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
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
	}
	respBody, _, err := c.do(ctx, http.MethodPost, urlStr, raw, map[string]string{
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

// do executes an HTTP request against the broker with bounded retry for
// transient failures. See ADR 0014 for the full rationale; summary:
//
//   - Network-level errors (DNS, TCP, TLS, connection reset, timeouts inside
//     a single attempt) are retried — the request may never have reached the
//     broker at all.
//   - HTTP 429 (rate limited) and any 5xx are retried — server-side or
//     throttling conditions that are expected to clear.
//   - Any other 4xx (400, 401, 403, 404, 409, ...) is NOT retried: it
//     reflects a client-side problem that will not be fixed by trying again.
//   - GET requests (index, links, matrix, can-i-deploy) are naturally safe
//     to retry — they have no side effects.
//   - The only POST this client ever issues is pb:record-deployment (see
//     deployment.go — postJSON has exactly one caller). Per the Pact Broker
//     API, recording the same (pacticipant, version, environment) deployment
//     twice is a no-op on the broker side: it just (re)marks that version as
//     currently deployed to that environment, it does not create duplicate
//     records or duplicate side effects. That makes it safe to fold into the
//     same retry policy as the GETs above. If a second, non-idempotent POST
//     is ever added to this client, this blanket policy must be revisited
//     (e.g. by threading an explicit "retryable" flag through do's callers)
//     before it inherits automatic retries.
//   - Each attempt gets a fresh call to c.http.Do, so c.http.Timeout (30s by
//     default, see New) applies per attempt, not to the retry loop as a
//     whole — a single slow attempt cannot silently eat the whole retry
//     budget.
//   - Context cancellation/deadline is honored between attempts: the loop
//     checks ctx before sleeping and before starting the next attempt, and
//     it never converts a context error into a retryable classification.
func (c *Client) do(ctx context.Context, method, urlStr string, bodyBytes []byte, headers map[string]string) ([]byte, int, error) {
	var (
		raw    []byte
		status int
		err    error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := backoffDelay(c.retryBaseDelay, attempt-1)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(delay):
			}
			c.log.Warn("retrying broker request",
				slog.String("method", method),
				slog.String("url", urlStr),
				slog.Int("attempt", attempt),
				slog.Int("max_attempts", maxAttempts),
				slog.String("previous_error", err.Error()),
			)
		}

		raw, status, err = c.doOnce(ctx, method, urlStr, bodyBytes, headers)
		if err == nil {
			return raw, status, nil
		}
		if !isRetryable(err, status) {
			return raw, status, err
		}
	}
	return raw, status, err
}

func (c *Client) doOnce(ctx context.Context, method, urlStr string, bodyBytes []byte, headers map[string]string) ([]byte, int, error) {
	var body io.Reader
	if bodyBytes != nil {
		body = bytes.NewReader(bodyBytes)
	}
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}
	if resp.StatusCode >= 400 {
		return raw, resp.StatusCode, fmt.Errorf("%w: %s %s -> %d: %s", ErrUnexpectedStatus, method, urlStr, resp.StatusCode, snippet(raw))
	}
	return raw, resp.StatusCode, nil
}

// isRetryable classifies a do() failure as transient (worth retrying) or
// permanent. See the do() doc comment for the policy this implements.
func isRetryable(err error, status int) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if status == http.StatusTooManyRequests {
		return true
	}
	if status >= 500 && status <= 599 {
		return true
	}
	if status == 0 && errors.Is(err, ErrBrokerUnreachable) {
		return true
	}
	return false
}

// backoffDelay returns the exponential-backoff-with-jitter delay before
// retry number n (n=1 for the first retry, n=2 for the second, ...), based
// on base. Full jitter in [0, backoff/2) is added on top of the doubling
// backoff so that concurrent callers hitting a struggling broker don't all
// retry in lockstep.
func backoffDelay(base time.Duration, n int) time.Duration {
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	backoff := base * time.Duration(1<<uint(n-1))
	jitter := time.Duration(0)
	if half := backoff / 2; half > 0 {
		jitter = rand.N(half + 1) //nolint:gosec // jitter timing, not a security-sensitive value; math/rand/v2 is fine.
	}
	return backoff + jitter
}

func snippet(raw []byte) string {
	const maxLen = 512
	if len(raw) <= maxLen {
		return string(raw)
	}
	return string(raw[:maxLen]) + "...(truncated)"
}
