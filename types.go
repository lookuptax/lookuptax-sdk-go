package lookuptax

// Status values for Result.Status. Mutually exclusive.
const (
	// StatusValid — format passed and a government registry confirmed the entity.
	StatusValid = "VALID"
	// StatusInvalid — definitive negative: bad structure/check digit, or no record.
	StatusInvalid = "INVALID"
	// StatusUnverified — no queryable registry; only the format was checked.
	// A pass is NOT proof the business exists.
	StatusUnverified = "UNVERIFIED"
	// StatusIndeterminate — a registry was temporarily unreachable. Retry.
	StatusIndeterminate = "INDETERMINATE"
	// StatusUnsupported — country or number type not covered.
	StatusUnsupported = "UNSUPPORTED"
)

// Validation source preferences (EU only).
const (
	SourceAuto  = "auto"
	SourceVIES  = "vies"
	SourceLocal = "local"
)

// Result is the canonical outcome. Status is the single source of truth.
//
// Deliberately do not branch on Validation.Overall.IsValid: it is true for
// VALID, UNVERIFIED and INDETERMINATE alike, so a format-only pass looks
// identical to a registry-confirmed one.
type Result struct {
	Status   string  `json:"status"`
	Reason   *string `json:"reason"`
	Message  string  `json:"message"`
	Retryable bool   `json:"retryable"`
}

// IsValid reports a registry-confirmed entity. Deliberately narrow:
// UNVERIFIED is not "valid".
func (r Result) IsValid() bool { return r.Status == StatusValid }

// ShouldRetry reports a temporary registry failure worth retrying.
func (r Result) ShouldRetry() bool { return r.Status == StatusIndeterminate }

type StageStatus struct {
	IsValid   *bool  `json:"isValid"`
	Attempted bool   `json:"attempted"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type OverallStatus struct {
	StageStatus
	ValidationMethod string `json:"validationMethod"`
}

type Stages struct {
	Format  StageStatus   `json:"format"`
	Source  StageStatus   `json:"source"`
	Overall OverallStatus `json:"overall"`
}

type Entity struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
	Type    string `json:"type,omitempty"`
	Status  string `json:"status,omitempty"`
}

type TinInfo struct {
	Label        string `json:"label"`
	Name         string `json:"name"`
	FormattedTin string `json:"formattedTin,omitempty"`
}

type ValidationResponse struct {
	ReferenceID string         `json:"referenceId"`
	CountryCode string         `json:"countryCode"`
	RequestDate string         `json:"requestDate"`
	Tin         string         `json:"tin"`
	Result      Result         `json:"result"`
	Validation  Stages         `json:"validation"`
	TinInfo     TinInfo        `json:"tinInfo"`
	Entity      *Entity        `json:"entity,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaxIDInput is one row of a batch submission.
type TaxIDInput struct {
	CountryISO string `json:"country_iso"`
	Tin        string `json:"tin"`
	// Name is required where the registry matches on name (e.g. MX).
	Name string `json:"name,omitempty"`
}

type CreateBatchResponse struct {
	BatchID    string `json:"batch_id"`
	Status     string `json:"status"`
	TotalCount int    `json:"total_count"`
}

type BatchItem struct {
	RequestID        string              `json:"request_id"`
	CountryISO       string              `json:"country_iso"`
	Tin              string              `json:"tin"`
	Status           string              `json:"status"`
	ValidationResult *ValidationResponse `json:"validation_result,omitempty"`
	ErrorCode        string              `json:"error_code,omitempty"`
	ErrorMessage     string              `json:"error_message,omitempty"`
	// NextRetryAt is set while the item is waiting to be retried.
	NextRetryAt string `json:"next_retry_at,omitempty"`
}

type BatchResults struct {
	Status         string      `json:"status"`
	TotalCount     int         `json:"total_count"`
	CompletedCount int         `json:"completed_count"`
	SuccessCount   int         `json:"success_count"`
	ErrorCount     int         `json:"error_count"`
	CanceledCount  int         `json:"canceled_count"`
	Results        []BatchItem `json:"results"`
	NextCursor     *int        `json:"next_cursor"`
}

// IsTerminal reports whether the batch has finished in any way.
func (b BatchResults) IsTerminal() bool {
	return b.Status == "completed" || b.Status == "failed" || b.Status == "canceled"
}

type BatchSummary struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	TotalCount     int    `json:"total_count"`
	CompletedCount int    `json:"completed_count"`
	SuccessCount   int    `json:"success_count"`
	ErrorCount     int    `json:"error_count"`
	UserID         string `json:"user_id,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type BatchList struct {
	Batches []BatchSummary `json:"batches"`
	Total   int            `json:"total"`
	Page    int            `json:"page"`
	Limit   int            `json:"limit"`
}
