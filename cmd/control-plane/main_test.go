package main

import "testing"

func TestLoadControlPlaneConfigIncludesCollectorQueryAndReplayKey(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application")
	t.Setenv("NANO_CONTROL_PLANE_ADDR", ":18080")
	t.Setenv("NANO_COLLECTOR_URL", "http://collector.internal:8082/")
	t.Setenv("NANO_COLLECTOR_QUERY_TOKEN", "query-secret")
	t.Setenv("NANO_COLLECTOR_SERVICE_TOKEN", "ingest-secret")
	t.Setenv("NANO_CONTROL_PLANE_PRODUCER_ID", "control-plane-a")
	t.Setenv("NANO_REPLAY_KEY_ID", "replay-key-7")
	t.Setenv("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA=")
	t.Setenv("NANO_SOURCE_S3_ENDPOINT", "sources.internal:9000")
	t.Setenv("NANO_SOURCE_S3_ACCESS_KEY_ID", "source-key")
	t.Setenv("NANO_SOURCE_S3_SECRET_ACCESS_KEY", "source-secret")
	t.Setenv("NANO_SOURCE_S3_BUCKET", "source-custody")
	t.Setenv("NANO_SOURCE_S3_REGION", "cn-test-1")
	t.Setenv("NANO_SOURCE_S3_USE_TLS", "true")
	t.Setenv("NANO_FETCHER_URL", "http://fetcher.internal:8083/")

	config, err := loadControlPlaneConfig()
	if err != nil {
		t.Fatalf("loadControlPlaneConfig: %v", err)
	}
	if config.DatabaseURL != "postgres://application" || config.Addr != ":18080" ||
		config.CollectorURL != "http://collector.internal:8082" || config.CollectorQueryToken != "query-secret" ||
		config.CollectorServiceToken != "ingest-secret" || config.ProducerID != "control-plane-a" ||
		config.ReplayKeyID != "replay-key-7" || len(config.ReplayKEK) != 32 ||
		config.SourceS3.Endpoint != "sources.internal:9000" || config.SourceS3.AccessKeyID != "source-key" ||
		config.SourceS3.SecretAccessKey != "source-secret" || config.SourceS3.Bucket != "source-custody" ||
		config.SourceS3.Region != "cn-test-1" || !config.SourceS3.UseTLS {
		t.Fatalf("Control Plane config = %#v", config)
	}
	if config.FetcherURL != "http://fetcher.internal:8083" {
		t.Fatalf("Fetcher URL = %q", config.FetcherURL)
	}
}
