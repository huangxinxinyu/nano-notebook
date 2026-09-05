package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceadmission"
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
	if config.WebReaderURL != "http://127.0.0.1:8085" {
		t.Fatalf("Web Reader URL default=%q", config.WebReaderURL)
	}
	if config.SourceMapParserURL != "http://127.0.0.1:8086" || config.SourceMapParserTimeout != 120*time.Second ||
		config.SourceMapParserServiceToken != "nano-local-source-map-parser-token" {
		t.Fatalf("Source Map parser defaults=%q/%s/%q", config.SourceMapParserURL, config.SourceMapParserTimeout, config.SourceMapParserServiceToken)
	}
	if config.AgentRelease.String() != "nano.default@26" {
		t.Fatalf("Agent release default=%q", config.AgentRelease)
	}
	if config.ToolResultRedisURL != "redis://:nano-tool-results@127.0.0.1:56379/0" || config.ToolResultKeyPrefix != "nano:tool-result:v2:" ||
		config.ToolResultCacheTTL != 30*time.Minute || config.ToolResultInlineBytes != 50*1024 ||
		config.ToolResultPageBytes != 50*1024 || config.ToolResultMaximumBytes != 2*1024*1024 ||
		config.ToolResultOperationTimeout != 750*time.Millisecond {
		t.Fatalf("Tool Result cache defaults=%#v", config)
	}
}

func TestLoadWorkerConfigDefaultsKafkaTraceProducer(t *testing.T) {
	t.Setenv("NANO_AGENT_TRACE_KAFKA_BROKERS", "")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_TOPIC", "")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_CLIENT_ID", "")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_PURGE_TOPIC", "")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_PURGE_CLIENT_ID", "")

	config, err := loadWorkerConfig()
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	if len(config.TraceKafkaBrokers) != 1 || config.TraceKafkaBrokers[0] != "127.0.0.1:59092" ||
		config.TraceKafkaTopic != "nano.observability.agent-trace.v1" || config.TraceKafkaClientID != "nano-worker-agent-trace" ||
		config.TraceKafkaPurgeTopic != "nano.observability.agent-trace-purge.v1" || config.TraceKafkaPurgeClientID != "nano-worker-agent-trace-purge" {
		t.Fatalf("Agent Trace Kafka defaults = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsMissingKafkaTraceConfig(t *testing.T) {
	t.Setenv("NANO_AGENT_TRACE_KAFKA_TOPIC", " ")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted Kafka without a topic")
	}
}

func TestLoadWorkerConfigIgnoresRemovedTraceQueueTransportAndRetrySettings(t *testing.T) {
	t.Setenv("NANO_AGENT_TRACE_TRANSPORT", "udp")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_MAX_RETRIES", "not-a-number")
	t.Setenv("NANO_TRACE_BATCH_MAX_RECORDS", "not-a-number")
	t.Setenv("NANO_TRACE_BATCH_MAX_ENCODED_BYTES", "not-a-number")
	t.Setenv("NANO_TRACE_BATCH_MAX_DELAY", "not-a-duration")
	if _, err := loadWorkerConfig(); err != nil {
		t.Fatalf("removed Trace settings still affect Worker config: %v", err)
	}
}

func TestLoadWorkerConfigRejectsMutableAgentRelease(t *testing.T) {
	t.Setenv("NANO_AGENT_RELEASE", "nano.default@latest")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted mutable Agent release")
	}
}

