package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrAccountNotFound   = errors.New("account does not exist")
	ErrInsufficientFunds = errors.New("account has insufficient funds")
	ErrConflict          = errors.New("idempotency key was used with a different request")
	ErrInvalid           = errors.New("invalid debit")
)

//go:embed schema.sql
var schema string

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback is a no-op after commit; pgx closes the connection on rollback failure.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Serialize initial schema creation across service instances.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(782146052)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type DebitRequest struct {
	AccountID      uuid.UUID
	Key            uuid.UUID
	OrderReference uuid.UUID
	Amount         int64
	Currency       string
}

type DebitResult struct {
	TransactionID  uuid.UUID `json:"transaction_id"`
	OrderReference uuid.UUID `json:"order_reference"`
	Status         string    `json:"status"`
}

// Debit commits the unique operation and conditional balance update together.
// PostgreSQL's unique index waits for an in-flight identical key; account row
// updates serialize distinct keys without an application-local lock.
func (s *Store) Debit(ctx context.Context, req DebitRequest) (DebitResult, error) {
	var result DebitResult
	if req.AccountID == uuid.Nil || req.Key == uuid.Nil || req.OrderReference == uuid.Nil || req.Amount <= 0 ||
		req.Currency != "PHP" {
		return result, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, fmt.Errorf("begin debit: %w", err)
	}
	// Rollback is a no-op after commit; pgx closes the connection on rollback failure.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	result = DebitResult{TransactionID: uuid.New(), OrderReference: req.OrderReference, Status: "completed"}
	// Defer the account FK so replay/conflict detection works for a changed account.
	tag, err := tx.Exec(ctx, `INSERT INTO debit_transactions
  (id, idempotency_key, account_id, order_reference, amount_minor_units, currency)
  VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (idempotency_key) DO NOTHING`,
		result.TransactionID, req.Key, req.AccountID, req.OrderReference, req.Amount, req.Currency)
	if err != nil {
		return result, fmt.Errorf("insert debit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var account, order uuid.UUID
		var amount int64
		var currency string
		err = tx.QueryRow(ctx, `SELECT id, account_id, order_reference, amount_minor_units, currency
   FROM debit_transactions WHERE idempotency_key=$1`, req.Key).
			Scan(&result.TransactionID, &account, &order, &amount, &currency)
		if err != nil {
			return result, fmt.Errorf("read debit: %w", err)
		}
		if account != req.AccountID || order != req.OrderReference || amount != req.Amount || currency != req.Currency {
			return result, ErrConflict
		}
		result.OrderReference = order
		return result, nil
	}
	tag, err = tx.Exec(ctx, `UPDATE accounts SET balance=balance-$2, updated_at=clock_timestamp()
  WHERE id=$1 AND balance >= $2`, req.AccountID, req.Amount)
	if err != nil {
		return result, fmt.Errorf("update balance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)", req.AccountID).
			Scan(&exists); err != nil {
			return result, fmt.Errorf("check account: %w", err)
		}
		if !exists {
			return result, ErrAccountNotFound
		}
		return result, ErrInsufficientFunds
	}
	if err = tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit debit: %w", err)
	}
	return result, nil
}

type Account struct {
	ID        uuid.UUID
	Balance   int64
	UpdatedAt time.Time
}
