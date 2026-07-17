package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nico/palworld-auto-shutdown/internal/docker"
	"github.com/nico/palworld-auto-shutdown/internal/monitor"
	"github.com/nico/palworld-auto-shutdown/internal/proxy"
	"github.com/nico/palworld-auto-shutdown/internal/web"
)

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	valStr := getEnv(key, "")
	if valStr == "" {
		return defaultValue
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		log.Printf("[Main] Warning: invalid integer for %s: %s. Using default: %d", key, valStr, defaultValue)
		return defaultValue
	}
	return val
}

func getEnvBool(key string, defaultValue bool) bool {
	valStr := getEnv(key, "")
	if valStr == "" {
		return defaultValue
	}
	val, err := strconv.ParseBool(valStr)
	if err != nil {
		log.Printf("[Main] Warning: invalid boolean for %s: %s. Using default: %t", key, valStr, defaultValue)
		return defaultValue
	}
	return val
}

func main() {
	log.Println("[Main] Starting Palworld On-Demand Manager & UDP Proxy...")

	// Configuration loading
	containerName := getEnv("CONTAINER_NAME", "palworld-server")
	apiUrl := getEnv("PALWORLD_API_URL", "http://palworld-server:8212")
	apiPassword := getEnv("PALWORLD_API_PASSWORD", "")
	targetAddr := getEnv("PALWORLD_INTERNAL_ADDR", "palworld-server:8211")
	listenAddr := getEnv("LISTEN_ADDR", "0.0.0.0:8211")

	idleTimeoutMin := getEnvInt("IDLE_TIMEOUT_MINUTES", 15)
	checkIntervalSec := getEnvInt("CHECK_INTERVAL_SECONDS", 60)

	enableWebUI := getEnvBool("ENABLE_WEB_UI", true)
	webPort := getEnvInt("WEB_PORT", 8213)
	webUser := getEnv("WEB_USER", "")
	webPassword := getEnv("WEB_PASSWORD", "")

	if apiPassword == "" {
		log.Println("[Main] Warning: PALWORLD_API_PASSWORD is empty. API calls may fail if the server password is set.")
	}

	idleTimeout := time.Duration(idleTimeoutMin) * time.Minute
	checkInterval := time.Duration(checkIntervalSec) * time.Second

	// Initialize Docker client
	dockerCli, err := docker.NewClient()
	if err != nil {
		log.Fatalf("[Main] Failed to initialize Docker client: %v", err)
	}

	// Initialize Monitor
	m := monitor.NewMonitor(dockerCli, containerName, apiUrl, apiPassword, idleTimeout, checkInterval)

	// Initialize Proxy
	p := proxy.NewProxy(listenAddr, targetAddr, func() {
		m.TriggerWakeUp()
	})

	// Start Proxy
	if err := p.Start(); err != nil {
		log.Fatalf("[Main] Failed to start UDP proxy: %v", err)
	}
	defer p.Stop()

	// Start Monitor loop
	m.Start()
	defer m.Stop()

	// Start Web Server if enabled
	var webSrv *web.Server
	if enableWebUI {
		var err error
		webSrv, err = web.NewServer(webPort, webUser, webPassword, m, dockerCli, containerName)
		if err != nil {
			log.Fatalf("[Main] Failed to initialize web server: %v", err)
		}

		go func() {
			if err := webSrv.Start(); err != nil && err != http.ErrServerClosed {
				log.Printf("[Main] Web server error: %v", err)
			}
		}()
	} else {
		log.Println("[Main] Web UI is disabled.")
	}

	// Graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	log.Printf("[Main] Received signal %v, shutting down...", sig)

	if webSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := webSrv.Stop(ctx); err != nil {
			log.Printf("[Main] Error shutting down web server: %v", err)
		}
	}

	log.Println("[Main] Shutdown complete.")
}
