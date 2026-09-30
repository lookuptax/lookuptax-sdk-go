package lookuptax

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validBody = `{"referenceId":"r1","countryCode":"IE","tin":"53057102A",
"result":{"status":"VALID","reason":null,"message":"ok","retryable":false},
"tinInfo":{"label":"VAT","name":"VAT number"},"entity":{"name":"GRAFTON FOODS LIMITED"}}`

func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	opts = append([]Option{WithBaseURL(srv.URL), WithMaxRetries(0)}, opts...)
	c, err := New("k", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestSendsAPIKey(t *testing.T) {
	var gotKey, gotPath, gotQuery string
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath, gotQuery = r.Header.Get("X-API-Key"), r.URL.Path, r.URL.RawQuery
		w.Write([]byte(validBody))
	})
	defer srv.Close()

	if _, err := c.Validate(context.Background(), "IE", "53057102A", nil); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if gotKey != "k" {
		t.Errorf("X-API-Key = %q", gotKey)
	}
	if gotPath != "/validate" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "country_iso=IE") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestNestsAdditionalParams(t *testing.T) {
	var q string
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("additional_params[name]")
		w.Write([]byte(validBody))
	})
	defer srv.Close()

	_, err := c.Validate(context.Background(), "MX", "X", &ValidateOptions{
		AdditionalParams: map[string]string{"name": "ACME"},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if q != "ACME" {
		t.Errorf("additional_params[name] = %q", q)
	}
}

// An INVALID tax ID is a successful call, not an error.
func TestInvalidIsNotAnError(t *testing.T) {
	body := `{"result":{"status":"INVALID","reason":"NOT_REGISTERED","message":"","retryable":false}}`
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
	defer srv.Close()

	res, err := c.Validate(context.Background(), "IE", "x", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res.Result.Status != StatusInvalid || res.Result.IsValid() {
		t.Errorf("got %+v", res.Result)
	}
}

// 422 carries a result and must be returned, not raised.
func TestUnsupported422ReturnsOutcome(t *testing.T) {
	body := `{"result":{"status":"UNSUPPORTED","reason":"COUNTRY_NOT_SUPPORTED","message":"","retryable":false}}`
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(body))
	})
	defer srv.Close()

	res, err := c.Validate(context.Background(), "ZZ", "x", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res.Result.Status != StatusUnsupported {
		t.Errorf("status = %q", res.Result.Status)
	}
}

// UNVERIFIED is format-only — must not read as valid.
func TestUnverifiedIsNotValid(t *testing.T) {
	r := Result{Status: StatusUnverified}
	if r.IsValid() || r.ShouldRetry() {
		t.Errorf("UNVERIFIED must be neither valid nor retryable")
	}
	if !(Result{Status: StatusIndeterminate}).ShouldRetry() {
		t.Errorf("INDETERMINATE must be retryable")
	}
}

func TestTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		check  func(*Error) bool
	}{
		{401, IsAuth}, {402, IsQuotaExceeded}, {429, IsRateLimited}, {404, IsNotFound},
	} {
		c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"error":"e","message":"m"}`))
		})
		_, err := c.Validate(context.Background(), "IE", "1", nil)
		apiErr, ok := err.(*Error)
		if !ok {
			t.Fatalf("status %d: expected *Error, got %T", tc.status, err)
		}
		if !tc.check(apiErr) {
			t.Errorf("status %d: classifier returned false", tc.status)
		}
		srv.Close()
	}
}

func TestRetryAfterAndRetryability(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		w.Write([]byte(`{"error":"API_RATE_LIMIT"}`))
	})
	defer srv.Close()

	_, err := c.Validate(context.Background(), "IE", "1", nil)
	apiErr := err.(*Error)
	if apiErr.RetryAfter != 7 {
		t.Errorf("RetryAfter = %v", apiErr.RetryAfter)
	}
	if !apiErr.IsRetryable() {
		t.Error("429 should be retryable")
	}
	if (&Error{Status: 400}).IsRetryable() {
		t.Error("400 should not be retryable")
	}
}

func TestRetriesTransientThenSucceeds(t *testing.T) {
	n := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"service_unavailable"}`))
			return
		}
		w.Write([]byte(validBody))
	}, WithMaxRetries(1))
	defer srv.Close()

	if _, err := c.Validate(context.Background(), "IE", "1", nil); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}
}

// A retried submission that actually succeeded would double-reserve quota.
func TestBatchSubmissionNeverRetried(t *testing.T) {
	n := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"service_unavailable"}`))
	}, WithMaxRetries(3))
	defer srv.Close()

	if _, err := c.CreateBatch(context.Background(), []TaxIDInput{{CountryISO: "IE", Tin: "1"}}, nil); err == nil {
		t.Fatal("expected error")
	}
	if n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestCreateBatchWireShape(t *testing.T) {
	var body map[string]any
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"batch_id":"b","status":"pending","total_count":1}`))
	})
	defer srv.Close()

	_, err := c.CreateBatch(context.Background(),
		[]TaxIDInput{{CountryISO: "MX", Tin: "X", Name: "ACME"}},
		&CreateBatchOptions{ValidationSource: SourceLocal})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if body["validation_source"] != "local" {
		t.Errorf("validation_source = %v", body["validation_source"])
	}
	row := body["tax_ids"].([]any)[0].(map[string]any)
	if row["country_iso"] != "MX" || row["name"] != "ACME" {
		t.Errorf("row = %v", row)
	}
}

func TestIterateFollowsCursor(t *testing.T) {
	pages := []string{
		`{"status":"completed","results":[{"request_id":"1"}],"next_cursor":1}`,
		`{"status":"completed","results":[{"request_id":"2"}],"next_cursor":null}`,
	}
	i := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pages[i]))
		i++
	})
	defer srv.Close()

	var seen []string
	err := c.IterateBatchResults(context.Background(), "b", 0, func(it BatchItem) error {
		seen = append(seen, it.RequestID)
		return nil
	})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(seen) != 2 || seen[0] != "1" || seen[1] != "2" {
		t.Errorf("seen = %v", seen)
	}
}

func TestDeleteHandles204(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	defer srv.Close()
	if err := c.DeleteBatch(context.Background(), "b"); err != nil {
		t.Errorf("DeleteBatch: %v", err)
	}
}

func TestRequiresAPIKey(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("expected error for empty apiKey")
	}
}
