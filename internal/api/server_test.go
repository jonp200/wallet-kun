package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"wallet-kun/internal/api"
	"wallet-kun/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const unitAccount = "a1111111-1111-4111-8111-111111111111"
const unitKey = "b2222222-2222-4222-8222-222222222222"
const unitOrder = "c3333333-3333-4333-8333-333333333333"
const unitRequestID = "d4444444-4444-4444-8444-444444444444"
const unitBody = `{"order_reference":"` + unitOrder + `","amount_minor_units":7000,"currency":"PHP"}`

type stubStore struct {
	debit func(context.Context, store.DebitRequest) (store.DebitResult, error)
	ping  func(context.Context) error
}

func (s stubStore) Debit(ctx context.Context, req store.DebitRequest) (store.DebitResult, error) {
	if s.debit == nil {
		panic("unexpected storage access")
	}
	return s.debit(ctx, req)
}

func (s stubStore) Ping(ctx context.Context) error {
	if s.ping == nil {
		panic("unexpected health check")
	}
	return s.ping(ctx)
}

func unitHandler(t *testing.T, st stubStore, output io.Writer) http.Handler {
	t.Helper()
	mode, logger := gin.Mode(), slog.Default()
	gin.SetMode(gin.ReleaseMode)
	slog.SetDefault(slog.New(slog.NewJSONHandler(output, nil)))
	t.Cleanup(func() {
		gin.SetMode(mode)
		slog.SetDefault(logger)
	})
	return api.New(st).Handler()
}

func unitRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/accounts/"+unitAccount+"/debits", strings.NewReader(unitBody))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-Request-ID", unitRequestID)
	req.Header.Set("Idempotency-Key", unitKey)
	return req
}

func assertUnitError(t *testing.T, response *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, status, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", response.Header().Get("Content-Type"))
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("invalid error body: %s", response.Body.String())
	}
	if message != "" && body.Error != message {
		t.Fatalf("error = %q, want %q", body.Error, message)
	}
}

func TestDebitMapsRequestAndReturnsCompletedResult(t *testing.T) {
	want := store.DebitRequest{
		AccountID: uuid.MustParse(unitAccount), Key: uuid.MustParse(unitKey),
		OrderReference: uuid.MustParse(unitOrder), Amount: 7000, Currency: "PHP",
	}
	result := store.DebitResult{TransactionID: uuid.New(), OrderReference: want.OrderReference, Status: "completed"}
	calls := 0
	var contextErr func() error
	handler := unitHandler(
		t,
		stubStore{debit: func(ctx context.Context, got store.DebitRequest) (store.DebitResult, error) {
			calls++
			if got != want {
				t.Errorf("request = %+v, want %+v", got, want)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
				t.Errorf("expected a live deadline within ten seconds, got %v", deadline)
			}
			contextErr = ctx.Err
			return result, nil
		}},
		io.Discard,
	)
	response := httptest.NewRecorder()
	req := unitRequest()
	// UUIDs are compared semantically, and the trace header is normalized.
	req.Header.Set("X-Request-ID", strings.ToUpper(unitRequestID))
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	var got store.DebitResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got != result {
		t.Fatalf("response=%s, want %+v", response.Body.String(), result)
	}
	if response.Header().Get("X-Request-ID") != unitRequestID {
		t.Fatal("request ID was not echoed in canonical form")
	}
	if !errors.Is(contextErr(), context.Canceled) {
		t.Fatal("request timeout context was not released after responding")
	}
}

