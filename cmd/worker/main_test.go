package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestLoadWorkerConfigDefaultsToGeminiEmbeddingCollection(t *testing.T) {
	for _, name := range []string{"NANO_QDRANT_COLLECTION", "NANO_QDRANT_DENSE_DIMENSIONS", "NANO_RETRIEVAL_BOOTSTRAP_MODE", "NANO_RETRIEVAL_BOOTSTRAP_CONFIG_PATH"} {
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	config, err := loadWorkerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.QdrantCollection != "nano-source-evidence-gemini-2-768-v1" || config.QdrantDenseDimensions != 768 {
		t.Fatalf("Qdrant embedding defaults=%q/%d", config.QdrantCollection, config.QdrantDenseDimensions)
	}
	if config.RetrievalBootstrapMode != "development" || config.RetrievalBootstrapConfigPath != "evals/rag/pinned-config-v1.json" {
		t.Fatalf("Retrieval bootstrap defaults=%q/%q", config.RetrievalBootstrapMode, config.RetrievalBootstrapConfigPath)
	}
}

func TestLoadWorkerConfigAcceptsRequiredRetrievalAuthority(t *testing.T) {
	t.Setenv("NANO_RETRIEVAL_BOOTSTRAP_MODE", "required")
	t.Setenv("NANO_RETRIEVAL_BOOTSTRAP_CONFIG_PATH", "/release/pinned-config.json")
	config, err := loadWorkerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.RetrievalBootstrapMode != "required" || config.RetrievalBootstrapConfigPath != "/release/pinned-config.json" {
		t.Fatalf("Retrieval bootstrap config=%q/%q", config.RetrievalBootstrapMode, config.RetrievalBootstrapConfigPath)
	}
}

func TestLoadWorkerConfigRejectsUnknownRetrievalBootstrapMode(t *testing.T) {
	t.Setenv("NANO_RETRIEVAL_BOOTSTRAP_MODE", "automatic")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted unknown Retrieval bootstrap mode")
	}
}

