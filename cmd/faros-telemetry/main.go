// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/faroshq/faros/pkg/telemetryreceiver"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultListenAddr         = ":8090"
	defaultRawRetention       = 2160 * time.Hour
	defaultAggregateRetention = 9360 * time.Hour
	defaultJanitorInterval    = time.Hour
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("faros telemetry receiver stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	dsn := os.Getenv("TELEMETRY_DATABASE_URL")
	ingestToken := os.Getenv("TELEMETRY_INGEST_TOKEN")
	adminToken := os.Getenv("TELEMETRY_ADMIN_TOKEN")
	if dsn == "" || ingestToken == "" || adminToken == "" {
		return errors.New("TELEMETRY_DATABASE_URL, TELEMETRY_INGEST_TOKEN, and TELEMETRY_ADMIN_TOKEN are required")
	}
	listenAddr := envString("TELEMETRY_LISTEN_ADDR", defaultListenAddr)
	maxBatch, err := envInt("TELEMETRY_MAX_BATCH_EVENTS", 1000)
	if err != nil {
		return err
	}
	maxEventBytes, err := envInt("TELEMETRY_MAX_EVENT_BYTES", 256*1024)
	if err != nil {
		return err
	}
	rawRetention, err := envDuration("TELEMETRY_RAW_RETENTION", defaultRawRetention)
	if err != nil {
		return err
	}
	aggregateRetention, err := envDuration("TELEMETRY_AGGREGATE_RETENTION", defaultAggregateRetention)
	if err != nil {
		return err
	}
	janitorInterval, err := envDuration("TELEMETRY_JANITOR_INTERVAL", defaultJanitorInterval)
	if err != nil {
		return err
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := telemetryreceiver.RunMigrations(ctx, sqlDB); err != nil {
		cancel()
		return err
	}
	cancel()

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse telemetry database URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return fmt.Errorf("open telemetry pool: %w", err)
	}
	defer pool.Close()
	store := telemetryreceiver.NewPostgresStore(pool)
	server, err := telemetryreceiver.NewServer(store, telemetryreceiver.Config{
		IngestToken:    ingestToken,
		AdminToken:     adminToken,
		MaxBatchEvents: maxBatch,
		MaxEventBytes:  maxEventBytes,
		Logger:         logger,
	})
	if err != nil {
		return err
	}
	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()
	go func() {
		if err := server.RunJanitor(janitorCtx, janitorInterval, rawRetention, aggregateRetention); err != nil {
			logger.Error("telemetry janitor stopped", "error", err)
		}
	}()

	httpServer := &http.Server{Addr: listenAddr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpServer.ListenAndServe() }()
	signalCtx, stopSignal := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignal()
	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalCtx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := envString(name, strconv.Itoa(fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := envString(name, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}
