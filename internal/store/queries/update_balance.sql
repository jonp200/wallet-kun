UPDATE accounts SET balance=balance-$2, updated_at=clock_timestamp()
  WHERE id=$1 AND balance >= $2
