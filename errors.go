package lookuptax

import (
	"encoding/json"
	"fmt"
)

// Error is a request error: the call could not be processed at all.
//
// A validation outcome (an INVALID tax ID, say) is NOT an Error — it arrives in
// the response with Result.Status set.
type Error struct {
	// Code is stable and machine-readable, e.g. "quota_exceeded".
	// Branch on this, never on Message.
	Code    string
	Status  int
	Message string
	// RetryAfter is seconds from the Retry-After header. Only set for 429.
	RetryAfter float64
	Body       []byte
}

func (e *Error) Error() string {
	return fmt.Sprintf("lookuptax: %s (code=%s, status=%d)", e.Message, e.Code, e.Status)
}

// IsRetryable reports whether retrying the identical request could plausibly
// succeed: 429 (retries do not consume quota), 500 and 503.
func (e *Error) IsRetryable() bool {
	return e.Status == 429 || e.Status == 500 || e.Status == 503
}

// Sentinel checks, usable with errors.As.
func IsAuth(e *Error) bool        { return e.Status == 401 || e.Status == 403 }
func IsQuotaExceeded(e *Error) bool { return e.Status == 402 }
func IsRateLimited(e *Error) bool   { return e.Status == 429 }
func IsNotFound(e *Error) bool      { return e.Status == 404 }

func errorFromResponse(status int, body []byte, retryAfter float64) *Error {
	var parsed struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &parsed)

	code := parsed.Error
	if code == "" {
		code = parsed.Code
	}
	if code == "" {
		code = fmt.Sprintf("http_%d", status)
	}
	msg := parsed.Message
	if msg == "" {
		msg = fmt.Sprintf("request failed with HTTP %d", status)
	}
	return &Error{Code: code, Status: status, Message: msg, RetryAfter: retryAfter, Body: body}
}
