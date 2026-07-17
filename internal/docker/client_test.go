package docker

import (
	"context"
	"errors"
	"testing"
)

type mockRunner struct {
	outputFunc func(ctx context.Context, name string, args ...string) ([]byte, error)
	runFunc    func(ctx context.Context, name string, args ...string) error
}

func (m *mockRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.outputFunc != nil {
		return m.outputFunc(ctx, name, args...)
	}
	return nil, nil
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) error {
	if m.runFunc != nil {
		return m.runFunc(ctx, name, args...)
	}
	return nil
}

func TestGetContainerStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		runner := &mockRunner{
			outputFunc: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "docker" || args[0] != "inspect" || args[3] != "my-container" {
					t.Fatalf("unexpected arguments: %v", args)
				}
				return []byte("  running\n "), nil
			},
		}
		client := &Client{runner: runner}
		status, err := client.GetContainerStatus(ctx, "my-container")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if status != "running" {
			t.Errorf("expected status 'running', got: '%s'", status)
		}
	})

	t.Run("error", func(t *testing.T) {
		expectedErr := errors.New("command failed")
		runner := &mockRunner{
			outputFunc: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return nil, expectedErr
			},
		}
		client := &Client{runner: runner}
		_, err := client.GetContainerStatus(ctx, "my-container")
		if !errors.Is(err, expectedErr) {
			t.Errorf("expected error %v, got %v", expectedErr, err)
		}
	})
}

func TestStartContainer(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		called := false
		runner := &mockRunner{
			runFunc: func(ctx context.Context, name string, args ...string) error {
				if name != "docker" || args[0] != "start" || args[1] != "my-container" {
					t.Fatalf("unexpected arguments: %v", args)
				}
				called = true
				return nil
			},
		}
		client := &Client{runner: runner}
		err := client.StartContainer(ctx, "my-container")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if !called {
			t.Error("expected runner.Run to be called")
		}
	})
}

func TestStopContainer(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		called := false
		runner := &mockRunner{
			runFunc: func(ctx context.Context, name string, args ...string) error {
				if name != "docker" || args[0] != "stop" || args[3] != "my-container" {
					t.Fatalf("unexpected arguments: %v", args)
				}
				called = true
				return nil
			},
		}
		client := &Client{runner: runner}
		err := client.StopContainer(ctx, "my-container")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if !called {
			t.Error("expected runner.Run to be called")
		}
	})
}

func TestGetContainerLogs(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		expectedLogs := []byte("server started\nplayer joined")
		runner := &mockRunner{
			outputFunc: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "docker" || args[0] != "logs" || args[2] != "50" || args[3] != "my-container" {
					t.Fatalf("unexpected arguments: %v", args)
				}
				return expectedLogs, nil
			},
		}
		client := &Client{runner: runner}
		logs, err := client.GetContainerLogs(ctx, "my-container", "50")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if string(logs) != string(expectedLogs) {
			t.Errorf("expected logs '%s', got '%s'", expectedLogs, logs)
		}
	})
}
