CREATE TABLE IF NOT EXISTS accounts (
    id uuid PRIMARY KEY,
    balance bigint NOT NULL CHECK (balance >= 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Rows represent completed debits only. No partial operation survives rollback.
CREATE TABLE IF NOT EXISTS debit_transactions (
    id uuid PRIMARY KEY,
    idempotency_key uuid NOT NULL UNIQUE,
    account_id uuid NOT NULL REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED,
    order_reference uuid NOT NULL,
    amount_minor_units bigint NOT NULL CHECK (amount_minor_units > 0),
    currency text NOT NULL CHECK (currency = 'PHP'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
