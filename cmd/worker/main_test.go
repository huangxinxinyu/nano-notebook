package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoadWorkerConfigIncludesBoundedCollectorSender(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application")
	t.Setenv("NANO_WORKER_ADDR", ":18081")
	t.Setenv("NANO_COLLECTOR_URL", "http://collector.internal:8082/")
	t.Setenv("NANO_COLLECTOR_SERVICE_TOKEN", "sender-secret")
	t.Setenv("NANO_COLLECTOR_PRODUCER_ID", "worker-a")
	t.Setenv("NANO_TRACE_BATCH_MAX_RECORDS", "64")
	t.Setenv("NANO_TRACE_BATCH_MAX_ENCODED_BYTES", "262144")
	t.Setenv("NANO_TRACE_BATCH_MAX_DELAY", "333ms")
	t.Setenv("NANO_TRACE_HTTP_TIMEOUT", "7s")
	t.Setenv("NANO_TRACE_PURGE_MAX_COMMANDS", "8")
	t.Setenv("NANO_TRACE_PURGE_LEASE_DURATION", "20s")
	t.Setenv("NANO_TRACE_PURGE_POLL_INTERVAL", "125ms")
	t.Setenv("NANO_REPLAY_STAGING_S3_ENDPOINT", "staging.internal:9000")
	t.Setenv("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "worker-staging-key")
	t.Setenv("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "worker-staging-secret")
	t.Setenv("NANO_REPLAY_STAGING_S3_BUCKET", "worker-staging")
	t.Setenv("NANO_REPLAY_STAGING_S3_REGION", "cn-test-1")
	t.Setenv("NANO_REPLAY_STAGING_S3_USE_TLS", "true")
	t.Setenv("NANO_SOURCE_S3_ENDPOINT", "sources.internal:9000")
	t.Setenv("NANO_SOURCE_S3_ACCESS_KEY_ID", "worker-source-key")
	t.Setenv("NANO_SOURCE_S3_SECRET_ACCESS_KEY", "worker-source-secret")
	t.Setenv("NANO_SOURCE_S3_BUCKET", "source-custody")
	t.Setenv("NANO_SOURCE_S3_REGION", "cn-test-2")
	t.Setenv("NANO_SOURCE_S3_USE_TLS", "true")
	t.Setenv("NANO_QDRANT_URL", "http://qdrant.internal:6333")
	t.Setenv("NANO_QDRANT_API_KEY", "qdrant-secret")
	t.Setenv("NANO_QDRANT_COLLECTION", "source-evidence")
	t.Setenv("NANO_QDRANT_DENSE_DIMENSIONS", "768")
	t.Setenv("NANO_SOURCE_PROCESSING_LEASE_DURATION", "45s")
	t.Setenv("NANO_SOURCE_PROCESSING_HEARTBEAT_INTERVAL", "10s")
	t.Setenv("NANO_SOURCE_PROCESSING_POLL_INTERVAL", "250ms")
	t.Setenv("NANO_SOURCE_EXTRACTION_CONFIG_ID", "extract-text-v1")
	t.Setenv("NANO_SOURCE_VISION_MODEL", "gemini/gemini-2.5-flash")
	t.Setenv("NANO_SOURCE_TRANSCRIPTION_MODEL", "openai/whisper-1")
	t.Setenv("NANO_SOURCE_VISION_PROMPT_VERSION", "vision-normalize-v1")
	t.Setenv("NANO_SOURCE_PROCESSING_MAX_BYTES", "1048576")
	t.Setenv("NANO_SOURCE_PROCESSING_MAX_RUNES", "200000")
	t.Setenv("NANO_REPLAY_KEY_ID", "replay-key-7")
	t.Setenv("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA=")

	config, err := loadWorkerConfig()
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	if config.DatabaseURL != "postgres://application" || config.Addr != ":18081" {
		t.Fatalf("Application config = %#v", config)
	}
	if config.CollectorEndpoint != "http://collector.internal:8082/internal/agent-observability/v2/batches" || config.CollectorServiceToken != "sender-secret" || config.ProducerID != "worker-a" {
		t.Fatalf("Collector config = %#v", config)
	}
	if config.BatchMaxRecords != 64 || config.BatchMaxEncodedBytes != 262144 || config.PurgeMaxCommands != 8 {
		t.Fatalf("batch bounds = %#v", config)
	}
	if config.PurgeLeaseDuration != 20*time.Second || config.PurgePollInterval != 125*time.Millisecond || config.BatchMaxDelay != 333*time.Millisecond || config.HTTPTimeout != 7*time.Second {
		t.Fatalf("Sender timing = %#v", config)
	}
	if config.ReplayStagingS3.Endpoint != "staging.internal:9000" || config.ReplayStagingS3.AccessKeyID != "worker-staging-key" ||
		config.ReplayStagingS3.SecretAccessKey != "worker-staging-secret" || config.ReplayStagingS3.Bucket != "worker-staging" ||
		config.ReplayStagingS3.Region != "cn-test-1" || !config.ReplayStagingS3.UseTLS || config.ReplayKeyID != "replay-key-7" || len(config.ReplayKEK) != 32 {
		t.Fatalf("Replay staging config = %#v", config)
	}
	if config.SourceS3.Endpoint != "sources.internal:9000" || config.SourceS3.AccessKeyID != "worker-source-key" ||
		config.SourceS3.SecretAccessKey != "worker-source-secret" || config.SourceS3.Bucket != "source-custody" ||
		config.SourceS3.Region != "cn-test-2" || !config.SourceS3.UseTLS {
		t.Fatalf("Source config = %#v", config)
	}
	if config.QdrantURL != "http://qdrant.internal:6333" || config.QdrantAPIKey != "qdrant-secret" ||
		config.QdrantCollection != "source-evidence" || config.QdrantDenseDimensions != 768 ||
		config.SourceProcessingLease != 45*time.Second || config.SourceProcessingHeartbeat != 10*time.Second ||
		config.SourceProcessingPoll != 250*time.Millisecond || config.SourceExtractionConfigID != "extract-text-v1" ||
		config.SourceVisionModel != "gemini/gemini-2.5-flash" || config.SourceTranscriptionModel != "openai/whisper-1" ||
		config.SourceVisionPromptVersion != "vision-normalize-v1" ||
		config.SourceProcessingMaxBytes != 1048576 || config.SourceProcessingMaxRunes != 200000 {
		t.Fatalf("Source processing config = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsInvalidSenderBounds(t *testing.T) {
	t.Setenv("NANO_TRACE_BATCH_MAX_RECORDS", "0")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted zero max records")
	}
}

func TestShutdownTraceExporterFlushesThenStopsMemoryExporter(t *testing.T) {
	wantErr := errors.New("collector unavailable")
	flusher := &workerFlusher{err: wantErr}
	err := shutdownTraceExporter(context.Background(), flusher)
	if !errors.Is(err, wantErr) || flusher.flushCalls != 1 || flusher.shutdownCalls != 0 {
		t.Fatalf("shutdownTraceExporter err=%v flush=%d shutdown=%d", err, flusher.flushCalls, flusher.shutdownCalls)
	}
}

type workerFlusher struct {
	flushCalls    int
	shutdownCalls int
	err           error
}

func (f *workerFlusher) ForceFlush(context.Context) error {
	f.flushCalls++
	return f.err
}

func (f *workerFlusher) Shutdown(context.Context) error {
	f.shutdownCalls++
	return nil
}
