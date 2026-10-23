# Wallet-kun

A small wallet service for demonstrating a high throughput and scalable service.

## Specifications

### Debits

#### Request

1. Must contain `X-Request-ID` header for tracing HTTP requests. Ideally, must be unique for each new HTTP request.
2. Must contain `Idempotency-Key` header for handling duplicate transactions or a retried request.
3. Account ID is required from request URL path.
4. Amount should be in cents value and must be greater than zero.

#### Response

1. A completed transaction should be returned when using a previous identical completed request.
2. An accepted response should be returned when for new transactions.
3. Return validation errors, if any.
4. Return account or balance errors, if any.

#### Rules

1. `Idempotency-Key` must be unique for each transaction.
2. Reject a transaction when it has a previously used `Idempotency-Key` header but different request payload.
3. Account balance must never be a negative value.
4. Debit amount must never be greater than the account balance.
5. Accepted requests should be recoverable when the service restarts or when resumed from being stopped.

#### Scenarios

| Scenario | Expected result |
|---|---|
| Debit 7,000 from a balance of 10,000 | Completes; balance becomes 3,000 |
| Debit 7,000 from a balance of 5,000 | Fails for insufficient funds; balance remains 5,000 |
| Two distinct 7,000 debits arrive concurrently against 10,000 | Exactly one completes; the other fails; final balance is 3,000 |
| Retry a completed debit with the same idempotency key and payload | Returns the original result; no additional deduction |
| Retry a pending debit with the same idempotency key and payload | Returns the existing operation’s status; no second operation |
| Reuse an idempotency key with a different amount | Conflict; no additional deduction |
| Restart after acceptance | The operation remains recoverable and cannot debit twice |