package main

import "testing"

func TestLoadTraceMigrationVerificationConfigUsesIndependentDatabases(t *testing.T) {
	config, err := loadTraceMigrationVerificationConfig(func(key string) string {
		switch key {
		case "NANO_DATABASE_URL":
			return "postgres://application"
		case "NANO_COLLECTOR_DATABASE_URL":
			return "postgres://observability"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("loadTraceMigrationVerificationConfig: %v", err)
	}
	if config.ApplicationDatabaseURL != "postgres://application" || config.CollectorDatabaseURL != "postgres://observability" {
		t.Fatalf("verification config = %#v", config)
	}
}

func TestLoadTraceMigrationVerificationConfigRejectsCoupledDatabases(t *testing.T) {
	_, err := loadTraceMigrationVerificationConfig(func(string) string { return "postgres://same-database" })
	if err == nil {
		t.Fatal("coupled Application and Collector databases were accepted")
	}
}

func TestLoadTraceMigrationVerificationConfigRejectsSameDatabaseThroughDifferentRoles(t *testing.T) {
	_, err := loadTraceMigrationVerificationConfig(func(key string) string {
		if key == "NANO_DATABASE_URL" {
			return "postgres://application-role@database.internal:5432/nano?sslmode=require"
		}
		return "postgres://collector-role@database.internal:5432/nano?sslmode=require"
	})
	if err == nil {
		t.Fatal("same database reached through different roles was accepted")
	}
}
