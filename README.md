# Wallet-kun

A small wallet service for demonstrating high throughput and scalable service.

## Specifications

[`spec.yaml`](spec.yaml) is the API contract. The implementation contract below specifies storage, logging, and runtime behavior.

### Debits

#### Request

1. Must contain `X-Request-ID` header for tracing HTTP requests. Must be easily shareable with low collision probability.
2. Must contain `Idempotency-Key` header for handling duplicate transactions or a retried request.
3. Account ID is required from a request URL path.
4. Amount should be in cent value and must be greater than zero.

#### Response

1. A `completed` transaction should be returned when using a previous identical completed request.
2. For this iteration, return HTTP 200 with `completed` after the database commits. HTTP 202 with `accepted` is reserved for future Kafka processing.
3. Return validation errors, if any.
4. Return account or balance errors, if any.

#### Rules

1. `Idempotency-Key` must be unique for each transaction.
2. Reject a transaction when it has a previously used `Idempotency-Key` header but different request payload.
3. Account balance must never be a negative value.
4. Debit amount must never be a negative value.
5. Debit amount must never be greater than the account balance.
6. Committed requests must remain recoverable across restarts. Future asynchronous accepted requests must also be recoverable.

#### Scenarios

| Scenario                                                           | Expected result                                                |
|--------------------------------------------------------------------|----------------------------------------------------------------|
| Debit 7,000 from a balance of 10,000                               | Completes; balance becomes 3,000                               |
| Debit 7,000 from a balance of 5,000                                | Fails for insufficient funds; balance remains 5,000            |
| Two distinct 7,000 debits arrive concurrently against 10,000       | Exactly one completes; the other fails; final balance is 3,000 |
| Retry a completed debit with the same idempotency key and payload  | Returns the original result; no additional deduction           |
| Retry an in-flight debit with the same idempotency key and payload | Waits for the database transaction; no second operation        |
| Reuse an idempotency key with a different amount                   | Conflict; no additional deduction                              |
| Restart after commit                                               | The operation remains recoverable and cannot debit twice       |

### Implementation contract

- New valid debits commit the balance change and completed transaction in one PostgreSQL transaction before returning HTTP 200 (`status: completed`). An identical retry returns HTTP 200 with the original transaction ID. HTTP 202 (`status: accepted`) remains in the API contract for future Kafka processing and is not emitted in this iteration. There is no background queue or externally visible pending state yet.
- A concurrent identical retry waits for the first database transaction and returns its result. A crash before commit leaves neither deduction nor transaction; after commit, retrying recovers the result without a second deduction.
- Idempotency keys are global UUIDs. Equality compares account ID, order reference, amount, and currency. JSON whitespace and field ordering do not matter. Failed requests do not consume keys.
- `X-Request-ID`, `Idempotency-Key`, account ID, and order reference must be nonzero UUIDs. All request fields are required; only PHP is supported. Amount is a positive signed 64-bit integer. Unknown fields, multiple JSON values, and bodies larger than 16 KiB are rejected.
- PostgreSQL enforces nonnegative account balances. Conditional updates serialize competing debits at the account row; a unique idempotency key serializes retries across service instances.
- Errors: 400 invalid request; 404 account missing; 409 key reused with different request; 422 insufficient funds; 500 unexpected internal failure; 503 unavailable health check. Authentication/authorization is outside this iteration; 403 is reserved for future authorization.
- Logs use structured JSON with request ID, route, status, latency, and safe database error codes. Do not log payloads, account IDs, idempotency keys, database URLs, or raw database errors that may contain sensitive details.
- Production is the default (`APP_ENV=production`): rotate `LOG_FILE` (default `logs/wallet-kun.log`) at 10 MiB, retaining 5 backups for 30 days. `APP_ENV=development` writes JSON to the console.

## Run

The application and its libraries build with `CGO_ENABLED=0` (pure Go). PostgreSQL is the only external runtime dependency. The service initializes the schema at startup; accounts must already be provisioned in the database. No account-management endpoints are included.

```powershell
$env:POSTGRES_PASSWORD = 'replace-with-a-URL-safe-password'
docker compose up --build -d
```

The API listens on port 5000. Database and rotating log files use named Docker volumes. Production logging is the default. Local development:

```powershell
$env:APP_ENV = 'development'
$env:DATABASE_URL = 'postgres://user:password@localhost:5432/wallet?sslmode=disable'
$env:CGO_ENABLED = '0'
go run ./cmd/wallet-kun
```

Optional environment variables: `HTTP_ADDR` (default `:5000`) and `LOG_FILE` (default `logs/wallet-kun.log`). `GET /healthz` checks database connectivity. A request example for an existing seeded account:

```http
POST /accounts/11111111-1111-4111-8111-111111111111/debits
Content-Type: application/json
X-Request-ID: 44444444-4444-4444-8444-444444444444
Idempotency-Key: 55555555-5555-4555-8555-555555555555

{"order_reference":"33333333-3333-4333-8333-333333333333","amount_minor_units":7000,"currency":"PHP"}
```

The response is HTTP 200 with `transaction_id`, `order_reference`, and `status: completed`. Retry with the same idempotency key if a connection fails or times out: a commit may have succeeded even when the response was lost.

## Integration tests

Tests use real PostgreSQL, create a random isolated schema per test, apply the application schema, seed accounts from `internal/api/testdata/seed.sql`, and drop only their own schemas afterward. The supplied test container uses ephemeral storage and binds only to localhost.

```powershell
docker compose -f compose.test.yaml up -d --wait
$env:TEST_DATABASE_URL = 'postgres://wallet_test:wallet_test@localhost:55432/wallet_test?sslmode=disable'
$env:CGO_ENABLED = '0'
go test -tags=integration -count=1 ./...
go vet -tags=integration ./...
docker compose -f compose.test.yaml down
```

The integration tag requires `TEST_DATABASE_URL`; it fails instead of silently skipping database tests. Coverage includes validation, insufficient funds, concurrent distinct debits, concurrent retries, full-operation idempotency conflicts, connection restart recovery, cancellation rollback, and the database balance constraint.

## Unit tests

Run `go test ./...` without PostgreSQL or Docker. Unit tests cover HTTP validation before storage access, request mapping and deadlines, domain error status codes, safe internal-error responses and logs, panic recovery, health checks, store input guards, logging environment selection and setup failures, and startup configuration errors. Database concurrency, transaction rollback, and durable idempotency remain covered by the seeded integration suite.

```powershell
$env:CGO_ENABLED = '0'
go test -count=1 ./...
go vet ./...
golangci-lint run ./...
```

### PostgreSQL startup credentials

Compose reads `POSTGRES_PASSWORD` from the shell or an ignored local `.env` file. With a persistent database volume, this value must match the stored PostgreSQL `wallet` role password. The PostgreSQL image only applies `POSTGRES_PASSWORD` when initializing an empty data directory; recreating containers does not change an existing role password. A healthy `pg_isready` check confirms readiness, not successful application authentication.

If API startup fails, inspect `/app/logs/wallet-kun.log` in its log volume and the database logs. A PostgreSQL `password authentication failed` error requires restoring the original password or updating the stored role password to match the deployment configuration. Preserve the `postgres_data` volume during this repair.
