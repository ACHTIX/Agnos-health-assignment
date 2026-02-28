package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/time/rate"
)

func TestLiveHandler_ReturnsOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	rec := httptest.NewRecorder()

	liveHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	expectedBody := `{"status":"ok"}`
	if rec.Body.String() != expectedBody {
		t.Errorf("expected body '%s', got '%s'", expectedBody, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got '%s'", ct)
	}
}

func TestReadyHandler_NoDB(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	dbMu.Lock()
	db = nil
	dbMu.Unlock()

	readyHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got '%s'", ct)
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	dbMu.Lock()
	db = nil
	dbMu.Unlock()

	// Initialize log buffer for test
	logCtx, logCancel := context.WithCancel(context.Background())
	startLogFlusher(logCtx, 64)

	mux := http.NewServeMux()
	mux.HandleFunc(routeLive, liveHandler)
	mux.HandleFunc(routeReady, readyHandler)

	handler := metricsMiddleware(mux)

	server := &http.Server{
		Addr:              ":8888",
		Handler:           handler,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("server error: %v", err)
		}
	}()

	// Wait for server to be ready with retry loop
	waitForServer(t, "http://localhost:8888/live")

	// Trigger middleware to get coverage
	resp, err := http.Get("http://localhost:8888/live")
	if err == nil {
		_ = resp.Body.Close()
	}

	// Trigger 404 for coverage
	resp2, err := http.Get("http://localhost:8888/notfound")
	if err == nil {
		_ = resp2.Body.Close()
	}

	// Graceful shutdown - cancel flusher first, then shutdown server
	logCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Logf("shutdown error: %v", err)
	}
}

func TestInitDB_InvalidDSN(t *testing.T) {
	_, err := initDB("invalid-dsn-format")
	if err == nil {
		t.Error("expected error for invalid DSN, got nil")
	}
}

func TestReadyHandler_DBPingSuccess(t *testing.T) {
	mockDB, mock, _ := sqlmock.New(sqlmock.MonitorPingsOption(true))
	defer func() { _ = mockDB.Close() }()

	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()
	mock.ExpectPing()

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	readyHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestReadyHandler_DBPingError(t *testing.T) {
	mockDB, mock, _ := sqlmock.New(sqlmock.MonitorPingsOption(true))
	defer func() { _ = mockDB.Close() }()

	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()
	mock.ExpectPing().WillReturnError(errors.New("db down"))

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	readyHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestMetricsMiddleware_WithDB(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer func() { _ = mockDB.Close() }()

	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()

	mock.ExpectExec("INSERT INTO api_logs").WillReturnResult(sqlmock.NewResult(1, 1))

	// Set up async log buffer for the test
	logCtx, logCancel := context.WithCancel(context.Background())
	startLogFlusher(logCtx, 64)
	defer logCancel()

	handler := metricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Give the async flusher time to process
	time.Sleep(100 * time.Millisecond)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled mock: %s", err)
	}
}

func TestMetricsMiddleware_DBError(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer func() { _ = mockDB.Close() }()

	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()

	mock.ExpectExec("INSERT INTO api_logs").WillReturnError(errors.New("insert temp error"))

	logCtx, logCancel := context.WithCancel(context.Background())
	startLogFlusher(logCtx, 64)
	defer logCancel()

	handler := metricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Give the async flusher time to process
	time.Sleep(100 * time.Millisecond)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled mock: %s", err)
	}
}

type errorResponseWriter struct {
	http.ResponseWriter
}

func (e *errorResponseWriter) Write(b []byte) (int, error) {
	return 0, errors.New("write error")
}

func TestLiveHandler_WriteError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	rec := httptest.NewRecorder()
	errRec := &errorResponseWriter{ResponseWriter: rec}
	liveHandler(errRec, req)
}

func TestReadyHandler_WriteError(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer func() { _ = mockDB.Close() }()

	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()
	mock.ExpectPing()

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	errRec := &errorResponseWriter{ResponseWriter: rec}
	readyHandler(errRec, req)
}

// waitForServer polls the given URL until it gets a response or times out.
func waitForServer(t *testing.T, url string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not start in time", url)
}

func TestConnectWithRetry_InvalidDSN(t *testing.T) {
	_, err := connectWithRetry("invalid-dsn", 2, 10*time.Millisecond)
	if err == nil {
		t.Error("expected error for invalid DSN, got nil")
	}
}

