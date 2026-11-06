package store_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"wallet-kun/internal/store"

	"github.com/google/uuid"
)

func TestInvalidDebitIsRejectedBeforeDatabaseAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*store.DebitRequest)
	}{
		{"missing account", func(r *store.DebitRequest) { r.AccountID = uuid.Nil }},
		{"missing key", func(r *store.DebitRequest) { r.Key = uuid.Nil }},
		{"missing order reference", func(r *store.DebitRequest) { r.OrderReference = uuid.Nil }},
		{"zero amount", func(r *store.DebitRequest) { r.Amount = 0 }},
		{"negative amount", func(r *store.DebitRequest) { r.Amount = -1 }},
		{"minimum int64", func(r *store.DebitRequest) { r.Amount = math.MinInt64 }},
		{"missing currency", func(r *store.DebitRequest) { r.Currency = "" }},
		{"unsupported currency", func(r *store.DebitRequest) { r.Currency = "USD" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := store.DebitRequest{AccountID: uuid.New(), Key: uuid.New(), OrderReference: uuid.New(), Amount: 1, Currency: "PHP"}
			tc.modify(&req)
			// A nil pool ensures invalid input never reaches the database.
			result, err := store.New(nil).Debit(context.Background(), req)
			if !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("error=%v, want ErrInvalid", err)
			}
			if result != (store.DebitResult{}) {
				t.Fatalf("invalid debit returned a result: %+v", result)
			}
		})
	}
}
