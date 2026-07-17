package docker

import (
	"context"
	"os/exec"
	"strings"
)

// DockerClient defines the interface for interacting with Docker.
type DockerClient interface {
	GetContainerStatus(ctx context.Context, name string) (string, error)
	StartContainer(ctx context.Context, name string) error
	StopContainer(ctx context.Context, name string) error
	GetContainerLogs(ctx context.Context, name string, limit string) ([]byte, error)
}

// commandRunner abstract shell command execution for testing.
type commandRunner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, name string, args ...string) error
}

type realRunner struct{}

func (r *realRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (r *realRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// Client wraps the Docker CLI.
type Client struct {
	runner commandRunner
}

// NewClient creates a new Docker client instance using the real runner.
func NewClient() (*Client, error) {
	return &Client{runner: &realRunner{}}, nil
}

// GetContainerStatus returns the status of the container using docker inspect.
func (c *Client) GetContainerStatus(ctx context.Context, name string) (string, error) {
	out, err := c.runner.Output(ctx, "docker", "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// StartContainer starts the target container using docker start.
func (c *Client) StartContainer(ctx context.Context, name string) error {
	return c.runner.Run(ctx, "docker", "start", name)
}

// StopContainer stops the target container using docker stop.
func (c *Client) StopContainer(ctx context.Context, name string) error {
	return c.runner.Run(ctx, "docker", "stop", "-t", "15", name)
}

// GetContainerLogs retrieves the last lines of container logs.
func (c *Client) GetContainerLogs(ctx context.Context, name string, limit string) ([]byte, error) {
	// For logs, CombinedOutput is cleaner since we want stdout and stderr.
	// Since we abstract this via Output for simplicity of the interface, we call Output.
	return c.runner.Output(ctx, "docker", "logs", "--tail", limit, name)
}
