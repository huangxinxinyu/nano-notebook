package main

import (
	"testing"
	"time"
)

func TestLoadFetcherConfigUsesOnlyBoundedNetworkSettings(t *testing.T) {
	t.Setenv("NANO_FETCHER_ADDR", "127.0.0.1:18083")
	t.Setenv("NANO_FETCHER_MAX_REDIRECTS", "4")
	t.Setenv("NANO_FETCHER_MAX_COMPRESSED_BYTES", "1048576")
	t.Setenv("NANO_FETCHER_MAX_EXPANDED_BYTES", "4194304")
	t.Setenv("NANO_FETCHER_TIMEOUT", "13s")
	config, err := loadFetcherConfig()
	if err != nil {
		t.Fatalf("loadFetcherConfig: %v", err)
	}
	if config.Addr != "127.0.0.1:18083" || config.MaxRedirects != 4 ||
		config.MaxCompressedBytes != 1048576 || config.MaxExpandedBytes != 4194304 ||
		config.Timeout != 13*time.Second {
		t.Fatalf("Fetcher config = %+v", config)
	}
}

func TestLoadFetcherConfigRejectsExpandedBudgetBelowCompressedBudget(t *testing.T) {
	t.Setenv("NANO_FETCHER_MAX_COMPRESSED_BYTES", "2048")
	t.Setenv("NANO_FETCHER_MAX_EXPANDED_BYTES", "1024")
	if _, err := loadFetcherConfig(); err == nil {
		t.Fatal("loadFetcherConfig accepted inconsistent response budgets")
	}
}