func TestDebitAcceptsAmountBoundariesAndReorderedFields(t *testing.T) {
	for _, amount := range []int64{1, math.MaxInt64} {
		t.Run(strconv.FormatInt(amount, 10), func(t *testing.T) {
			calls := 0
			handler := unitHandler(
				t,
				stubStore{debit: func(_ context.Context, req store.DebitRequest) (store.DebitResult, error) {
					calls++
					if req.Amount != amount || req.OrderReference.String() != unitOrder || req.Currency != "PHP" {
						t.Fatalf("unexpected debit fields: %+v", req)
					}
					return store.DebitResult{
						TransactionID:  uuid.New(),
						OrderReference: req.OrderReference,
						Status:         "completed",
					}, nil
				}},
				io.Discard,
			)
			req := unitRequest()
			req.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(
				`{ "currency": "PHP", "amount_minor_units": %d, "order_reference": %q }`, amount, unitOrder,
			)))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestDebitRejectsInvalidRequestsBeforeStorage(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*http.Request)
		body   string
	}{
		{name: "malformed account", modify: func(r *http.Request) { r.URL.Path = "/accounts/bad/debits" }},
		{
			name:   "zero account",
			modify: func(r *http.Request) { r.URL.Path = "/accounts/" + uuid.Nil.String() + "/debits" },
		},
		{name: "missing request ID", modify: func(r *http.Request) { r.Header.Del("X-Request-ID") }},
		{
			name:   "malformed request ID",
			modify: func(r *http.Request) { r.Header.Set("X-Request-ID", "private-invalid-header") },
		},
		{name: "zero request ID", modify: func(r *http.Request) { r.Header.Set("X-Request-ID", uuid.Nil.String()) }},
		{name: "missing key", modify: func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{name: "malformed key", modify: func(r *http.Request) { r.Header.Set("Idempotency-Key", "invalid") }},
		{name: "zero key", modify: func(r *http.Request) { r.Header.Set("Idempotency-Key", uuid.Nil.String()) }},
		{
			name:   "compact UUID key",
			modify: func(r *http.Request) { r.Header.Set("Idempotency-Key", strings.ReplaceAll(unitKey, "-", "")) },
		},
		{name: "missing content type", modify: func(r *http.Request) { r.Header.Del("Content-Type") }},
		{name: "wrong content type", modify: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{
			name:   "malformed content type",
			modify: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; broken") },
		},
		{name: "empty object", body: `{}`},
		{name: "null", body: `null`},
		{name: "malformed JSON", body: `{`},
		{name: "array", body: `[]`},
		{name: "negative amount", body: strings.Replace(unitBody, "7000", "-1", 1)},
		{name: "zero amount", body: strings.Replace(unitBody, "7000", "0", 1)},
		{name: "fractional amount", body: strings.Replace(unitBody, "7000", "1.5", 1)},
		{name: "overflow", body: strings.Replace(unitBody, "7000", "9223372036854775808", 1)},
		{name: "missing amount", body: strings.Replace(unitBody, `"amount_minor_units":7000,`, "", 1)},
		{name: "invalid order", body: strings.Replace(unitBody, unitOrder, "bad", 1)},
		{name: "zero order", body: strings.Replace(unitBody, unitOrder, uuid.Nil.String(), 1)},
		{name: "missing currency", body: strings.Replace(unitBody, `,"currency":"PHP"`, "", 1)},
		{name: "unsupported currency", body: strings.Replace(unitBody, "PHP", "USD", 1)},
		{name: "unknown field", body: strings.Replace(unitBody, "{", `{"unexpected":true,`, 1)},
		{name: "wrong case order field", body: strings.Replace(unitBody, "order_reference", "Order_Reference", 1)},
		{
			name: "wrong case amount field",
			body: strings.Replace(unitBody, "amount_minor_units", "AMOUNT_MINOR_UNITS", 1),
		},
		{name: "wrong case currency field", body: strings.Replace(unitBody, "currency", "Currency", 1)},
		{name: "extra case variant field", body: strings.Replace(unitBody, "{", `{"Currency":"PHP",`, 1)},
		{name: "null amount", body: strings.Replace(unitBody, "7000", "null", 1)},
		{name: "null order", body: strings.Replace(unitBody, `"`+unitOrder+`"`, "null", 1)},
		{name: "null currency", body: strings.Replace(unitBody, `"PHP"`, "null", 1)},
		{name: "multiple values", body: unitBody + ` {}`},
		{name: "trailing garbage", body: unitBody + `x`},
		{name: "oversized", body: unitBody + strings.Repeat(" ", 16<<10)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := unitHandler(
				t,
				stubStore{debit: func(context.Context, store.DebitRequest) (store.DebitResult, error) {
					calls++
					return store.DebitResult{}, nil
				}},
				io.Discard,
			)
			req := unitRequest()
			if tc.body != "" {
				req.Body = io.NopCloser(strings.NewReader(tc.body))
			}
			if tc.modify != nil {
				tc.modify(req)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			assertUnitError(t, response, http.StatusBadRequest, "")
			if calls != 0 {
				t.Fatal("invalid request reached storage")
			}
		})
	}
}

func TestDebitDomainErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"account missing", store.ErrAccountNotFound, http.StatusNotFound, store.ErrAccountNotFound.Error()},
		{"insufficient funds", store.ErrInsufficientFunds, http.StatusUnprocessableEntity, store.ErrInsufficientFunds.Error()},
		{"key conflict", store.ErrConflict, http.StatusConflict, store.ErrConflict.Error()},
		{"invalid debit", store.ErrInvalid, http.StatusBadRequest, store.ErrInvalid.Error()},
		{"wrapped missing account", fmt.Errorf("private account: %w", store.ErrAccountNotFound), http.StatusNotFound, store.ErrAccountNotFound.Error()},
		{"wrapped insufficient funds", fmt.Errorf("private balance: %w", store.ErrInsufficientFunds), http.StatusUnprocessableEntity, store.ErrInsufficientFunds.Error()},
		{"wrapped conflict", fmt.Errorf("private key: %w", store.ErrConflict), http.StatusConflict, store.ErrConflict.Error()},
		{"wrapped invalid", fmt.Errorf("private payload: %w", store.ErrInvalid), http.StatusBadRequest, store.ErrInvalid.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := unitHandler(
				t,
				stubStore{debit: func(context.Context, store.DebitRequest) (store.DebitResult, error) {
					return store.DebitResult{}, tc.err
				}},
				io.Discard,
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, unitRequest())
			assertUnitError(t, response, tc.status, tc.message)
		})
	}
}

