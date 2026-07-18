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
	t.Setenv("NANO_OUTBOX_MAX_RECORDS", "64")
	t.Setenv("NANO_OUTBOX_MAX_ENCODED_BYTES", "262144")
	t.Setenv("NANO_OUTBOX_MAX_TRACES", "8")
	t.Setenv("NANO_OUTBOX_LEASE_DURATION", "20s")
	t.Setenv("NANO_OUTBOX_POLL_INTERVAL", "125ms")
	t.Setenv("NANO_OUTBOX_MAX_DELAY", "333ms")
	t.Setenv("NANO_OUTBOX_HTTP_TIMEOUT", "7s")
	t.Setenv("NANO_REPLAY_STAGING_S3_ENDPOINT", "staging.internal:9000")
	t.Setenv("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "worker-staging-key")
	t.Setenv("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "worker-staging-secret")
	t.Setenv("NANO_REPLAY_STAGING_S3_BUCKET", "worker-staging")
	t.Setenv("NANO_REPLAY_STAGING_S3_REGION", "cn-test-1")
	t.Setenv("NANO_REPLAY_STAGING_S3_USE_TLS", "true")
	t.Setenv("NANO_REPLAY_KEY_ID", "replay-key-7")
	t.Setenv("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA=")

	config, err := loadWorkerConfig()
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	if config.DatabaseURL != "postgres://application" || config.Addr != ":18081" {
		t.Fatalf("Application config = %#v", config)
	}
	if config.CollectorEndpoint != "http://collector.internal:8082/internal/agent-observability/v1/batches" || config.CollectorServiceToken != "sender-secret" || config.ProducerID != "worker-a" {
		t.Fatalf("Collector config = %#v", config)
	}
	if config.MaxRecords != 64 || config.MaxEncodedBytes != 262144 || config.MaxTraces != 8 {
		t.Fatalf("batch bounds = %#v", config)
	}
	if config.LeaseDuration != 20*time.Second || config.PollInterval != 125*time.Millisecond || config.MaxDelay != 333*time.Millisecond || config.HTTPTimeout != 7*time.Second {
		t.Fatalf("Sender timing = %#v", config)
	}
	if config.ReplayStagingS3.Endpoint != "staging.internal:9000" || config.ReplayStagingS3.AccessKeyID != "worker-staging-key" ||
		config.ReplayStagingS3.SecretAccessKey != "worker-staging-secret" || config.ReplayStagingS3.Bucket != "worker-staging" ||
		config.ReplayStagingS3.Region != "cn-test-1" || !config.ReplayStagingS3.UseTLS || config.ReplayKeyID != "replay-key-7" || len(config.ReplayKEK) != 32 {
		t.Fatalf("Replay staging config = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsInvalidSenderBounds(t *testing.T) {
	t.Setenv("NANO_OUTBOX_MAX_RECORDS", "0")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted zero max records")
	}
}

func TestFlushOutboxOnShutdownDelegatesToDurableSender(t *testing.T) {
	wantErr := errors.New("collector unavailable")
	flusher := &workerFlusher{err: wantErr}
	err := flushOutboxOnShutdown(context.Background(), flusher)
	if !errors.Is(err, wantErr) || flusher.calls != 1 {
		t.Fatalf("flushOutboxOnShutdown err=%v calls=%d", err, flusher.calls)
	}
}

type workerFlusher struct {
	calls int
	err   error
}

func (f *workerFlusher) ForceFlush(context.Context) error {
	f.calls++
	return f.err
}
