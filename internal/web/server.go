package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/nico/palworld-auto-shutdown/internal/docker"
	"github.com/nico/palworld-auto-shutdown/internal/monitor"
)

type Server struct {
	port       int
	user       string
	password   string
	monitor    *monitor.Monitor
	dockerName string
	dockerCli  docker.DockerClient
	httpServer *http.Server
}

func NewServer(port int, user, password string, m *monitor.Monitor, dockerCli docker.DockerClient, dockerName string) (*Server, error) {
	return &Server{
		port:       port,
		user:       user,
		password:   password,
		monitor:    m,
		dockerName: dockerName,
		dockerCli:  dockerCli,
	}, nil
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/status", s.basicAuth(s.handleStatus))
	mux.HandleFunc("/api/control", s.basicAuth(s.handleControl))
	mux.HandleFunc("/api/logs", s.basicAuth(s.handleLogs))

	// Static UI routes
	uiDir := "./ui"
	if _, err := os.Stat(uiDir); err == nil {
		fileServer := http.FileServer(http.Dir(uiDir))
		mux.Handle("/", s.basicAuth(func(w http.ResponseWriter, r *http.Request) {
			// Disable caching for UI development/updates
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			fileServer.ServeHTTP(w, r)
		}))
	} else {
		mux.HandleFunc("/", s.basicAuth(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<h1>Palworld Manager UI placeholder</h1><p>ui/ directory not found</p>"))
		}))
	}

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	log.Printf("[Web] Admin server listening on http://0.0.0.0:%d", s.port)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

func (s *Server) basicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.user == "" || s.password == "" {
			// Basic auth not configured, pass through
			next.ServeHTTP(w, r)
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(s.user)) != 1 || subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Palworld Manager"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state, players, idleRemaining := s.monitor.GetStatus()

	resp := map[string]interface{}{
		"state":              state,
		"players":            players,
		"idle_remaining_sec": int(idleRemaining.Seconds()),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action string `json:"action"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var err error
	switch req.Action {
	case "start":
		err = s.monitor.ForceStart()
	case "stop":
		err = s.monitor.ForceStop()
	case "save":
		err = s.monitor.SaveGame()
	default:
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if val, err := strconv.Atoi(limitStr); err == nil && val > 0 && val <= 500 {
		limit = val
	}

	logs, err := s.dockerCli.GetContainerLogs(ctx, s.dockerName, strconv.Itoa(limit))
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("Could not retrieve logs for container %s: %v\n", s.dockerName, err)))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(logs)
}
