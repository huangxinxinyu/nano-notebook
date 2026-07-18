package main

import "testing"

func TestLoadConfigUsesDedicatedCollectorDatabaseAndPool(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application-should-not-be-used")
	t.Setenv("NANO_COLLECTOR_DATABASE_URL", "postgres://observability")
	t.Setenv("NANO_COLLECTOR_DATABASE_MAX_CONNS", "24")
	t.Setenv("NANO_COLLECTOR_DATABASE_MIN_CONNS", "3")
	t.Setenv("NANO_COLLECTOR_ADDR", ":18082")
	t.Setenv("NANO_COLLECTOR_SERVICE_TOKEN", "test-service-token")
	t.Setenv("NANO_COLLECTOR_PRODUCER_ID", "test-worker")

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if config.DatabaseURL != "postgres://observability" {
		t.Fatalf("DatabaseURL = %q", config.DatabaseURL)
	}
	if config.DatabaseMaxConns != 24 || config.DatabaseMinConns != 3 {
		t.Fatalf("database pool = %d/%d", config.DatabaseMaxConns, config.DatabaseMinConns)
	}
	if config.Addr != ":18082" || config.ServiceToken != "test-service-token" || config.ProducerID != "test-worker" {
		t.Fatalf("Collector config = %#v", config)
	}
}

func TestLoadConfigRejectsInvalidCollectorPoolBounds(t *testing.T) {
	t.Setenv("NANO_COLLECTOR_DATABASE_MAX_CONNS", "2")
	t.Setenv("NANO_COLLECTOR_DATABASE_MIN_CONNS", "3")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig accepted min connections above max")
	}
}
