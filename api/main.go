package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

var (
	db   *sql.DB
	dbMu sync.RWMutex
)

// logEntry holds the data for an async DB log insert.
type logEntry struct {
	method     string
	endpoint   string
	status     int
	durationMs float64
	remoteAddr string
}

// logBuffer is the channel used for async DB logging.
var logBuffer chan logEntry

// startLogFlusher starts a background goroutine that drains logBuffer
// and inserts rows into the database. It stops when ctx is cancelled
// and drains any remaining entries before returning.
func startLogFlusher(ctx context.Context, bufSize int) {
	ch := make(chan logEntry, bufSize)
	logBuffer = ch
	go func() {
		for {
			select {
			case entry := <-ch:
				flushLog(entry)
			case <-ctx.Done():
				// Drain remaining entries
				for {
					select {
					case entry := <-ch:
						flushLog(entry)
					default:
						return
					}
				}
			}
		}
	}()
}

func flushLog(entry logEntry) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	_, err := d.Exec(`
		INSERT INTO api_logs (method, endpoint, status, duration_ms, remote_addr)
		VALUES ($1, $2, $3, $4, $5)
	`, entry.method, entry.endpoint, entry.status, entry.durationMs, entry.remoteAddr)
	if err != nil {
		slog.Error("failed to log request to db", "error", err)
	}
}

func initDB(dsn string) (*sql.DB, error) {
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	_, err = d.Exec(`
		CREATE TABLE IF NOT EXISTS api_logs (
			id SERIAL PRIMARY KEY,
			method VARCHAR(10),
			endpoint VARCHAR(255),
			status INT,
			duration_ms FLOAT,
			remote_addr VARCHAR(255),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			processed_at TIMESTAMP NULL
		)
	`)
	return d, err
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

// Prometheus metrics
var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "endpoint", "status"},
	)
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	httpErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_errors_total",
			Help: "Total number of HTTP errors (4xx and 5xx)",
		},
		[]string{"method", "endpoint", "status"},
	)
	httpRateLimitedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "http_rate_limited_total",
			Help: "Total number of rate-limited requests",
		},
	)
)

func init() {
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(httpRequestDuration)
	prometheus.MustRegister(httpErrorsTotal)
	prometheus.MustRegister(httpRateLimitedTotal)
}

// rateLimitMiddleware returns HTTP 429 when the rate limit is exceeded.
func rateLimitMiddleware(limiter *rate.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				httpRateLimitedTotal.Inc()
				w.Header().Set(headerContentType, contentTypeJSON)
				w.WriteHeader(http.StatusTooManyRequests)
				if _, err := w.Write([]byte(`{"status":"error","message":"rate limit exceeded"}`)); err != nil {
					slog.Error(errWriteResponse, "error", err)
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// HealthResponse represents the JSON response for the health endpoint.
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
	Service   string `json:"service"`
	Version   string `json:"version"`
}

// HTTP header and content type constants to avoid duplicated string literals.
const (
	headerContentType   = "Content-Type"
	contentTypeJSON     = "application/json"
	errWriteResponse    = "failed to write response"
)

// Route path constants to avoid duplicated string literals.
const (
	routeLive    = "/live"
	routeReady   = "/ready"
	routeMetrics = "/metrics"
	routePublic  = "/api/v1/time"
)

// knownRoutes maps registered paths to their route pattern to prevent
// unbounded cardinality in Prometheus labels.
var knownRoutes = map[string]string{
	routeLive:    routeLive,
	routeReady:   routeReady,
	routeMetrics: routeMetrics,
	routePublic:  routePublic,
}

func routePattern(path string) string {
	if route, ok := knownRoutes[path]; ok {
		return route
	}
	return "/other"
}

// metricsMiddleware records request metrics for Prometheus.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()
		status := http.StatusText(rec.statusCode)
		route := routePattern(r.URL.Path)

		httpRequestsTotal.WithLabelValues(r.Method, route, status).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route).Observe(duration)

		if rec.statusCode >= 400 {
			httpErrorsTotal.WithLabelValues(r.Method, route, status).Inc()
		}

		if logBuffer != nil {
			select {
			case logBuffer <- logEntry{
				method:     r.Method,
				endpoint:   r.URL.Path,
				status:     rec.statusCode,
				durationMs: duration * 1000,
				remoteAddr: r.RemoteAddr,
			}:
			default:
				slog.Warn("log buffer full, dropping log entry")
			}
		}

		slog.Info("request completed", // #nosec G706 -- slog JSON handler safely encodes values
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.statusCode,
			"duration_ms", duration*1000,
			"remote_addr", r.RemoteAddr,
		)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// Live response format
func liveHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		slog.Error(errWriteResponse, "error", err)
	}
}

// Ready response evaluates Postgres DB
func readyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, contentTypeJSON)
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

// PublicResponse represents the JSON response for the public time endpoint.
type PublicResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
	Env       string `json:"env"`
}

func publicHandler(env string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentType, contentTypeJSON)
		w.WriteHeader(http.StatusOK)
		resp := PublicResponse{
			Status:    "ok",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Env:       env,
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Error(errWriteResponse, "error", err)
		}
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getRateLimit() int {
	rateLimit := 100
	if rlStr := os.Getenv("RATE_LIMIT"); rlStr != "" {
		if rl, err := strconv.Atoi(rlStr); err == nil && rl > 0 {
			rateLimit = rl
		}
	}
	return rateLimit
}

func setupDatabase(dsn string) (*sql.DB, error) {
	d, err := connectWithRetry(dsn, 5, 1*time.Second)
	if err != nil {
		return nil, err
	}
	dbMu.Lock()
	db = d
	dbMu.Unlock()
	slog.Info("connected to postgres successfully")
	return d, nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
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

	port := getEnvOrDefault("PORT", "8080")
	publicPort := getEnvOrDefault("PUBLIC_PORT", "8090")
	env := getEnvOrDefault("APP_ENV", "development")

	if dsn := os.Getenv("DB_DSN"); dsn != "" {
		d, err := setupDatabase(dsn)
		if err != nil {
			slog.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		defer func() {
			if err := d.Close(); err != nil {
				slog.Error("error closing db", "error", err)
			}
		}()
	} else {
		slog.Warn("DB_DSN not set, running without database logging")
	}

	logCtx, logCancel := context.WithCancel(context.Background())
	defer logCancel()
	startLogFlusher(logCtx, 1024)

	limiter := rate.NewLimiter(rate.Limit(getRateLimit()), getRateLimit())

	mux := http.NewServeMux()
	mux.HandleFunc(routeLive, liveHandler)
	mux.HandleFunc(routeReady, readyHandler)
	mux.Handle(routeMetrics, promhttp.Handler())

	server := newHTTPServer(":"+port, rateLimitMiddleware(limiter)(metricsMiddleware(mux)))

	publicMux := http.NewServeMux()
	publicMux.HandleFunc(routePublic, publicHandler(env))
	publicServer := newHTTPServer(":"+publicPort, publicMux)

	go func() {
		slog.Info("internal api server starting", "port", port, "env", env)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("internal server failed to start", "error", err)
			os.Exit(1)
		}
	}()

	go func() {
		slog.Info("public api server starting", "port", publicPort, "env", env)
		if err := publicServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("public server failed to start", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	slog.Info("shutdown signal received", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("internal server forced to shutdown", "error", err)
	}
	if err := publicServer.Shutdown(ctx); err != nil {
		slog.Error("public server forced to shutdown", "error", err)
	}

	logCancel()
	slog.Info("servers stopped gracefully")
}
