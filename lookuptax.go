// Package lookuptax is the official Go SDK for the LookupTax API.
//
// It validates tax IDs — VAT, EIN, GSTIN, ABN and more — against official
// government registries.
//
// The API separates two kinds of failure, and so does this package:
//
//   - A request error (HTTP 4xx/5xx) means the call could not be processed:
//     authentication, quota, rate limit, a malformed request. These are
//     returned as a *Error.
//   - A validation outcome (HTTP 200) means the call succeeded but the tax ID
//     itself is invalid or could not be confirmed. These come back in the
//     response, and you read Result.Status.
//
// An INVALID tax ID is a successful call, not an error.
package lookuptax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the API root. Override via WithBaseURL: the published path
// prefix is deployment configuration, not a property of the SDK.
const DefaultBaseURL = "https://api.lookuptax.com/v1"

// Client talks to the LookupTax API. Safe for concurrent use.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	maxRetries int
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at a different API root.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient injects your own *http.Client — for proxies, custom
// transports, or tests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// WithMaxRetries sets retries for transient failures (429/500/503), honouring
// Retry-After when present. Zero disables retrying. Batch submission is never
// retried regardless — see CreateBatch.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// New returns a Client. apiKey is required.
func New(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("lookuptax: apiKey is required")
	}
	c := &Client{
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		maxRetries: 2,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// ─── requests ────────────────────────────────────────────────────────

type requestOpts struct {
	query  url.Values
	body   any
	noRetry bool
}

func (c *Client) do(ctx context.Context, method, path string, o requestOpts, out any) error {
	endpoint := c.baseURL + path
	if len(o.query) > 0 {
		endpoint += "?" + o.query.Encode()
	}

	attempts := c.maxRetries + 1
	if o.noRetry {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		var reader io.Reader
		if o.body != nil {
			raw, err := json.Marshal(o.body)
			if err != nil {
				return fmt.Errorf("lookuptax: encoding request: %w", err)
			}
			reader = bytes.NewReader(raw)
		}

		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return fmt.Errorf("lookuptax: building request: %w", err)
		}
		req.Header.Set("X-API-Key", c.apiKey)
		req.Header.Set("Accept", "application/json")
		if o.body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < attempts-1 && ctx.Err() == nil {
				sleep(ctx, backoff(attempt))
				continue
			}
			return fmt.Errorf("lookuptax: %w", err)
		}

		payload, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < attempts-1 {
				sleep(ctx, backoff(attempt))
				continue
			}
			return fmt.Errorf("lookuptax: reading response: %w", readErr)
		}

		if resp.StatusCode == http.StatusNoContent {
			return nil
		}

		// 422 is an OUTCOME, not a failure: the body still carries `result`
		// with status UNSUPPORTED. Decode it like any success.
		if resp.StatusCode < 300 || (resp.StatusCode == 422 && hasResult(payload)) {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(payload, out); err != nil {
				return fmt.Errorf("lookuptax: decoding response: %w", err)
			}
			return nil
		}

		apiErr := errorFromResponse(resp.StatusCode, payload, retryAfter(resp))
		if apiErr.IsRetryable() && attempt < attempts-1 {
			lastErr = apiErr
			d := backoff(attempt)
			if apiErr.RetryAfter > 0 {
				d = time.Duration(apiErr.RetryAfter * float64(time.Second))
			}
			sleep(ctx, d)
			continue
		}
		return apiErr
	}
	return lastErr
}