func TestLoadWorkerConfigRejectsToolResultPageTooSmallForVisibleEnvelope(t *testing.T) {
	t.Setenv("NANO_TOOL_RESULT_PAGE_BYTES", "511")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted a Tool Result page budget smaller than its model-visible envelope")
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

func TestLoadWorkerConfigIncludesKafkaTraceAndPurgeSettings(t *testing.T) {
	t.Setenv("NANO_DATABASE_URL", "postgres://application")
	t.Setenv("NANO_WORKER_ADDR", ":18081")
	t.Setenv("NANO_COLLECTOR_PRODUCER_ID", "worker-a")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_BROKERS", "kafka-a:9092,kafka-b:9092")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_TOPIC", "trace-topic")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_CLIENT_ID", "trace-client")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_PURGE_TOPIC", "purge-topic")
	t.Setenv("NANO_AGENT_TRACE_KAFKA_PURGE_CLIENT_ID", "purge-client")
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
	t.Setenv("NANO_RESEARCH_WORKSPACE_S3_ENDPOINT", "workspace.internal:9000")
	t.Setenv("NANO_RESEARCH_WORKSPACE_S3_ACCESS_KEY_ID", "workspace-key")
	t.Setenv("NANO_RESEARCH_WORKSPACE_S3_SECRET_ACCESS_KEY", "workspace-secret")
	t.Setenv("NANO_RESEARCH_WORKSPACE_S3_BUCKET", "research-workspaces")
	t.Setenv("NANO_RESEARCH_WORKSPACE_S3_REGION", "cn-test-3")
	t.Setenv("NANO_RESEARCH_WORKSPACE_S3_USE_TLS", "true")
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
	t.Setenv("NANO_SOURCE_VISION_PROMPT_VERSION", models.ImageEvidenceNormalizerPromptVersion)
	t.Setenv("NANO_SOURCE_MAX_VISION_PAGES", "12")
	t.Setenv("NANO_DOCUMENT_RENDERER_URL", "http://renderer.internal:8084/")
	t.Setenv("NANO_DOCUMENT_RENDERER_SERVICE_TOKEN", "renderer-secret")
	t.Setenv("NANO_DOCUMENT_RENDER_CONFIG_ID", "pdfium-lo-v7")
	t.Setenv("NANO_DOCUMENT_RENDER_TIMEOUT", "70s")
	t.Setenv("NANO_DOCUMENT_RENDER_MAX_PAGES", "25")
	t.Setenv("NANO_DOCUMENT_RENDER_DPI", "144")
	t.Setenv("NANO_DOCUMENT_RENDER_MAX_PIXELS_PER_PAGE", "3000000")
	t.Setenv("NANO_DOCUMENT_RENDER_MAX_OUTPUT_BYTES", "4194304")
	t.Setenv("NANO_SOURCE_MAP_PARSER_URL", "http://source-map-parser.internal:8086/")
	t.Setenv("NANO_SOURCE_MAP_PARSER_SERVICE_TOKEN", "source-map-parser-secret")
	t.Setenv("NANO_SOURCE_MAP_PARSER_TIMEOUT", "95s")
	t.Setenv("NANO_SOURCE_PROCESSING_MAX_BYTES", "1048576")
	t.Setenv("NANO_SOURCE_PROCESSING_MAX_RUNES", "200000")
	t.Setenv("NANO_SOURCE_ADMISSION_MODE", "enforcement")
	t.Setenv("NANO_SOURCE_ADMISSION_QUERY_TIMEOUT", "3s")
	t.Setenv("NANO_BRAVE_SEARCH_API_KEY", "brave-search-secret")
	t.Setenv("NANO_SOURCE_DISCOVERY_LEASE_DURATION", "35s")
	t.Setenv("NANO_SOURCE_DISCOVERY_POLL_INTERVAL", "300ms")
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
	if config.ProducerID != "worker-a" || len(config.TraceKafkaBrokers) != 2 || config.TraceKafkaTopic != "trace-topic" ||
		config.TraceKafkaClientID != "trace-client" || config.TraceKafkaPurgeTopic != "purge-topic" ||
		config.TraceKafkaPurgeClientID != "purge-client" || config.PurgeMaxCommands != 8 {
		t.Fatalf("Trace producer config = %#v", config)
	}
	if config.PurgeLeaseDuration != 20*time.Second || config.PurgePollInterval != 125*time.Millisecond || config.HTTPTimeout != 7*time.Second {
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
	if config.ResearchWorkspaceS3.Endpoint != "workspace.internal:9000" || config.ResearchWorkspaceS3.AccessKeyID != "workspace-key" ||
		config.ResearchWorkspaceS3.SecretAccessKey != "workspace-secret" || config.ResearchWorkspaceS3.Bucket != "research-workspaces" ||
		config.ResearchWorkspaceS3.Region != "cn-test-3" || !config.ResearchWorkspaceS3.UseTLS {
		t.Fatalf("Research workspace config = %#v", config.ResearchWorkspaceS3)
	}
	if config.QdrantURL != "http://qdrant.internal:6333" || config.QdrantAPIKey != "qdrant-secret" ||
		config.QdrantCollection != "source-evidence" || config.QdrantDenseDimensions != 768 ||
		config.SourceProcessingLease != 45*time.Second || config.SourceProcessingHeartbeat != 10*time.Second ||
		config.SourceProcessingPoll != 250*time.Millisecond || config.SourceExtractionConfigID != "extract-text-v1" ||
		config.SourceVisionModel != "gemini/gemini-2.5-flash" || config.SourceTranscriptionModel != "openai/whisper-1" ||
		config.SourceVisionPromptVersion != models.ImageEvidenceNormalizerPromptVersion || config.SourceMaxVisionPages != 12 ||
		config.DocumentRendererURL != "http://renderer.internal:8084" || config.DocumentRendererServiceToken != "renderer-secret" ||
		config.DocumentRenderConfigID != "pdfium-lo-v7" || config.DocumentRenderTimeout != 70*time.Second ||
		config.DocumentRenderMaxPages != 25 || config.DocumentRenderDPI != 144 || config.DocumentRenderMaxPixelsPerPage != 3_000_000 ||
		config.DocumentRenderMaxOutputBytes != 4<<20 ||
		config.SourceMapParserURL != "http://source-map-parser.internal:8086" || config.SourceMapParserServiceToken != "source-map-parser-secret" ||
		config.SourceMapParserTimeout != 95*time.Second ||
		config.SourceProcessingMaxBytes != 1048576 || config.SourceProcessingMaxRunes != 200000 ||
		config.SourceAdmissionMode != sourceadmission.ModeEnforcement || config.SourceAdmissionQueryTimeout != 3*time.Second ||
		config.AgentInteractiveConcurrency != 6 || config.SourceProcessingConcurrency != 4 ||
		config.BraveSearchAPIKey != "brave-search-secret" || config.SourceDiscoveryLease != 35*time.Second ||
		config.SourceDiscoveryPoll != 300*time.Millisecond {
		t.Fatalf("Source processing config = %#v", config)
	}
	if config.MailSMTPAddr != "mailpit.internal:1025" || config.MailFrom != "nano@example.test" ||
		config.WebBaseURL != "http://web.internal:5173" || config.MailLeaseDuration != 25*time.Second ||
		config.MailPollInterval != 175*time.Millisecond || config.MailSMTPTimeout != 4*time.Second {
		t.Fatalf("mail config = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsInvalidSourceAdmissionMode(t *testing.T) {
	t.Setenv("NANO_SOURCE_ADMISSION_MODE", "truth-gate")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted an invalid Source Admission mode")
	}
}

func TestLoadWorkerConfigRejectsWorkloadCapacityAboveTenInteractiveJobs(t *testing.T) {
	t.Setenv("NANO_AGENT_INTERACTIVE_CONCURRENCY", "7")
	t.Setenv("NANO_SOURCE_PROCESSING_CONCURRENCY", "4")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("loadWorkerConfig accepted more than ten interactive jobs")
	}
}

func TestRetryUntilReadySucceedsAfterTransientFailures(t *testing.T) {
	attempts := 0
	err := retryUntilReady(context.Background(), "test check", func() error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready yet")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRetryUntilReadyReturnsLastErrorAfterDeadline(t *testing.T) {
	wantErr := errors.New("still not ready")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := retryUntilReady(ctx, "test check", func() error { return wantErr })
	if !errors.Is(err, wantErr) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}
