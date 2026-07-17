package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
	return "exited", nil
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
	return nil, nil
}

func TestForceStart(t *testing.T) {
	t.Run("start success from stopped", func(t *testing.T) {
		called := int32(0)
		dockerMock := &mockDockerClient{
			startFunc: func(ctx context.Context, name string) error {
				atomic.StoreInt32(&called, 1)
				return nil
			},
		}

		m := NewMonitor(dockerMock, "palworld", "http://localhost:8212", "password", 1*time.Minute, 1*time.Second)
		err := m.ForceStart()
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}

		// Yield scheduler to let the goroutine complete if any, though ForceStart blocks on StartContainer
		time.Sleep(50 * time.Millisecond)

		if atomic.LoadInt32(&called) != 1 {
			t.Error("expected docker start to be called")
		}
		state, _, _ := m.GetStatus()
		if state != StateStarting {
			t.Errorf("expected state 'starting', got '%s'", state)
		}
	})

	t.Run("start fails when already running", func(t *testing.T) {
		dockerMock := &mockDockerClient{}
		m := NewMonitor(dockerMock, "palworld", "http://localhost:8212", "password", 1*time.Minute, 1*time.Second)
		m.state = StateRunning

		err := m.ForceStart()
		if err == nil {
			t.Error("expected error when starting non-stopped server, got nil")
		}
	})
}

func TestSaveGame(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		serverCalled := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/v1/api/save" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			username, password, ok := r.BasicAuth()
			if !ok || username != "admin" || password != "mypassword" {
				t.Errorf("unexpected basic auth: %s %s", username, password)
			}
			serverCalled = true
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		dockerMock := &mockDockerClient{}
		m := NewMonitor(dockerMock, "palworld", server.URL, "mypassword", 1*time.Minute, 1*time.Second)
		err := m.SaveGame()
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if !serverCalled {
			t.Error("expected HTTP server to be called")
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		dockerMock := &mockDockerClient{}
		m := NewMonitor(dockerMock, "palworld", server.URL, "mypassword", 1*time.Minute, 1*time.Second)
		err := m.SaveGame()
		if err == nil {
			t.Error("expected save error, got nil")
		}
	})
}

func TestTickStates(t *testing.T) {
	t.Run("docker stopped -> state stopped", func(t *testing.T) {
		dockerMock := &mockDockerClient{
			statusFunc: func(ctx context.Context, name string) (string, error) {
				return "exited", nil
			},
		}
		m := NewMonitor(dockerMock, "palworld", "http://localhost:8212", "pass", 5*time.Minute, 1*time.Second)
		m.state = StateRunning

		m.tick()

		state, players, _ := m.GetStatus()
		if state != StateStopped {
			t.Errorf("expected state 'stopped', got '%s'", state)
		}
		if players != 0 {
			t.Errorf("expected 0 players, got %d", players)
		}
	})

	t.Run("docker running, REST API down -> state starting", func(t *testing.T) {
		dockerMock := &mockDockerClient{
			statusFunc: func(ctx context.Context, name string) (string, error) {
				return "running", nil
			},
		}
		// REST API will fail because server doesn't exist
		m := NewMonitor(dockerMock, "palworld", "http://invalid-localhost:9999", "pass", 5*time.Minute, 1*time.Second)
		m.state = StateStopped

		m.tick()

		state, _, _ := m.GetStatus()
		if state != StateStarting {
			t.Errorf("expected state 'starting', got '%s'", state)
		}
	})

	t.Run("docker running, REST API active, players > 0 -> state running, reset countdown", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"currentplayernum": 3}`))
		}))
		defer server.Close()

		dockerMock := &mockDockerClient{
			statusFunc: func(ctx context.Context, name string) (string, error) {
				return "running", nil
			},
		}

		m := NewMonitor(dockerMock, "palworld", server.URL, "pass", 5*time.Minute, 1*time.Second)
		m.idleStartTime = time.Now().Add(-10 * time.Minute) // set dummy idle start

		m.tick()

		state, players, remaining := m.GetStatus()
		if state != StateRunning {
			t.Errorf("expected state 'running', got '%s'", state)
		}
		if players != 3 {
			t.Errorf("expected 3 players, got %d", players)
		}
		if remaining != 5*time.Minute {
			t.Errorf("expected countdown reset to 5 minutes, got %v", remaining)
		}
	})

	t.Run("docker running, REST API active, 0 players -> triggers countdown then shutdown", func(t *testing.T) {
		// Mock REST API returning 0 players
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/api/metrics" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"currentplayernum": 0}`))
			} else if r.URL.Path == "/v1/api/shutdown" {
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer server.Close()

		stopCalled := int32(0)
		dockerMock := &mockDockerClient{
			statusFunc: func(ctx context.Context, name string) (string, error) {
				if atomic.LoadInt32(&stopCalled) == 1 {
					return "exited", nil
				}
				return "running", nil
			},
			stopFunc: func(ctx context.Context, name string) error {
				atomic.StoreInt32(&stopCalled, 1)
				return nil
			},
		}

		// Very short timeout for testing (10ms)
		m := NewMonitor(dockerMock, "palworld", server.URL, "pass", 10*time.Millisecond, 1*time.Second)
		m.sleepFn = func(d time.Duration) {}

		// First tick: detects 0 players, starts countdown
		m.tick()

		state, players, _ := m.GetStatus()
		if state != StateRunning {
			t.Errorf("expected state 'running', got '%s'", state)
		}
		if players != 0 {
			t.Errorf("expected 0 players, got %d", players)
		}
		if m.idleStartTime.IsZero() {
			t.Error("expected idle start time to be set")
		}

		// Wait for timeout
		time.Sleep(20 * time.Millisecond)

		// Second tick: timeout reached, triggers shutdown
		m.tick()

		// Yield scheduler to let shutdown sequence complete
		time.Sleep(100 * time.Millisecond)

		m.mu.Lock()
		currentState := m.state
		m.mu.Unlock()

		if currentState != StateStopped {
			t.Errorf("expected state 'stopped' after shutdown, got '%s'", currentState)
		}
		if atomic.LoadInt32(&stopCalled) != 1 {
			t.Error("expected Docker StopContainer to be called")
		}
	})
}
