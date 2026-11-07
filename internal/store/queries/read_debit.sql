SELECT id, account_id, order_reference, amount_minor_units, currency
   FROM debit_transactions WHERE idempotency_key=$1
