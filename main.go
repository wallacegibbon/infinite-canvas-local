// Command comfyui-gateway is the local service behind Comfy Loft: it runs ComfyUI
// workflows and offers them to the canvas as models. It speaks its own protocol
// (`/api/...`), not OpenAI's.
//
// It is a single statically linked binary: no interpreter, no virtualenv, no
// node_modules. Configuration comes from the environment (see README), and all
// mutable state lives in the data directory.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logx.New("gateway")

	app, err := server.New(cfg, logger)
	if err != nil {
		return err
	}

	if !config.IsLoopbackHost(cfg.GatewayHost) {
		logger.Warn("listening on %s with no authentication: anyone who can reach this address can run workflows, read their artifacts and drive a canvas's agent", cfg.GatewayHost)
	}

	address := net.JoinHostPort(cfg.GatewayHost, fmt.Sprint(cfg.GatewayPort))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", address, err)
	}

	httpServer := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		// No global write timeout: a video generation legitimately holds a request
		// open for minutes, and cancelling it must come from the client or ComfyUI.
		IdleTimeout: 120 * time.Second,
	}

	logger.Info("ComfyUI gateway %s listening on http://%s", config.Version, address)
	logger.Info("ComfyUI: %s | workflows: %s", cfg.ComfyURL, cfg.WorkflowDir)

	cleanupDone := make(chan struct{})
	if cfg.TaskRetentionDays > 0 || cfg.MaxStoredTasks > 0 {
		go taskCleanupLoop(cfg, app, logger, cleanupDone)
	} else {
		close(cleanupDone)
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil {
			return err
		}
	case received := <-signals:
		logger.Info("received %s, shutting down", received)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown timed out: %s", err)
	}
	app.Tasks().Close()
	<-cleanupDone
	logger.Info("stopped")
	return nil
}

// taskCleanupLoop periodically prunes finished tasks and their output files.
func taskCleanupLoop(cfg *config.Config, app *server.Server, logger *logx.Logger, done chan struct{}) {
	defer close(done)
	interval := time.Duration(cfg.CleanupIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			app.Tasks().Cleanup(cfg.TaskRetentionDays, cfg.MaxStoredTasks)
		case <-done:
			return
		}
	}
}
