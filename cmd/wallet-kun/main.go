package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wallet-kun/internal/api"
	"wallet-kun/internal/logging"
	"wallet-kun/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	closeLog, err := logging.Setup(os.Getenv("APP_ENV"), os.Getenv("LOG_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot initialize logging:", err)
		os.Exit(1)
	}
	err = run()
	if err != nil {
		slog.Error("service stopped", "reason", err.Error())
	}
	_ = closeLog()
	if err != nil {
		os.Exit(1)
	}
}

func run() error {
	gin.SetMode(gin.ReleaseMode)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(startup, databaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	st := store.New(pool)
	if err := st.Migrate(startup); err != nil {
		return errors.New("database initialization failed")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":5000"
	}
	server := &http.Server{Addr: addr, Handler: api.New(st).Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	slog.Info("service started", "address", addr)
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP listener failed")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("HTTP shutdown timed out")
		}
	}
	return nil
}
