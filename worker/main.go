package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	db   *sql.DB
	dbMu sync.RWMutex
)

// Prometheus metrics
var (
	workerLogsProcessed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "worker_logs_processed_total",
			Help: "Total number of log entries processed by the worker",
		},
	)
	workerProcessingDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_processing_duration_seconds",
			Help:    "Duration of each worker batch processing cycle",
			Buckets: prometheus.DefBuckets,
		},
	)
	workerBatchErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "worker_batch_errors_total",
			Help: "Total number of batch processing errors",
		},
	)
)

func init() {
	prometheus.MustRegister(workerLogsProcessed)
	prometheus.MustRegister(workerProcessingDuration)
	prometheus.MustRegister(workerBatchErrors)
}

const (
	defaultBatchSize = 1000
	errWriteResponse = "failed to write response"
)

func initDB(dsn string) (*sql.DB, error) {
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// Verify connection
	if err := d.Ping(); err != nil {
		return nil, err
	}
	return d, nil
}

// connectWithRetry attempts to connect to the database with exponential backoff.
func connectWithRetry(dsn string, maxRetries int, baseDelay time.Duration) (*sql.DB, error) {
	var d *sql.DB
	var err error
	for i := 0; i < maxRetries; i++ {
		d, err = initDB(dsn)
		if err == nil {
			// Configure connection pool
			d.SetMaxOpenConns(25)
			d.SetMaxIdleConns(5)
			d.SetConnMaxLifetime(5 * time.Minute)
			return d, nil
		}
		delay := baseDelay * (1 << uint(i))
		slog.Warn("db connection failed, retrying", "attempt", i+1, "max", maxRetries, "delay", delay, "error", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("failed to connect after %d retries: %w", maxRetries, err)
}

// Worker manages the background job for processing api_logs
type Worker struct {
	interval  time.Duration
	batchSize int
	lastRunAt time.Time
	isHealthy bool
}

// NewWorker creates a new Worker.
func NewWorker(interval time.Duration) *Worker {
	return &Worker{
		interval:  interval,
		batchSize: defaultBatchSize,
		isHealthy: true,
	}
}

// Run starts the worker loop for near-real-time updates.
func (w *Worker) Run(ctx context.Context) {
	slog.Info("worker started", "interval", w.interval.String())

	for {
		select {
		case <-ctx.Done():
			slog.Info("worker stopping", "reason", "context cancelled")
			return
		default:
			processed := w.processLogs()
			w.lastRunAt = time.Now()

			if processed == 0 {
				// Sleep if there are no logs to process
				time.Sleep(w.interval)
			} else {
				// Yield but continue processing quickly if we have an active queue
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

func (w *Worker) processLogs() int {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		w.isHealthy = false
		slog.Warn("db not connected")
		return 0
	}

	start := time.Now()
	res, err := d.Exec(`
		UPDATE api_logs
		SET processed_at = CURRENT_TIMESTAMP
		WHERE id IN (
			SELECT id FROM api_logs
			WHERE processed_at IS NULL
			ORDER BY id
			LIMIT $1
		)
	`, w.batchSize)
	duration := time.Since(start).Seconds()
	workerProcessingDuration.Observe(duration)

	if err != nil {
		w.isHealthy = false
		workerBatchErrors.Inc()
		slog.Error("failed to process logs", "error", err)
		return 0
	}

	w.isHealthy = true
	rows, _ := res.RowsAffected()
	if rows > 0 {
		workerLogsProcessed.Add(float64(rows))
		slog.Info("processed api logs", "count", rows)
	}
	return int(rows)
}

func (w *Worker) LastRunAt() time.Time {
	return w.lastRunAt
}

func (w *Worker) IsHealthy() bool {
	// Unhealthy if last run was more than 3x the interval ago
	if !w.lastRunAt.IsZero() && time.Since(w.lastRunAt) > 3*w.interval {
		return false
	}
	return w.isHealthy
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getWorkerInterval() time.Duration {
	intervalStr := getEnvOrDefault("WORKER_INTERVAL", "")
	interval := 2 * time.Second // default: 2 seconds for near real-time
	if intervalStr != "" {
		if parsed, err := time.ParseDuration(intervalStr); err == nil {
			interval = parsed
		}
	}
	return interval
}

func liveHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		slog.Error(errWriteResponse, "error", err)
	}
}

func readyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte(`{"status":"error","message":"db not configured"}`)); err != nil {
			slog.Error(errWriteResponse, "error", err)
		}
		return
	}
	if err := d.Ping(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte(`{"status":"error","message":"db unreachable"}`)); err != nil {
			slog.Error(errWriteResponse, "error", err)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"status":"ready"}`)); err != nil {
		slog.Error(errWriteResponse, "error", err)
	}
}

func setupHealthServer(worker *Worker, healthPort string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/live", liveHandler)
	mux.HandleFunc("/ready", readyHandler)
	mux.Handle("/metrics", promhttp.Handler())

	return &http.Server{
		Addr:              ":" + healthPort,
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	env := getEnvOrDefault("APP_ENV", "development")
	interval := getWorkerInterval()
	healthPort := getEnvOrDefault("HEALTH_PORT", "8081")

	slog.Info("worker initializing",
		"env", env,
		"interval", interval.String(),
		"health_port", healthPort,
	)

	dsn := os.Getenv("DB_DSN")
	if dsn != "" {
		d, err := connectWithRetry(dsn, 5, 1*time.Second)
		if err != nil {
			slog.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		dbMu.Lock()
		db = d
		dbMu.Unlock()
		defer func() {
			if err := d.Close(); err != nil {
				slog.Error("error closing db", "error", err)
			}
		}()
		slog.Info("connected to postgres successfully")
	} else {
		slog.Warn("DB_DSN not set, running without database connection")
	}

	worker := NewWorker(interval)
	healthServer := setupHealthServer(worker, healthPort)

	go func() {
		slog.Info("health server starting", "port", healthPort)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("health server failed", "error", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	slog.Info("shutdown signal received", "signal", sig.String())

	cancel() // Stop worker

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("health server shutdown error", "error", err)
	}

	slog.Info("worker stopped gracefully")
}
