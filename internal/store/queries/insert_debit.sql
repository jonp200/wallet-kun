INSERT INTO debit_transactions
  (id, idempotency_key, account_id, order_reference, amount_minor_units, currency)
  VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (idempotency_key) DO NOTHING
