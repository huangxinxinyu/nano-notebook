package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	sourcefetcher "github.com/huangxinxinyu/nano-notebook/internal/fetcher"
)

type fetcherConfig struct {
	Addr               string
	MaxRedirects       int
	MaxCompressedBytes int64
	MaxExpandedBytes   int64
	Timeout            time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config, err := loadFetcherConfig()
	if err != nil {
		slog.Error("Source Fetcher configuration invalid", "error", err)
		os.Exit(1)
	}
	core := sourcefetcher.New(sourcefetcher.Config{
		MaxRedirects: config.MaxRedirects, MaxCompressedBytes: config.MaxCompressedBytes,
		MaxExpandedBytes: config.MaxExpandedBytes, Timeout: config.Timeout,
	})
	server := &http.Server{
		Addr: config.Addr, Handler: sourcefetcher.NewHTTPHandler(core),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: config.Timeout + 5*time.Second,
		WriteTimeout: config.Timeout + 5*time.Second, IdleTimeout: 30 * time.Second,
	}
	go func() {
		slog.Info("Source Fetcher listening", "addr", config.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Source Fetcher failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Source Fetcher shutdown failed", "error", err)
		os.Exit(1)
	}
}

func loadFetcherConfig() (fetcherConfig, error) {
	maxRedirects, err := fetcherEnvInt("NANO_FETCHER_MAX_REDIRECTS", 5)
	if err != nil {
		return fetcherConfig{}, err
	}
	maxCompressed, err := fetcherEnvInt64("NANO_FETCHER_MAX_COMPRESSED_BYTES", 20*1024*1024)
	if err != nil {
		return fetcherConfig{}, err
	}
	maxExpanded, err := fetcherEnvInt64("NANO_FETCHER_MAX_EXPANDED_BYTES", 50*1024*1024)
	if err != nil {
		return fetcherConfig{}, err
	}
	timeout, err := fetcherEnvDuration("NANO_FETCHER_TIMEOUT", 20*time.Second)
	if err != nil {
		return fetcherConfig{}, err
	}
	config := fetcherConfig{
		Addr: fetcherEnv("NANO_FETCHER_ADDR", "127.0.0.1:8083"), MaxRedirects: maxRedirects,
		MaxCompressedBytes: maxCompressed, MaxExpandedBytes: maxExpanded, Timeout: timeout,
	}
	if strings.TrimSpace(config.Addr) == "" || config.MaxRedirects < 1 || config.MaxRedirects > 10 ||
		config.MaxCompressedBytes < 1 || config.MaxExpandedBytes < config.MaxCompressedBytes ||
		config.MaxExpandedBytes > 100*1024*1024 || config.Timeout <= 0 || config.Timeout > time.Minute {
		return fetcherConfig{}, errors.New("Source Fetcher configuration is incomplete or inconsistent")
	}
	return config, nil
}

func fetcherEnvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func fetcherEnvInt64(key string, fallback int64) (int64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func fetcherEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func fetcherEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
