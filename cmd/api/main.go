package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"queuedesk/internal/booking"
	"queuedesk/internal/httpapi"
	"queuedesk/internal/postgres"
	"queuedesk/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("приложение остановлено", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("DATABASE_URL")
	token := os.Getenv("ADMIN_TOKEN")
	if dsn == "" || len(token) < 16 {
		return fmt.Errorf("нужны DATABASE_URL и ADMIN_TOKEN длиной не меньше 16 символов")
	}
	ttl, err := time.ParseDuration(env("BOOKING_TTL", "10m"))
	if err != nil || ttl <= 0 {
		return fmt.Errorf("BOOKING_TTL должна быть положительной длительностью, например 10m")
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(startup, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(startup); err != nil {
		return err
	}
	store := &postgres.Store{Pool: pool}
	if err = store.Migrate(startup); err != nil {
		return err
	}
	rdb := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "localhost:6379"), DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxRetries: 1})
	defer rdb.Close()
	if err = rdb.Ping(startup).Err(); err != nil {
		return fmt.Errorf("подключение к Redis: %w", err)
	}
	a := &httpapi.API{Service: booking.NewService(store, ttl), Redis: rdb, AdminToken: token, Ready: func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return err
		}
		return rdb.Ping(ctx).Err()
	}}
	server := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: a.Router(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); worker.Run(workerCtx, store, 5*time.Second) }()
	errorsCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP-сервер запущен", "address", server.Addr)
		errorsCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err = <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	stopWorker()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		server.Close()
	}
	wg.Wait()
	return errors.Join(err, shutdownErr)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
