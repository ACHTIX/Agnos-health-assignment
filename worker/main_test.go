package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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

func TestWorker_IsHealthy(t *testing.T) {
	// Worker is healthy initially
	w := NewWorker(1 * time.Second)
	if !w.IsHealthy() {
		t.Errorf("expected worker to be healthy initially")
	}

	// Worker is unhealthy if last run was more than 3x the interval ago
	w.lastRunAt = time.Now().Add(-4 * time.Second)
	if w.IsHealthy() {
		t.Errorf("expected worker to be unhealthy due to staleness")
	}

	// Worker isn't stale if it hasn't run yet
	w.lastRunAt = time.Time{}
	if !w.IsHealthy() {
		t.Errorf("expected worker to be healthy if no job has started yet")
	}
}

func TestWorker_BatchSize(t *testing.T) {
	w := NewWorker(1 * time.Second)
	if w.batchSize != defaultBatchSize {
		t.Errorf("expected default batch size %d, got %d", defaultBatchSize, w.batchSize)
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	dbMu.Lock()
	db = nil
	dbMu.Unlock()

	worker := NewWorker(100 * time.Millisecond)
	healthServer := setupHealthServer(worker, "8889")

	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)

	go func() {
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("health server error: %v", err)
		}
	}()

	// Wait for server to be ready with retry loop
	waitForServer(t, "http://localhost:8889/live")

	resp, err := http.Get("http://localhost:8889/live")
	if err == nil {
		_ = resp.Body.Close()
	}

	// Graceful shutdown
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		t.Logf("shutdown error: %v", err)
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

func TestProcessLogs_Success(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer func() { _ = mockDB.Close() }()
	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()

	w := NewWorker(1 * time.Second)

	mock.ExpectExec("UPDATE api_logs").WillReturnResult(sqlmock.NewResult(0, 5))

	w.processLogs()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled mock: %s", err)
	}
}

func TestProcessLogs_Error(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer func() { _ = mockDB.Close() }()
	dbMu.Lock()
	db = mockDB
	dbMu.Unlock()

	w := NewWorker(1 * time.Second)

	mock.ExpectExec("UPDATE api_logs").WillReturnError(errors.New("db update failed"))

	w.processLogs()

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

func TestGetWorkerInterval_Default(t *testing.T) {
	t.Setenv("WORKER_INTERVAL", "")
	interval := getWorkerInterval()
	if interval != 2*time.Second {
		t.Errorf("expected 2s default, got %v", interval)
	}
}

func TestGetWorkerInterval_Custom(t *testing.T) {
	t.Setenv("WORKER_INTERVAL", "5s")
	interval := getWorkerInterval()
	if interval != 5*time.Second {
		t.Errorf("expected 5s, got %v", interval)
	}
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

func TestWorker_LastRunAt(t *testing.T) {
	w := NewWorker(1 * time.Second)
	if !w.LastRunAt().IsZero() {
		t.Error("expected zero LastRunAt for new worker")
	}
	now := time.Now()
	w.lastRunAt = now
	if !w.LastRunAt().Equal(now) {
		t.Error("expected LastRunAt to match set time")
	}
}

func TestConnectWithRetry_InvalidDSN(t *testing.T) {
	_, err := connectWithRetry("invalid-dsn", 2, 10*time.Millisecond)
	if err == nil {
		t.Error("expected error for invalid DSN, got nil")
	}
}
