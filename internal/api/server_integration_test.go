//go:build integration

package api_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"wallet-kun/internal/api"
	"wallet-kun/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed testdata/seed.sql
var seed string

const funded = "11111111-1111-4111-8111-111111111111"
const limited = "22222222-2222-4222-8222-222222222222"
const order = "33333333-3333-4333-8333-333333333333"

type fixture struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	config *pgxpool.Config
}

func setup(t *testing.T) *fixture {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required with -tags=integration; see README")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	// Each test owns a random schema; never truncate caller-owned tables.
	t.Cleanup(func() {
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 20
	f := &fixture{config: config}
	f.open(t)
	t.Cleanup(func() { f.server.Close(); f.pool.Close() })
	if err := store.New(f.pool).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) open(t *testing.T) {
	t.Helper()
	var err error
	f.pool, err = pgxpool.NewWithConfig(context.Background(), f.config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(api.New(store.New(f.pool)).Handler())
}

func payload(amount int64) string {
	return fmt.Sprintf(`{"order_reference":%q,"amount_minor_units":%d,"currency":"PHP"}`, order, amount)
}

type reply struct {
	status int
	body   store.DebitResult
	raw    string
	err    error
}

func (f *fixture) send(account, key, body string, modify func(*http.Request)) reply {
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/accounts/"+account+"/debits", strings.NewReader(body))
	if err != nil {
		return reply{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())
	req.Header.Set("Idempotency-Key", key)
	if modify != nil {
		modify(req)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return reply{err: err}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return reply{err: err}
	}
	got := reply{status: res.StatusCode, raw: string(data)}
	if res.StatusCode == http.StatusOK {
		got.err = json.Unmarshal(data, &got.body)
	}
	return got
}

func check(t *testing.T, got reply, want int) {
	t.Helper()
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.status != want {
		t.Fatalf("status %d, want %d: %s", got.status, want, got.raw)
	}
	if want == http.StatusOK && (got.body.Status != "completed" || got.body.TransactionID == uuid.Nil || got.body.OrderReference.String() != order) {
		t.Fatalf("unexpected debit response: %+v", got.body)
	}
	if want != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(got.raw), &body); err != nil || body.Error == "" {
			t.Fatalf("invalid error response: %s", got.raw)
		}
	}
}

func (f *fixture) balance(t *testing.T, account string, want int64, count int) {
	t.Helper()
	var balance int64
	var transactions int
	var updated time.Time
	if err := f.pool.QueryRow(context.Background(), "SELECT balance, updated_at FROM accounts WHERE id=$1", account).Scan(&balance, &updated); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM debit_transactions").Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if balance != want || transactions != count {
		t.Fatalf("balance=%d transactions=%d, want %d/%d", balance, transactions, want, count)
	}
	if count > 0 && !updated.After(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("updated_at did not advance")
	}
}

func TestDebitIntegration(t *testing.T) {
	t.Run("completion retry and restart", func(t *testing.T) {
		f := setup(t)
		key := uuid.NewString()
		first := f.send(funded, key, payload(7000), nil)
		check(t, first, 200)
		f.balance(t, funded, 3000, 1)
		// Lose all HTTP and database client state; only PostgreSQL retains the result.
		f.server.Close()
		f.pool.Close()
		f.open(t)
		retry := f.send(funded, key, payload(7000), nil)
		check(t, retry, 200)
		if retry.body != first.body {
			t.Fatalf("retry changed original result: %+v / %+v", first.body, retry.body)
		}
		f.balance(t, funded, 3000, 1)
	})
	t.Run("insufficient funds and failed key rollback", func(t *testing.T) {
		f := setup(t)
		key := uuid.NewString()
		check(t, f.send(limited, key, payload(7000), nil), 422)
		f.balance(t, limited, 5000, 0)
		check(t, f.send(limited, key, payload(5000), nil), 200)
		f.balance(t, limited, 0, 1)
	})
	t.Run("missing account", func(t *testing.T) {
		f := setup(t)
		check(t, f.send(uuid.NewString(), uuid.NewString(), payload(1), nil), 404)
		f.balance(t, funded, 10000, 0)
	})
	t.Run("different keys cannot overdraw", func(t *testing.T) {
		f := setup(t)
		start := make(chan struct{})
		replies := make(chan reply, 2)
		for range 2 {
			go func() { <-start; replies <- f.send(funded, uuid.NewString(), payload(7000), nil) }()
		}
		close(start)
		successes, failures := 0, 0
		for range 2 {
			got := <-replies
			if got.status == 200 {
				check(t, got, 200)
				successes++
			} else {
				check(t, got, 422)
				failures++
			}
		}
		if successes != 1 || failures != 1 {
			t.Fatalf("successes=%d failures=%d", successes, failures)
		}
		f.balance(t, funded, 3000, 1)
	})
	t.Run("concurrent identical retries", func(t *testing.T) {
		f := setup(t)
		key := uuid.NewString()
		start := make(chan struct{})
		replies := make(chan reply, 16)
		var ready sync.WaitGroup
		ready.Add(16)
		for range 16 {
			go func() { ready.Done(); <-start; replies <- f.send(funded, key, payload(7000), nil) }()
		}
		ready.Wait()
		close(start)
		var id uuid.UUID
		for range 16 {
			got := <-replies
			check(t, got, 200)
			if id != uuid.Nil && got.body.TransactionID != id {
				t.Fatal("duplicate transaction created")
			}
			id = got.body.TransactionID
		}
		f.balance(t, funded, 3000, 1)
	})
	t.Run("key conflicts compare full operation", func(t *testing.T) {
		f := setup(t)
		key := uuid.NewString()
		check(t, f.send(funded, key, payload(7000), nil), 200)
		check(t, f.send(funded, key, payload(1), nil), 409)
		check(t, f.send(limited, key, payload(7000), nil), 409)
		check(t, f.send(uuid.NewString(), key, payload(7000), nil), 409)
		changedOrder := strings.ReplaceAll(payload(7000), order, uuid.NewString())
		check(t, f.send(funded, key, changedOrder, nil), 409)
		// JSON representation does not affect idempotency.
		body := fmt.Sprintf(`{ "currency":"PHP", "amount_minor_units":7000, "order_reference":%q }`, order)
		check(t, f.send(funded, key, body, nil), 200)
		f.balance(t, funded, 3000, 1)
	})
	t.Run("database constraints", func(t *testing.T) {
		f := setup(t)
		_, err := f.pool.Exec(context.Background(), "UPDATE accounts SET balance=-1 WHERE id=$1", funded)
		pgErr, ok := err.(*pgconn.PgError)
		if !ok || pgErr.Code != "23514" {
			t.Fatalf("expected CHECK violation, got %v", err)
		}
		f.balance(t, funded, 10000, 0)
	})
	t.Run("canceled operation rolls back and can retry", func(t *testing.T) {
		f := setup(t)
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(context.Background(), "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", funded); err != nil {
			t.Fatal(err)
		}
		key := uuid.New()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_, err = store.New(f.pool).Debit(ctx, store.DebitRequest{AccountID: uuid.MustParse(funded), Key: key, OrderReference: uuid.MustParse(order), Amount: 7000, Currency: "PHP"})
		if err == nil {
			t.Fatal("expected cancellation while account is locked")
		}
		if err := tx.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		check(t, f.send(funded, key.String(), payload(7000), nil), 200)
		f.balance(t, funded, 3000, 1)
	})
	t.Run("request validation", func(t *testing.T) {
		f := setup(t)
		cases := []struct {
			name, body string
			modify     func(*http.Request)
		}{
			{name: "missing request ID", body: payload(1), modify: func(r *http.Request) { r.Header.Del("X-Request-ID") }},
			{name: "malformed request ID", body: payload(1), modify: func(r *http.Request) { r.Header.Set("X-Request-ID", "bad") }},
			{name: "missing key", body: payload(1), modify: func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
			{name: "zero key", body: payload(1), modify: func(r *http.Request) { r.Header.Set("Idempotency-Key", uuid.Nil.String()) }},
			{name: "content type", body: payload(1), modify: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
			{name: "negative", body: payload(-1)},
			{name: "zero", body: payload(0)},
			{name: "missing fields", body: `{}`},
			{name: "null", body: `null`},
			{name: "malformed", body: `{`},
			{name: "unsupported currency", body: strings.ReplaceAll(payload(1), "PHP", "USD")},
			{name: "unknown field", body: strings.Replace(payload(1), "{", `{"extra":1,`, 1)},
			{name: "overflow", body: strings.ReplaceAll(payload(1), `"amount_minor_units":1`, `"amount_minor_units":9223372036854775808`)},
			{name: "fraction", body: strings.ReplaceAll(payload(1), `"amount_minor_units":1`, `"amount_minor_units":1.5`)},
			{name: "multiple JSON values", body: payload(1) + ` {}`},
			{name: "oversized", body: payload(1) + strings.Repeat(" ", 16<<10)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) { check(t, f.send(funded, uuid.NewString(), tc.body, tc.modify), 400) })
		}
		check(t, f.send("bad", uuid.NewString(), payload(1), nil), 400)
		f.balance(t, funded, 10000, 0)
	})
	t.Run("health reflects database availability", func(t *testing.T) {
		f := setup(t)
		response, err := http.Get(f.server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal(response.StatusCode)
		}
		f.pool.Close()
		response, err = http.Get(f.server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 503 {
			t.Fatal(response.StatusCode)
		}
	})
}