func backoff(attempt int) time.Duration {
	return time.Duration(math.Pow(2, float64(attempt))*500) * time.Millisecond
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func retryAfter(resp *http.Response) float64 {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

func hasResult(payload []byte) bool {
	var probe struct {
		Result *json.RawMessage `json:"result"`
	}
	return json.Unmarshal(payload, &probe) == nil && probe.Result != nil
}

// ─── single validation ───────────────────────────────────────────────

// ValidateOptions are the optional parameters for Validate.
type ValidateOptions struct {
	// ReferenceID is an idempotency key, echoed back as ReferenceID.
	ReferenceID string
	// ValidationSource is EU-only: "auto" (default), "vies" or "local".
	// With "vies" or "local" there is no fallback to the other family.
	ValidationSource string
	// AdditionalParams carries extra registry params, e.g. {"name": "ACME"}
	// for Mexico.
	AdditionalParams map[string]string
}

// Validate checks one tax ID.
//
// It returns a response for every validation outcome, including INVALID — read
// res.Result.Status. It returns an error only when the request itself could not
// be processed.
func (c *Client) Validate(ctx context.Context, countryISO, tin string, opts *ValidateOptions) (*ValidationResponse, error) {
	q := url.Values{}
	q.Set("country_iso", countryISO)
	q.Set("tin", tin)
	if opts != nil {
		if opts.ReferenceID != "" {
			q.Set("reference_id", opts.ReferenceID)
		}
		if opts.ValidationSource != "" {
			q.Set("validation_source", opts.ValidationSource)
		}
		// Nested params render as additional_params[name]=…, the shape the API
		// expects; a flat ?name= is stripped before it reaches routing.
		for k, v := range opts.AdditionalParams {
			q.Set(fmt.Sprintf("additional_params[%s]", k), v)
		}
	}

	var out ValidationResponse
	if err := c.do(ctx, http.MethodGet, "/validate", requestOpts{query: q}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ─── batch ───────────────────────────────────────────────────────────

// CreateBatchOptions are the optional parameters for CreateBatch.
type CreateBatchOptions struct {
	UserID   string
	Metadata map[string]any
	// ValidationSource applies to every tax ID in the batch; non-EU rows are
	// unaffected.
	ValidationSource string
}

// CreateBatch submits up to 100 tax IDs. Enterprise plan only.
//
// Never retried automatically: a retried submission that actually succeeded
// would create a second batch and reserve quota twice.
func (c *Client) CreateBatch(ctx context.Context, taxIDs []TaxIDInput, opts *CreateBatchOptions) (*CreateBatchResponse, error) {
	body := map[string]any{"tax_ids": taxIDs}
	if opts != nil {
		if opts.UserID != "" {
			body["user_id"] = opts.UserID
		}
		if opts.Metadata != nil {
			body["metadata"] = opts.Metadata
		}
		if opts.ValidationSource != "" {
			body["validation_source"] = opts.ValidationSource
		}
	}
	var out CreateBatchResponse
	if err := c.do(ctx, http.MethodPost, "/batch", requestOpts{body: body, noRetry: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBatch fetches batch progress and one page of results.
func (c *Client) GetBatch(ctx context.Context, batchID string, limit, cursor int) (*BatchResults, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor > 0 {
		q.Set("cursor", strconv.Itoa(cursor))
	}
	var out BatchResults
	if err := c.do(ctx, http.MethodGet, "/batch/"+url.PathEscape(batchID), requestOpts{query: q}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListBatches returns your organization's batches, newest first.
func (c *Client) ListBatches(ctx context.Context, page, limit int) (*BatchList, error) {
	q := url.Values{}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out BatchList
	if err := c.do(ctx, http.MethodGet, "/batch", requestOpts{query: q}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelBatch stops a pending/processing batch. Reserved quota is released.
func (c *Client) CancelBatch(ctx context.Context, batchID string) (*CreateBatchResponse, error) {
	var out CreateBatchResponse
	path := "/batch/" + url.PathEscape(batchID) + "/cancel"
	if err := c.do(ctx, http.MethodPost, path, requestOpts{noRetry: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBatch permanently removes a terminal batch. Cannot be undone.
func (c *Client) DeleteBatch(ctx context.Context, batchID string) error {
	return c.do(ctx, http.MethodDelete, "/batch/"+url.PathEscape(batchID), requestOpts{noRetry: true}, nil)
}

// GetTaxID fetches one item from a batch by its request_id, without paging.
func (c *Client) GetTaxID(ctx context.Context, requestID string) (*BatchItem, error) {
	var out BatchItem
	if err := c.do(ctx, http.MethodGet, "/batch/tax-id/"+url.PathEscape(requestID), requestOpts{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WaitForBatch polls until the batch reaches a terminal state.
//
// Polls on a relaxed cadence by design: when a registry is unreachable an item
// keeps retrying for roughly three days, so a long-running item is not a stuck
// one. Cancel via ctx.
func (c *Client) WaitForBatch(ctx context.Context, batchID string, interval time.Duration) (*BatchResults, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		b, err := c.GetBatch(ctx, batchID, 0, 0)
		if err != nil {
			return nil, err
		}
		if b.IsTerminal() {
			return b, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// IterateBatchResults calls fn for every result across all pages, following
// next_cursor. Returning a non-nil error from fn stops iteration.
func (c *Client) IterateBatchResults(ctx context.Context, batchID string, limit int, fn func(BatchItem) error) error {
	cursor := 0
	for {
		page, err := c.GetBatch(ctx, batchID, limit, cursor)
		if err != nil {
			return err
		}
		for _, item := range page.Results {
			if err := fn(item); err != nil {
				return err
			}
		}
		if page.NextCursor == nil {
			return nil
		}
		cursor = *page.NextCursor
	}
}
