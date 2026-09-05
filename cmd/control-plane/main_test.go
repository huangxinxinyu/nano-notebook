package main

import (
	"testing"
	"time"
)

func TestLoadControlPlaneConfigIncludesCollectorQueryAndReplayKey(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application")
	t.Setenv("NANO_CONTROL_PLANE_ADDR", ":18080")
	t.Setenv("NANO_COLLECTOR_URL", "http://collector.internal:8082/")
	t.Setenv("NANO_COLLECTOR_QUERY_TOKEN", "query-secret")
	t.Setenv("NANO_CONTROL_PLANE_PRODUCER_ID", "control-plane-a")
	t.Setenv("NANO_REPLAY_KEY_ID", "replay-key-7")
	t.Setenv("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA=")
	t.Setenv("NANO_SOURCE_S3_ENDPOINT", "sources.internal:9000")
	t.Setenv("NANO_SOURCE_S3_ACCESS_KEY_ID", "source-key")
	t.Setenv("NANO_SOURCE_S3_SECRET_ACCESS_KEY", "source-secret")
	t.Setenv("NANO_SOURCE_S3_BUCKET", "source-custody")
	t.Setenv("NANO_SOURCE_S3_REGION", "cn-test-1")
	t.Setenv("NANO_SOURCE_S3_USE_TLS", "true")
	t.Setenv("NANO_WEB_READER_URL", "http://reader.internal:8085/")
	t.Setenv("NANO_WEB_READER_SERVICE_TOKEN", "reader-secret")
	t.Setenv("NANO_WEB_READER_TIMEOUT", "45s")

	config, err := loadControlPlaneConfig()
	if err != nil {
		t.Fatalf("loadControlPlaneConfig: %v", err)
	}
	if config.DatabaseURL != "postgres://application" || config.Addr != ":18080" ||
		config.CollectorURL != "http://collector.internal:8082" || config.CollectorQueryToken != "query-secret" ||
		config.ProducerID != "control-plane-a" ||
		config.ReplayKeyID != "replay-key-7" || len(config.ReplayKEK) != 32 ||
		config.SourceS3.Endpoint != "sources.internal:9000" || config.SourceS3.AccessKeyID != "source-key" ||
		config.SourceS3.SecretAccessKey != "source-secret" || config.SourceS3.Bucket != "source-custody" ||
		config.SourceS3.Region != "cn-test-1" || !config.SourceS3.UseTLS {
		t.Fatalf("Control Plane config = %#v", config)
	}
	if config.WebReaderURL != "http://reader.internal:8085" || config.WebReaderServiceToken != "reader-secret" || config.WebReaderTimeout != 45*time.Second {
		t.Fatalf("Web Reader config = %q/%q/%s", config.WebReaderURL, config.WebReaderServiceToken, config.WebReaderTimeout)
	}
}

func TestLoadControlPlaneConfigDefaultsToQwenPlus(t *testing.T) {
	t.Setenv("NANO_CHAT_MODEL", "")

	config, err := loadControlPlaneConfig()
	if err != nil {
		t.Fatalf("loadControlPlaneConfig: %v", err)
	}
	if config.DefaultModel != "aliyun/qwen-plus" {
		t.Fatalf("Default model = %q, want aliyun/qwen-plus", config.DefaultModel)
	}
	if config.AgentRelease.String() != "nano.default@26" {
		t.Fatalf("Agent release = %q", config.AgentRelease)
	}
}

func TestLoadControlPlaneConfigDefaultsKafkaTraceProducer(t *testing.T) {
	t.Setenv("NANO_AGENT_TRACE_KAFKA_BROKERS", "")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_TOPIC", "")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_CLIENT_ID", "")

	config, err := loadControlPlaneConfig()
	if err != nil {
		t.Fatalf("loadControlPlaneConfig: %v", err)
	}
	if len(config.TraceKafkaBrokers) != 1 || config.TraceKafkaBrokers[0] != "127.0.0.1:59092" ||
		config.TraceKafkaTopic != "nano.observability.agent-trace.v1" || config.TraceKafkaClientID != "nano-control-plane-agent-trace" {
		t.Fatalf("Agent Trace Kafka defaults = %#v", config)
	}
}

func TestLoadControlPlaneConfigRejectsMissingKafkaTraceConfig(t *testing.T) {
	t.Setenv("NANO_AGENT_TRACE_KAFKA_BROKERS", " ")
	if _, err := loadControlPlaneConfig(); err == nil {
		t.Fatal("loadControlPlaneConfig accepted Kafka without brokers")
	}
}

func TestLoadControlPlaneConfigIgnoresRemovedTraceTransportAndRetrySettings(t *testing.T) {
	t.Setenv("NANO_AGENT_TRACE_TRANSPORT", "udp")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_MAX_RETRIES", "not-a-number")
	if _, err := loadControlPlaneConfig(); err != nil {
		t.Fatalf("removed Trace settings still affect Control Plane config: %v", err)
	}
}

func TestLoadControlPlaneConfigRejectsMutableAgentRelease(t *testing.T) {
	t.Setenv("NANO_AGENT_RELEASE", "nano.default@latest")
	if _, err := loadControlPlaneConfig(); err == nil {
		t.Fatal("loadControlPlaneConfig accepted mutable Agent release")
	}
}
