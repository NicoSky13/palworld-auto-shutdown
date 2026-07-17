package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/nico/palworld-auto-shutdown/internal/docker"
)

type ServerState string

const (
	StateStopped  ServerState = "stopped"
	StateStarting ServerState = "starting"
	StateRunning  ServerState = "running"
	StateStopping ServerState = "stopping"
)

type MetricsResponse struct {
	CurrentPlayerNum int `json:"currentplayernum"`
}

type Monitor struct {
	dockerClient  docker.DockerClient
	containerName string
	apiUrl        string
	apiPassword   string
	idleTimeout   time.Duration
	checkInterval time.Duration

	state         ServerState
	playerCount   int
	lastActive    time.Time
	idleStartTime time.Time
	mu            sync.RWMutex

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	sleepFn func(time.Duration)
}

func NewMonitor(
	dockerClient docker.DockerClient,
	containerName string,
	apiUrl string,
	apiPassword string,
	idleTimeout time.Duration,
	checkInterval time.Duration,
) *Monitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Monitor{
		dockerClient:  dockerClient,
		containerName: containerName,
		apiUrl:        apiUrl,
		apiPassword:   apiPassword,
		idleTimeout:   idleTimeout,
		checkInterval: checkInterval,
		state:         StateStopped,
		ctx:           ctx,
		cancel:        cancel,
		sleepFn:       time.Sleep,
	}
}

func (m *Monitor) Start() {
	m.wg.Add(1)
	go m.loop()
}

func (m *Monitor) Stop() {
	m.cancel()
	m.wg.Wait()
}

func (m *Monitor) GetStatus() (ServerState, int, time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var idleRemaining time.Duration
	if m.state == StateRunning && !m.idleStartTime.IsZero() {
		elapsed := time.Since(m.idleStartTime)
		if elapsed < m.idleTimeout {
			idleRemaining = m.idleTimeout - elapsed
		}
	} else if m.state == StateRunning && m.playerCount > 0 {
		idleRemaining = m.idleTimeout
	}
	return m.state, m.playerCount, idleRemaining
}

// TriggerWakeUp starts the server container if it is stopped.
func (m *Monitor) TriggerWakeUp() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateStopped {
		log.Printf("[Monitor] Traffic detected. Waking up Palworld server (%s)...", m.containerName)
		m.state = StateStarting
		m.idleStartTime = time.Time{}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.dockerClient.StartContainer(ctx, m.containerName); err != nil {
				log.Printf("[Monitor] Error starting container: %v", err)
				m.mu.Lock()
				m.state = StateStopped
				m.mu.Unlock()
			}
		}()
	}
}

// ForceStart manually starts the container.
func (m *Monitor) ForceStart() error {
	m.mu.Lock()
	if m.state != StateStopped {
		m.mu.Unlock()
		return fmt.Errorf("server is not stopped (current state: %s)", m.state)
	}
	m.state = StateStarting
	m.idleStartTime = time.Time{}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.dockerClient.StartContainer(ctx, m.containerName); err != nil {
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return err
	}
	return nil
}

// ForceStop manually stops the container after saving.
func (m *Monitor) ForceStop() error {
	m.mu.Lock()
	if m.state == StateStopped || m.state == StateStopping {
		m.mu.Unlock()
		return fmt.Errorf("server is already stopped or stopping")
	}
	m.state = StateStopping
	m.mu.Unlock()

	go m.shutdownSequence()
	return nil
}

// SaveGame triggers the REST API save endpoint.
func (m *Monitor) SaveGame() error {
	url := fmt.Sprintf("%s/v1/api/save", m.apiUrl)
	req, err := http.NewRequestWithContext(m.ctx, "POST", url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("admin", m.apiPassword)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("save API returned status: %s", resp.Status)
	}
	log.Printf("[Monitor] Save API triggered successfully")
	return nil
}

func (m *Monitor) loop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

func (m *Monitor) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dockerState, err := m.dockerClient.GetContainerStatus(ctx, m.containerName)
	if err != nil {
		log.Printf("[Monitor] Error getting container status: %v", err)
		m.mu.Lock()
		m.state = StateStopped
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Sync state with Docker status
	if dockerState != "running" {
		if m.state != StateStarting && m.state != StateStopping {
			m.state = StateStopped
		}
		if dockerState == "exited" || dockerState == "created" {
			m.state = StateStopped
		}
		m.playerCount = 0
		m.idleStartTime = time.Time{}
		return
	}

	// If container is running in Docker, query REST API to confirm it's ready
	players, err := m.fetchPlayerCount()
	if err != nil {
		// API is not responding yet, server is starting up
		if m.state != StateStopping {
			m.state = StateStarting
		}
		m.playerCount = 0
		m.idleStartTime = time.Time{}
		return
	}

	// API responded, server is fully running
	m.state = StateRunning
	m.playerCount = players

	if players > 0 {
		m.idleStartTime = time.Time{} // Reset idle timer
	} else {
		if m.idleStartTime.IsZero() {
			m.idleStartTime = time.Now()
			log.Printf("[Monitor] Server is empty. Idle countdown started (%v timeout)", m.idleTimeout)
		} else if time.Since(m.idleStartTime) >= m.idleTimeout {
			log.Printf("[Monitor] Idle timeout reached. Initiating shutdown sequence...")
			m.state = StateStopping
			go m.shutdownSequence()
		}
	}
}

func (m *Monitor) fetchPlayerCount() (int, error) {
	url := fmt.Sprintf("%s/v1/api/metrics", m.apiUrl)
	req, err := http.NewRequestWithContext(m.ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth("admin", m.apiPassword)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metrics API returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var metrics MetricsResponse
	if err := json.Unmarshal(body, &metrics); err != nil {
		return 0, err
	}

	return metrics.CurrentPlayerNum, nil
}

func (m *Monitor) shutdownSequence() {
	log.Printf("[Monitor] Saving world before shutting down...")
	if err := m.SaveGame(); err != nil {
		log.Printf("[Monitor] Save failed: %v. Proceeding with shutdown anyway.", err)
	}

	// Try graceful API shutdown first
	url := fmt.Sprintf("%s/v1/api/shutdown", m.apiUrl)
	payload := map[string]interface{}{
		"waittime": 10,
		"message":  "Server shutting down due to inactivity. See you soon!",
	}
	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("admin", m.apiPassword)
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Printf("[Monitor] Graceful shutdown requested via API")
				// Wait for container to stop naturally
				for i := 0; i < 30; i++ {
					m.sleepFn(2 * time.Second)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					status, err := m.dockerClient.GetContainerStatus(ctx, m.containerName)
					cancel()
					if err == nil && (status == "exited" || status == "stopped" || status == "") {
						log.Printf("[Monitor] Container stopped gracefully")
						m.mu.Lock()
						m.state = StateStopped
						m.mu.Unlock()
						return
					}
				}
			}
		}
	}

	// Fallback to Docker stop if API shutdown failed or container is still running
	log.Printf("[Monitor] API shutdown failed or timed out. Stopping container via Docker...")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.dockerClient.StopContainer(ctx, m.containerName); err != nil {
		log.Printf("[Monitor] Error stopping container: %v", err)
	}

	m.mu.Lock()
	m.state = StateStopped
	m.mu.Unlock()
}
