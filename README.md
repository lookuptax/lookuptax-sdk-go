# LookupTax SDK for Go

Validate tax IDs — VAT, EIN, GSTIN, ABN and more — against official government registries.

[lookuptax.com](https://lookuptax.com)

```bash
go get github.com/lookuptax/lookuptax-sdk-go
```

Go 1.21+. No third-party dependencies.

## Quick start

```go
client, err := lookuptax.New(os.Getenv("LOOKUPTAX_API_KEY"))
if err != nil {
    log.Fatal(err)
}

res, err := client.Validate(context.Background(), "IE", "53057102A", nil)
if err != nil {
    log.Fatal(err) // request error: auth, quota, rate limit…
}

if res.Result.IsValid() {
    fmt.Println("Confirmed:", res.Entity.Name)
}
```

## Read `result.status`, not `isValid`

Every response carries a `result` whose `status` is the single source of truth. Five mutually exclusive values:

| `result.status` | Meaning |
|---|---|
| `VALID` | Format passed and a government registry confirmed the entity. |
| `INVALID` | Definitive negative — bad structure/check digit, or no registry record. |
| `UNVERIFIED` | No queryable registry for that country, so only the format was checked. A pass is **not** proof the business exists. |
| `INDETERMINATE` | A registry was temporarily unreachable. Retry — do not treat as invalid. |
| `UNSUPPORTED` | We don't validate that country or number type yet. |

Do **not** branch on `validation.overall.isValid`: it is true for `VALID`, `UNVERIFIED` **and** `INDETERMINATE` alike, so a format-only pass looks identical to a registry-confirmed one. `result.status` exists to remove that ambiguity.

## Errors vs outcomes

- **Validation outcomes are returned.** An `INVALID` tax ID is a *successful* call. `UNSUPPORTED` is returned too, even though it arrives as HTTP 422, because the body still carries a `result`.
- **Request errors are raised/returned as errors.** Authentication, quota, rate limit, malformed request — the call could not be processed at all.

Branch on the error's machine-readable `code`, never on its message. Transient failures (429, 500, 503) retry automatically, honouring `Retry-After`. Batch submission is **never** retried automatically: a retried submission that actually succeeded would create a second batch and reserve quota twice.

## EU routing

Set the validation source to pin which authority answers: `auto` (default), `vies`, or `local`. With `vies` or `local` there is no fallback to the other — an unavailable source yields `INDETERMINATE` rather than a silent answer from the other one, which is what makes it usable for reconciliation. Ignored outside VIES-covered countries.

## Countries needing a name

Some registries match on name as well as number (e.g. Mexico). Pass it via the additional-params map; it is sent as `additional_params[name]`.

## Batch

Submit up to 100 IDs, validated asynchronously. Enterprise plan only. Poll to a terminal state, then page through results following `next_cursor`. Polling is relaxed by design: when a registry is unreachable an item keeps retrying for roughly three days, carrying a `next_retry_at` — a long-running item is not a stuck one.

## Configuration

The API root is configurable (default `https://api.lookuptax.com/v1`) because the published path prefix is deployment configuration rather than a property of the SDK.

## API

| Method | Endpoint |
|---|---|
| validate | `GET /validate` |
| createBatch | `POST /batch` |
| getBatch | `GET /batch/{id}` |
| listBatches | `GET /batch` |
| cancelBatch | `POST /batch/{id}/cancel` |
| deleteBatch | `DELETE /batch/{id}` |
| getTaxId | `GET /batch/tax-id/{id}` |

## License

MIT