func TestRateLimitMiddleware_Allows(t *testing.T) {
	limiter := rate.NewLimiter(rate.Limit(100), 100)
	handler := rateLimitMiddleware(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestPublicHandler_ReturnsOK(t *testing.T) {
	handler := publicHandler("test-env")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/time", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got '%s'", ct)
	}

	var resp PublicResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got '%s'", resp.Status)
	}
	if resp.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
	if resp.Env != "test-env" {
		t.Errorf("expected env 'test-env', got '%s'", resp.Env)
	}

	// Verify timestamp is valid RFC3339
	if _, err := time.Parse(time.RFC3339, resp.Timestamp); err != nil {
		t.Errorf("timestamp is not valid RFC3339: %s", resp.Timestamp)
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	t.Setenv("TEST_API_ENV_VAR", "custom-value")
	if v := getEnvOrDefault("TEST_API_ENV_VAR", "default"); v != "custom-value" {
		t.Errorf("expected 'custom-value', got '%s'", v)
	}
	if v := getEnvOrDefault("TEST_API_UNSET_VAR", "fallback"); v != "fallback" {
		t.Errorf("expected 'fallback', got '%s'", v)
	}
}

func TestGetRateLimit_Default(t *testing.T) {
	t.Setenv("RATE_LIMIT", "")
	rl := getRateLimit()
	if rl != 100 {
		t.Errorf("expected 100, got %d", rl)
	}
}

func TestGetRateLimit_Custom(t *testing.T) {
	t.Setenv("RATE_LIMIT", "50")
	rl := getRateLimit()
	if rl != 50 {
		t.Errorf("expected 50, got %d", rl)
	}
}

func TestGetRateLimit_Invalid(t *testing.T) {
	t.Setenv("RATE_LIMIT", "not-a-number")
	rl := getRateLimit()
	if rl != 100 {
		t.Errorf("expected default 100 for invalid input, got %d", rl)
	}
}

func TestGetRateLimit_Zero(t *testing.T) {
	t.Setenv("RATE_LIMIT", "0")
	rl := getRateLimit()
	if rl != 100 {
		t.Errorf("expected default 100 for zero input, got %d", rl)
	}
}

func TestNewHTTPServer(t *testing.T) {
	handler := http.NewServeMux()
	srv := newHTTPServer(":9999", handler)
	if srv.Addr != ":9999" {
		t.Errorf("expected addr ':9999', got '%s'", srv.Addr)
	}
	if srv.ReadTimeout != 10*time.Second {
		t.Errorf("expected ReadTimeout 10s, got %v", srv.ReadTimeout)
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("expected ReadHeaderTimeout 5s, got %v", srv.ReadHeaderTimeout)
	}
}

func TestSetupDatabase_InvalidDSN(t *testing.T) {
	_, err := setupDatabase("invalid-dsn")
	if err == nil {
		t.Error("expected error for invalid DSN, got nil")
	}
}

func TestFlushLog_NilDB(t *testing.T) {
	dbMu.Lock()
	db = nil
	dbMu.Unlock()
	// Should not panic
	flushLog(logEntry{method: "GET", endpoint: "/test", status: 200, durationMs: 1.0, remoteAddr: "127.0.0.1"})
}

func TestRoutePattern_Known(t *testing.T) {
	if r := routePattern("/live"); r != "/live" {
		t.Errorf("expected '/live', got '%s'", r)
	}
	if r := routePattern("/api/v1/time"); r != "/api/v1/time" {
		t.Errorf("expected '/api/v1/time', got '%s'", r)
	}
}

func TestRoutePattern_Unknown(t *testing.T) {
	if r := routePattern("/unknown"); r != "/other" {
		t.Errorf("expected '/other', got '%s'", r)
	}
}

func TestStatusRecorder_WriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, statusCode: http.StatusOK}
	sr.WriteHeader(http.StatusNotFound)
	if sr.statusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", sr.statusCode)
	}
}

func TestRateLimitMiddleware_Rejects(t *testing.T) {
	// Limiter with 0 rate effectively blocks all requests
	limiter := rate.NewLimiter(rate.Limit(0), 0)
	handler := rateLimitMiddleware(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rec.Code)
	}
}
