package store

import (
	"sync"
	"time"
)

type Store struct {
	accounts map[string]*Account
	mu       sync.Mutex
}

func New() *Store {
	return &Store{
		accounts: make(map[string]*Account),
	}
}

func (store *Store) Debit(accountID string, amount int64) error {
	panic("implement me")
}

type Account struct {
	Id        string
	Balance   int64
	UpdatedAt time.Time
}
