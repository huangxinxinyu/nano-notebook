package main

import "testing"

func TestLoadControlPlaneConfigIncludesCollectorQueryAndReplayKey(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application")
	t.Setenv("NANO_CONTROL_PLANE_ADDR", ":18080")
	t.Setenv("NANO_COLLECTOR_URL", "http://collector.internal:8082/")
	t.Setenv("NANO_COLLECTOR_QUERY_TOKEN", "query-secret")
	t.Setenv("NANO_REPLAY_KEY_ID", "replay-key-7")
	t.Setenv("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA=")

	config, err := loadControlPlaneConfig()
	if err != nil {
		t.Fatalf("loadControlPlaneConfig: %v", err)
	}
	if config.DatabaseURL != "postgres://application" || config.Addr != ":18080" ||
		config.CollectorURL != "http://collector.internal:8082" || config.CollectorQueryToken != "query-secret" ||
		config.ReplayKeyID != "replay-key-7" || len(config.ReplayKEK) != 32 {
		t.Fatalf("Control Plane config = %#v", config)
	}
}
