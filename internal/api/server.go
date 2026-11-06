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

func New(st walletStore) *Server {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(logging(), recovery())
	r.GET("/healthz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			logFailure(c, "database health check failed", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.POST("/accounts/:id/debits", func(c *gin.Context) { debit(c, st) })
	return &Server{router: r}
}

func (s *Server) Handler() http.Handler { return s.router }

func parseUUID(value string) (uuid.UUID, error) {
	if len(value) != 36 {
		return uuid.Nil, errors.New("invalid UUID")
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("invalid UUID")
	}
	return id, nil
}

func debit(c *gin.Context, st walletStore) {
	invalid := func(message string) { c.JSON(http.StatusBadRequest, gin.H{"error": message}) }
	account, err := parseUUID(c.Param("id"))
	if err != nil {
		invalid("account ID must be a nonzero UUID")
		return
	}
	if _, err := parseUUID(c.GetHeader("X-Request-ID")); err != nil {
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
	var body struct {
		OrderReference string `json:"order_reference"`
		Amount         int64  `json:"amount_minor_units"`
		Currency       string `json:"currency"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		invalid("invalid JSON request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		invalid("request must contain one JSON object")
		return
	}
	order, err := parseUUID(body.OrderReference)
	if err != nil || body.Amount <= 0 || body.Currency != "PHP" {
		invalid("order_reference UUID, positive int64 amount_minor_units, and currency PHP are required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := st.Debit(ctx, store.DebitRequest{AccountID: account, Key: key, OrderReference: order, Amount: body.Amount, Currency: body.Currency})
	if err != nil {
		status := http.StatusInternalServerError
		message := "internal server error"
		switch {
		case errors.Is(err, store.ErrAccountNotFound):
			status, message = http.StatusNotFound, err.Error()
		case errors.Is(err, store.ErrInsufficientFunds):
			status, message = http.StatusUnprocessableEntity, err.Error()
		case errors.Is(err, store.ErrConflict):
			status, message = http.StatusConflict, err.Error()
		case errors.Is(err, store.ErrInvalid):
			status, message = http.StatusBadRequest, err.Error()
		default:
			logFailure(c, "debit failed", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, result)
}

func logFailure(c *gin.Context, message string, err error) {
	attrs := []any{"error_type", "database_or_transport", "route", c.FullPath(), "request_id", c.Writer.Header().Get("X-Request-ID")}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		attrs = append(attrs, "sqlstate", pgErr.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		attrs = append(attrs, "timeout", true)
	}
	slog.Error(message, attrs...)
}

func logging() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := ""
		if id, err := parseUUID(c.GetHeader("X-Request-ID")); err == nil {
			requestID = id.String()
			c.Header("X-Request-ID", requestID)
		}
		c.Next()
		slog.Info("http request", "request_id", requestID, "method", c.Request.Method,
			"route", c.FullPath(), "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
	}
}

func recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				slog.Error("http handler panic", "route", c.FullPath())
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			}
		}()
		c.Next()
	}
}