func TestDebitRespectsParentContext(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			deadline, _ := parent.Deadline()
			if canceled {
				cancel()
			}
			handler := unitHandler(
				t,
				stubStore{debit: func(ctx context.Context, _ store.DebitRequest) (store.DebitResult, error) {
					if canceled && !errors.Is(ctx.Err(), context.Canceled) {
						t.Error("parent cancellation was not propagated")
					}
					if got, ok := ctx.Deadline(); !ok || got != deadline {
						t.Errorf("caller deadline changed: %v, want %v", got, deadline)
					}
					if canceled {
						return store.DebitResult{}, ctx.Err()
					}
					return store.DebitResult{}, context.DeadlineExceeded
				}},
				io.Discard,
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, unitRequest().WithContext(parent))
			assertUnitError(t, response, http.StatusInternalServerError, "internal server error")
		})
	}
}

func TestInternalFailuresAreSanitizedAndTraced(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		panicValue  any
		wantMessage string
		wantField   string
	}{
		{name: "database", err: fmt.Errorf("execute debit: %w", &pgconn.PgError{Code: "08006", Message: "secret-db-detail", Detail: "private-payload"}), wantMessage: "debit failed", wantField: "sqlstate"},
		{name: "timeout", err: fmt.Errorf("secret-db-detail: %w", context.DeadlineExceeded), wantMessage: "debit failed", wantField: "timeout"},
		{name: "unexpected", err: errors.New("secret-db-detail private-payload"), wantMessage: "debit failed"},
		{name: "panic", panicValue: "secret-db-detail private-payload", wantMessage: "http handler panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			handler := unitHandler(
				t,
				stubStore{debit: func(context.Context, store.DebitRequest) (store.DebitResult, error) {
					if tc.panicValue != nil {
						panic(tc.panicValue)
					}
					return store.DebitResult{}, tc.err
				}},
				&output,
			)
			req := unitRequest()
			req.URL.RawQuery = "customer=private-query"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			assertUnitError(t, response, http.StatusInternalServerError, "internal server error")
			for _, secret := range []string{"secret-db-detail", "private-payload", "private-query", unitAccount, unitKey, unitOrder} {
				if strings.Contains(output.String()+response.Body.String(), secret) {
					t.Fatalf("sensitive detail leaked: %q", secret)
				}
			}
			failure, access := failureLogs(t, output.String())
			assertFailureLog(t, failure, tc.wantMessage, tc.wantField)
			assertAccessLog(t, access)
		})
	}
}

func assertFailureLog(t *testing.T, failure map[string]any, message, field string) {
	t.Helper()
	if failure["msg"] != message || failure["level"] != "ERROR" {
		t.Fatalf("unexpected error log: %v", failure)
	}
	if field == "sqlstate" && failure["sqlstate"] != "08006" {
		t.Fatalf("SQLSTATE missing: %v", failure)
	}
	if field == "timeout" && failure["timeout"] != true {
		t.Fatalf("timeout metric missing: %v", failure)
	}
}

func failureLogs(t *testing.T, output string) (map[string]any, map[string]any) {
	t.Helper()
	entries := strings.Split(strings.TrimSpace(output), "\n")
	if len(entries) != 2 {
		t.Fatalf("expected error and access logs, got %s", output)
	}
	var failure, access map[string]any
	if err := json.Unmarshal([]byte(entries[0]), &failure); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(entries[1]), &access); err != nil {
		t.Fatal(err)
	}
	if failure["request_id"] != unitRequestID || failure["route"] != "/accounts/:id/debits" {
		t.Fatalf("failure log lacks safe tracing fields: %v", failure)
	}
	return failure, access
}

func assertAccessLog(t *testing.T, access map[string]any) {
	t.Helper()
	if access["request_id"] != unitRequestID || access["route"] != "/accounts/:id/debits" ||
		access["status"] != float64(500) ||
		access["duration_ms"] == nil {
		t.Fatalf("access log lacks safe tracing fields: %v", access)
	}
}

func TestInvalidTraceHeaderIsNotEchoedOrLogged(t *testing.T) {
	var output bytes.Buffer
	handler := unitHandler(t, stubStore{}, &output)
	req := unitRequest()
	req.Header.Set("X-Request-ID", "private-invalid-header")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	assertUnitError(t, response, http.StatusBadRequest, "")
	if response.Header().Get("X-Request-ID") != "" || strings.Contains(output.String(), "private-invalid-header") {
		t.Fatal("unvalidated trace header was echoed or logged")
	}
}

func TestHealthCheck(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%t", unavailable), func(t *testing.T) {
			calls := 0
			handler := unitHandler(t, stubStore{ping: func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
					t.Error("health check must have a bounded deadline")
				}
				if unavailable {
					return errors.New("secret connection string")
				}
				return nil
			}}, io.Discard)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if unavailable {
				assertUnitError(t, response, http.StatusServiceUnavailable, "database unavailable")
			} else if response.Code != http.StatusOK || response.Body.String() != `{"status":"ok"}` {
				t.Fatalf("unexpected health response: %d %s", response.Code, response.Body.String())
			}
			if calls != 1 {
				t.Fatalf("health calls=%d, want 1", calls)
			}
		})
	}
}
