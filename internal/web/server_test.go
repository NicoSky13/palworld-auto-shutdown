package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nico/palworld-auto-shutdown/internal/monitor"
)

type mockDockerClient struct {
	statusFunc func(ctx context.Context, name string) (string, error)
	startFunc  func(ctx context.Context, name string) error
	stopFunc   func(ctx context.Context, name string) error
	logsFunc   func(ctx context.Context, name string, limit string) ([]byte, error)
}

func (m *mockDockerClient) GetContainerStatus(ctx context.Context, name string) (string, error) {
	if m.statusFunc != nil {
		return m.statusFunc(ctx, name)
	}
	return "running", nil
}

func (m *mockDockerClient) StartContainer(ctx context.Context, name string) error {
	if m.startFunc != nil {
		return m.startFunc(ctx, name)
	}
	return nil
}

func (m *mockDockerClient) StopContainer(ctx context.Context, name string) error {
	if m.stopFunc != nil {
		return m.stopFunc(ctx, name)
	}
	return nil
}

func (m *mockDockerClient) GetContainerLogs(ctx context.Context, name string, limit string) ([]byte, error) {
	if m.logsFunc != nil {
		return m.logsFunc(ctx, name, limit)
	}
	return []byte("test-logs"), nil
}

func TestServerAuth(t *testing.T) {
	dockerMock := &mockDockerClient{}
	m := monitor.NewMonitor(dockerMock, "palworld", "http://localhost:8212", "pass", 10*time.Minute, 1*time.Second)

	srv, err := NewServer(8213, "admin", "securepass", m, dockerMock, "palworld")
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	t.Run("unauthorized no auth header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/status", nil)
		w := httptest.NewRecorder()

		srv.basicAuth(srv.handleStatus)(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("unauthorized invalid credentials", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.SetBasicAuth("admin", "wrongpass")
		w := httptest.NewRecorder()

		srv.basicAuth(srv.handleStatus)(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("authorized success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.SetBasicAuth("admin", "securepass")
		w := httptest.NewRecorder()

		srv.basicAuth(srv.handleStatus)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestServerRoutes(t *testing.T) {
	dockerMock := &mockDockerClient{}
	m := monitor.NewMonitor(dockerMock, "palworld", "http://localhost:8212", "pass", 10*time.Minute, 1*time.Second)

	// Set mock logs
	dockerMock.logsFunc = func(ctx context.Context, name string, limit string) ([]byte, error) {
		return []byte("line 1\nline 2"), nil
	}

	srv, err := NewServer(8213, "", "", m, dockerMock, "palworld") // No auth for simpler route testing
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	t.Run("GET /api/status", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/status", nil)
		w := httptest.NewRecorder()

		srv.handleStatus(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		if resp["state"] != "stopped" { // default state is stopped
			t.Errorf("expected state 'stopped', got '%v'", resp["state"])
		}
	})

	t.Run("POST /api/control start", func(t *testing.T) {
		body := `{"action": "start"}`
		req := httptest.NewRequest("POST", "/api/control", strings.NewReader(body))
		w := httptest.NewRecorder()

		srv.handleControl(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
		}

		var resp map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["status"] != "success" {
			t.Errorf("expected status 'success', got '%s'", resp["status"])
		}
	})

	t.Run("GET /api/logs", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/logs?limit=50", nil)
		w := httptest.NewRecorder()

		srv.handleLogs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		logOutput := w.Body.String()
		if !strings.Contains(logOutput, "line 1") || !strings.Contains(logOutput, "line 2") {
			t.Errorf("expected logs to contain 'line 1' and 'line 2', got '%s'", logOutput)
		}
	})

	t.Run("GET /api/logs error", func(t *testing.T) {
		dockerMock.logsFunc = func(ctx context.Context, name string, limit string) ([]byte, error) {
			return nil, fmt.Errorf("read error")
		}

		req := httptest.NewRequest("GET", "/api/logs", nil)
		w := httptest.NewRecorder()

		srv.handleLogs(w, req)

		if w.Code != http.StatusOK { // handleLogs returns 200 with an error description inside
			t.Fatalf("expected 200, got %d", w.Code)
		}

		logOutput := w.Body.String()
		if !strings.Contains(logOutput, "Could not retrieve logs") {
			t.Errorf("expected log description error message, got '%s'", logOutput)
		}
	})
}