func TestPrepareRetrievalAuthorityBootstrapsPinnedDevelopmentConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pinned.json")
	if err := os.WriteFile(configPath, []byte(`{
		"index": {
			"chunk": {"max_runes": 800, "overlap_runes": 120, "preserve_heading_context": true},
			"analyzer_id": "nano-mixed-v1",
			"bm25_k1": 1.2,
			"bm25_b": 0.75,
			"bm25_average_document_length": 240,
			"embedding_model": "gemini/gemini-embedding-2",
			"embedding_dimensions": 768,
			"embedding_profile_id": "gemini-retrieval-v1",
			"dense_candidates": 40,
			"sparse_candidates": 40,
			"rrf_k": 60,
			"reranker_id": "qwen-rerank-v1",
			"rerank_candidates": 20,
			"degradation_policy_id": "hybrid-required-v1"
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	authority := &workerRetrievalAuthority{requiredErr: retrieval.ErrVersionNotFound}
	version, created, err := prepareRetrievalAuthority(context.Background(), authority, workerConfig{
		RetrievalBootstrapMode: "development", RetrievalBootstrapConfigPath: configPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || version.ID != developmentBaselineVersionID || authority.bootstrapCalls != 1 || authority.requireCalls != 1 ||
		authority.config.EmbeddingModel != "gemini/gemini-embedding-2" || authority.config.EmbeddingDimensions != 768 {
		t.Fatalf("version=%+v created=%t authority=%+v", version, created, authority)
	}
}

func TestPrepareRetrievalAuthorityKeepsExistingDevelopmentAuthorityWithoutReadingBootstrapConfig(t *testing.T) {
	authority := &workerRetrievalAuthority{}
	version, created, err := prepareRetrievalAuthority(context.Background(), authority, workerConfig{
		RetrievalBootstrapMode: "development", RetrievalBootstrapConfigPath: "/does/not/exist.json",
	})
	if err != nil || created || version.ID != "" || authority.requireCalls != 1 || authority.bootstrapCalls != 0 {
		t.Fatalf("prepare existing development version=%+v created=%t err=%v authority=%+v", version, created, err, authority)
	}
}

func TestPrepareRetrievalAuthorityRequiresExistingProductionAuthorityWithoutReadingBootstrapConfig(t *testing.T) {
	authority := &workerRetrievalAuthority{requiredErr: retrieval.ErrVersionNotFound}
	_, _, err := prepareRetrievalAuthority(context.Background(), authority, workerConfig{
		RetrievalBootstrapMode: "required", RetrievalBootstrapConfigPath: "/does/not/exist.json",
	})
	if !errors.Is(err, retrieval.ErrVersionNotFound) || authority.requireCalls != 1 || authority.bootstrapCalls != 0 {
		t.Fatalf("prepare required err=%v authority=%+v", err, authority)
	}
}

type workerRetrievalAuthority struct {
	bootstrapCalls int
	requireCalls   int
	config         retrieval.IndexConfig
	requiredErr    error
}

func (a *workerRetrievalAuthority) BootstrapDevelopment(_ context.Context, id, provenance string, config retrieval.IndexConfig) (retrieval.IndexVersion, bool, error) {
	a.bootstrapCalls++
	a.config = config
	return retrieval.IndexVersion{ID: id, Status: retrieval.VersionActive, PromotedByEvalRunID: provenance, Config: config}, true, nil
}

func (a *workerRetrievalAuthority) RequireActive(context.Context) error {
	a.requireCalls++
	return a.requiredErr
}

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
	t.Setenv("NANO_SOURCE_MAX_VISION_PAGES", "12")
	t.Setenv("NANO_DOCUMENT_RENDERER_URL", "http://renderer.internal:8084/")
	t.Setenv("NANO_DOCUMENT_RENDERER_SERVICE_TOKEN", "renderer-secret")
	t.Setenv("NANO_DOCUMENT_RENDER_CONFIG_ID", "pdfium-lo-v7")
	t.Setenv("NANO_DOCUMENT_RENDER_TIMEOUT", "70s")
	t.Setenv("NANO_DOCUMENT_RENDER_MAX_PAGES", "25")
	t.Setenv("NANO_DOCUMENT_RENDER_DPI", "144")
	t.Setenv("NANO_DOCUMENT_RENDER_MAX_PIXELS_PER_PAGE", "3000000")
	t.Setenv("NANO_DOCUMENT_RENDER_MAX_OUTPUT_BYTES", "4194304")
	t.Setenv("NANO_SOURCE_PROCESSING_MAX_BYTES", "1048576")
	t.Setenv("NANO_SOURCE_PROCESSING_MAX_RUNES", "200000")
	t.Setenv("NANO_AGENT_INTERACTIVE_CONCURRENCY", "6")
	t.Setenv("NANO_SOURCE_PROCESSING_CONCURRENCY", "4")
	t.Setenv("NANO_REPLAY_KEY_ID", "replay-key-7")
	t.Setenv("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA=")
	t.Setenv("NANO_MAIL_SMTP_ADDR", "mailpit.internal:1025")
	t.Setenv("NANO_MAIL_FROM", "nano@example.test")
	t.Setenv("NANO_WEB_BASE_URL", "http://web.internal:5173/")
	t.Setenv("NANO_MAIL_LEASE_DURATION", "25s")
	t.Setenv("NANO_MAIL_POLL_INTERVAL", "175ms")
	t.Setenv("NANO_MAIL_SMTP_TIMEOUT", "4s")

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
		config.SourceVisionPromptVersion != "vision-normalize-v1" || config.SourceMaxVisionPages != 12 ||
		config.DocumentRendererURL != "http://renderer.internal:8084" || config.DocumentRendererServiceToken != "renderer-secret" ||
		config.DocumentRenderConfigID != "pdfium-lo-v7" || config.DocumentRenderTimeout != 70*time.Second ||
		config.DocumentRenderMaxPages != 25 || config.DocumentRenderDPI != 144 || config.DocumentRenderMaxPixelsPerPage != 3_000_000 ||
		config.DocumentRenderMaxOutputBytes != 4<<20 ||
		config.SourceProcessingMaxBytes != 1048576 || config.SourceProcessingMaxRunes != 200000 ||
		config.AgentInteractiveConcurrency != 6 || config.SourceProcessingConcurrency != 4 {
		t.Fatalf("Source processing config = %#v", config)
	}
	if config.MailSMTPAddr != "mailpit.internal:1025" || config.MailFrom != "nano@example.test" ||
		config.WebBaseURL != "http://web.internal:5173" || config.MailLeaseDuration != 25*time.Second ||
		config.MailPollInterval != 175*time.Millisecond || config.MailSMTPTimeout != 4*time.Second {
		t.Fatalf("mail config = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsWorkloadCapacityAboveTenInteractiveJobs(t *testing.T) {
	t.Setenv("NANO_AGENT_INTERACTIVE_CONCURRENCY", "7")
	t.Setenv("NANO_SOURCE_PROCESSING_CONCURRENCY", "4")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted more than ten interactive jobs")
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
