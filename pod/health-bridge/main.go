package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Config holds runtime configuration sourced from env vars (documented in
// pod/.env.example and pod/docker-compose.yml).
type Config struct {
	Port          int
	LlamaURL      string
	InfinityURL   string
	ProbeInterval time.Duration
	LogLevel      string
	Env           string
}

func loadConfig() Config {
	return Config{
		Port:          atoiOr(os.Getenv("HEALTH_BRIDGE_PORT"), 9100),
		LlamaURL:      envOr("LLAMA_URL", "http://llama:8000"),
		InfinityURL:   envOr("INFINITY_URL", "http://infinity:8002"),
		ProbeInterval: time.Duration(atoiOr(os.Getenv("PROBE_INTERVAL_SECONDS"), 10)) * time.Second,
		LogLevel:      envOr("LOG_LEVEL", "info"),
		Env:           envOr("ENV", "production"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// newLogger returns the slog.Logger used by main and every probe loop.
// JSON handler in production (matches Ifix NDJSON convention); Text in
// development for readable local output.
func newLogger(cfg Config) *slog.Logger {
	lvl := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if cfg.Env == "development" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("module", "HEALTH_BRIDGE")
}

func main() {
	selfCheck := flag.Bool("self-check", false, "exit 0 immediately (docker healthcheck)")
	flag.Parse()
	if *selfCheck {
		// Consumed by pod/docker-compose.yml healthcheck for the
		// health-bridge container. The mere fact the binary runs and
		// parses flags proves the process is alive; upstream liveness
		// is surfaced via the HTTP endpoints themselves.
		fmt.Println("ok")
		os.Exit(0)
	}

	cfg := loadConfig()
	log := newLogger(cfg)
	log.Info("starting health-bridge",
		"port", cfg.Port,
		"llama", cfg.LlamaURL,
		"infinity", cfg.InfinityURL,
		"probe_interval_s", cfg.ProbeInterval.Seconds(),
		"env", cfg.Env,
	)

	state := NewState()
	client := newHTTPClient()

	// Root context cancelled on SIGTERM/SIGINT.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("signal received, shutting down", "signal", sig.String())
		cancel()
	}()

	// Spawn probe loops (one per upstream).
	var wg sync.WaitGroup
	type probeDef struct {
		name  string
		url   string
		probe func(context.Context, *http.Client, string, *slog.Logger) ProbeResult
	}
	probes := []probeDef{
		{UpstreamLLM, cfg.LlamaURL, probeLLM},
		{UpstreamEmbed, cfg.InfinityURL, probeEmbed},
	}
	for _, p := range probes {
		wg.Add(1)
		go func(p probeDef) {
			defer wg.Done()
			ProbeLoop(ctx, log, state, p.name, p.probe, client, p.url, cfg.ProbeInterval)
		}(p)
	}

	// HTTP server.
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux(state),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		// graceful shutdown on SIGTERM/SIGINT
	case err := <-serverErr:
		log.Error("http server failed", "err", err)
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown error", "err", err)
	}
	wg.Wait()
	log.Info("health-bridge exited cleanly")
}
