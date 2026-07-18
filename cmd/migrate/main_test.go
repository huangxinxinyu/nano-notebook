package main

import "testing"

func TestLoadMigrationConfigKeepsApplicationAndCollectorDatabasesSeparate(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application")
	t.Setenv("NANO_COLLECTOR_DATABASE_URL", "postgres://observability")

	config := loadMigrationConfig()
	if config.ApplicationDatabaseURL != "postgres://application" {
		t.Fatalf("ApplicationDatabaseURL = %q", config.ApplicationDatabaseURL)
	}
	if config.CollectorDatabaseURL != "postgres://observability" {
		t.Fatalf("CollectorDatabaseURL = %q", config.CollectorDatabaseURL)
	}
	if config.ApplicationDatabaseURL == config.CollectorDatabaseURL {
		t.Fatal("Application and Collector migration DSNs are coupled")
	}
}
