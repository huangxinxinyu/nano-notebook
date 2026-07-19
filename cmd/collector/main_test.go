package main

import "testing"

func TestLoadConfigUsesDedicatedCollectorDatabaseAndPool(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application-should-not-be-used")
	t.Setenv("NANO_COLLECTOR_DATABASE_URL", "postgres://observability")
	t.Setenv("NANO_COLLECTOR_DATABASE_MAX_CONNS", "24")
	t.Setenv("NANO_COLLECTOR_DATABASE_MIN_CONNS", "3")
	t.Setenv("NANO_COLLECTOR_PROJECTION_DATABASE_MAX_CONNS", "5")
	t.Setenv("NANO_COLLECTOR_PROJECTION_DATABASE_MIN_CONNS", "1")
	t.Setenv("NANO_COLLECTOR_QUERY_DATABASE_MAX_CONNS", "7")
	t.Setenv("NANO_COLLECTOR_QUERY_DATABASE_MIN_CONNS", "2")
	t.Setenv("NANO_COLLECTOR_ADDR", ":18082")
	t.Setenv("NANO_COLLECTOR_SERVICE_TOKEN", "test-service-token")
	t.Setenv("NANO_COLLECTOR_QUERY_TOKEN", "test-query-token")
	t.Setenv("NANO_COLLECTOR_PRODUCER_ID", "test-worker")
	t.Setenv("NANO_COLLECTOR_PRODUCER_ID_PREFIX", "test-")
	t.Setenv("NANO_REPLAY_STAGING_S3_ENDPOINT", "staging.internal:9000")
	t.Setenv("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "collector-staging-reader")
	t.Setenv("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "staging-reader-secret")
	t.Setenv("NANO_REPLAY_STAGING_S3_BUCKET", "worker-staging")
	t.Setenv("NANO_REPLAY_S3_ENDPOINT", "replay.internal:9000")
	t.Setenv("NANO_REPLAY_S3_ACCESS_KEY_ID", "collector-replay-writer")
	t.Setenv("NANO_REPLAY_S3_SECRET_ACCESS_KEY", "replay-writer-secret")
	t.Setenv("NANO_REPLAY_S3_BUCKET", "collector-replay")

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
	if config.ProjectionDatabaseMaxConns != 5 || config.ProjectionDatabaseMinConns != 1 || config.QueryDatabaseMaxConns != 7 || config.QueryDatabaseMinConns != 2 {
		t.Fatalf("projection/query pools = %#v", config)
	}
	if config.Addr != ":18082" || config.ServiceToken != "test-service-token" || config.QueryToken != "test-query-token" || config.ProducerID != "test-worker" || config.ProducerIDPrefix != "test-" {
		t.Fatalf("Collector config = %#v", config)
	}
	if config.ReplayStagingS3.Endpoint != "staging.internal:9000" || config.ReplayStagingS3.AccessKeyID != "collector-staging-reader" || config.ReplayStagingS3.Bucket != "worker-staging" ||
		config.ReplayS3.Endpoint != "replay.internal:9000" || config.ReplayS3.AccessKeyID != "collector-replay-writer" || config.ReplayS3.Bucket != "collector-replay" {
		t.Fatalf("Collector Replay config = %#v", config)
	}
}

func TestLoadConfigRejectsInvalidCollectorPoolBounds(t *testing.T) {
	t.Setenv("NANO_COLLECTOR_DATABASE_MAX_CONNS", "2")
	t.Setenv("NANO_COLLECTOR_DATABASE_MIN_CONNS", "3")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig accepted min connections above max")
	}
}
