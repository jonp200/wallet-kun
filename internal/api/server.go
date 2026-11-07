package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"wallet-kun/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type walletStore interface {
	Debit(context.Context, store.DebitRequest) (store.DebitResult, error)
	Ping(context.Context) error
}

type Server struct{ router *gin.Engine }

const (
	healthTimeout       = 2 * time.Second
	debitTimeout        = 10 * time.Second
	maxBodyBytes        = 16 << 10
	canonicalUUIDLength = 36
	debitFieldCount     = 3
)

type errorResponse struct {
	Error string `json:"error"`
}

type debitBody struct {
	OrderReference string `json:"order_reference"`
	Amount         int64  `json:"amount_minor_units"`
	Currency       string `json:"currency"`
}

func New(st walletStore) *Server {
	logger := slog.Default()
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(logging(logger), recovery(logger))
	r.GET("/healthz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), healthTimeout)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			logFailure(c, logger, "database health check failed", err)
			c.JSON(http.StatusServiceUnavailable, errorResponse{Error: "database unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.POST("/accounts/:id/debits", func(c *gin.Context) { debit(c, st, logger) })
	return &Server{router: r}
}

func (s *Server) Handler() http.Handler { return s.router }

func parseUUID(value string) (uuid.UUID, error) {
	if len(value) != canonicalUUIDLength {
		return uuid.Nil, errors.New("invalid UUID")
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("invalid UUID")
	}
	return id, nil
}

func debit(c *gin.Context, st walletStore, logger *slog.Logger) {
	invalid := func(message string) { c.JSON(http.StatusBadRequest, errorResponse{Error: message}) }
	account, err := parseUUID(c.Param("id"))
	if err != nil {
		invalid("account ID must be a nonzero UUID")
		return
	}
	if _, err = parseUUID(c.GetHeader("X-Request-ID")); err != nil {
		invalid("X-Request-ID must be a nonzero UUID")
		return
	}
	key, err := parseUUID(c.GetHeader("Idempotency-Key"))
	if err != nil {
		invalid("Idempotency-Key must be a nonzero UUID")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		invalid("Content-Type must be application/json")
		return
	}
	body, err := decodeDebitBody(c)
	if err != nil {
		invalid(err.Error())
		return
	}
	order, err := parseUUID(body.OrderReference)
	if err != nil || body.Amount <= 0 || body.Currency != "PHP" {
		invalid("order_reference UUID, positive int64 amount_minor_units, and currency PHP are required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), debitTimeout)
	defer cancel()
	result, err := st.Debit(
		ctx,
		store.DebitRequest{
			AccountID:      account,
			Key:            key,
			OrderReference: order,
			Amount:         body.Amount,
			Currency:       body.Currency,
		},
	)
	if err != nil {
		debitError(c, logger, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func decodeDebitBody(c *gin.Context) (debitBody, error) {
	var body debitBody
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return body, errors.New("invalid JSON request")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return body, errors.New("request must contain one JSON object")
	}
	if len(fields) != debitFieldCount {
		return body, errors.New("all three debit fields are required")
	}
	// Map lookups enforce the exact property names required by the API contract.
	for name, target := range map[string]any{
		"order_reference":    &body.OrderReference,
		"amount_minor_units": &body.Amount,
		"currency":           &body.Currency,
	} {
		if err := json.Unmarshal(fields[name], target); err != nil {
			return body, errors.New("invalid JSON request")
		}
	}
	return body, nil
}

func debitError(c *gin.Context, logger *slog.Logger, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"
	switch {
	case errors.Is(err, store.ErrAccountNotFound):
		status, message = http.StatusNotFound, store.ErrAccountNotFound.Error()
	case errors.Is(err, store.ErrInsufficientFunds):
		status, message = http.StatusUnprocessableEntity, store.ErrInsufficientFunds.Error()
	case errors.Is(err, store.ErrConflict):
		status, message = http.StatusConflict, store.ErrConflict.Error()
	case errors.Is(err, store.ErrInvalid):
		status, message = http.StatusBadRequest, store.ErrInvalid.Error()
	default:
		logFailure(c, logger, "debit failed", err)
	}
	c.JSON(status, errorResponse{Error: message})
}

func logFailure(c *gin.Context, logger *slog.Logger, message string, err error) {
	attrs := []any{
		"error_type",
		"database_or_transport",
		"route",
		c.FullPath(),
		"request_id",
		c.Writer.Header().Get("X-Request-ID"),
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		attrs = append(attrs, "sqlstate", pgErr.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		attrs = append(attrs, "timeout", true)
	}
	logger.ErrorContext(c.Request.Context(), message, attrs...)
}

func logging(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := ""
		if id, err := parseUUID(c.GetHeader("X-Request-ID")); err == nil {
			requestID = id.String()
			c.Header("X-Request-ID", requestID)
		}
		c.Next()
		logger.InfoContext(c.Request.Context(), "http request", "request_id", requestID, "method", c.Request.Method,
			"route", c.FullPath(), "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
	}
}

func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				logger.ErrorContext(c.Request.Context(), "http handler panic", "route", c.FullPath(),
					"request_id", c.Writer.Header().Get("X-Request-ID"))
				c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{Error: "internal server error"})
			}
		}()
		c.Next()
	}
}
