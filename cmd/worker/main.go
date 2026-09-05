package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/otelbridge"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/documentreading"
	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/mailoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/metrics"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/huangxinxinyu/nano-notebook/internal/researchsource"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/skillcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceadmission"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcediscovery"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprojection"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcepurge"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
	"github.com/huangxinxinyu/nano-notebook/internal/websearch"
	agentworker "github.com/huangxinxinyu/nano-notebook/internal/worker"
	"github.com/huangxinxinyu/nano-notebook/internal/workload"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

type workerConfig struct {
	DatabaseURL                    string
	AgentConfigurationID           string
	AgentRelease                   agentcatalog.Reference
	LeaderModel                    string
	ResearchModel                  string
	Addr                           string
	ProducerID                     string
	TraceKafkaBrokers              []string
	TraceKafkaTopic                string
	TraceKafkaClientID             string
	TraceKafkaPurgeTopic           string
	TraceKafkaPurgeClientID        string
	HTTPTimeout                    time.Duration
	PurgeMaxCommands               int
	PurgeLeaseDuration             time.Duration
	PurgePollInterval              time.Duration
	PurgeBaseBackoff               time.Duration
	PurgeMaxBackoff                time.Duration
	ReplayStagingS3                objectstore.S3Config
	SourceS3                       objectstore.S3Config
	ResearchWorkspaceS3            objectstore.S3Config
	ToolResultRedisURL             string
	ToolResultKeyPrefix            string
	ToolResultCacheTTL             time.Duration
	ToolResultInlineBytes          int
	ToolResultPageBytes            int
	ToolResultMaximumBytes         int
	ToolResultOperationTimeout     time.Duration
	SourcePurgeLease               time.Duration
	SourcePurgePoll                time.Duration
	QdrantURL                      string
	QdrantAPIKey                   string
	QdrantCollection               string
	QdrantDenseDimensions          int
	RetrievalBootstrapMode         string
	RetrievalBootstrapConfigPath   string
	SourceProcessingLease          time.Duration
	SourceProcessingHeartbeat      time.Duration
	SourceProcessingPoll           time.Duration
	SourceExtractionConfigID       string
	SourceVisionModel              string
	SourceTranscriptionModel       string
	SourceVisionPromptVersion      string
	SourceMaxVisionPages           int
	DocumentRendererURL            string
	DocumentRendererServiceToken   string
	DocumentRenderConfigID         string
	DocumentRenderTimeout          time.Duration
	DocumentRenderMaxPages         int
	DocumentRenderDPI              int
	DocumentRenderMaxPixelsPerPage int64
	DocumentRenderMaxOutputBytes   int64
	SourceMapParserURL             string
	SourceMapParserServiceToken    string
	SourceMapParserTimeout         time.Duration
	WebReaderURL                   string
	WebReaderServiceToken          string
	WebReaderTimeout               time.Duration
	SourceProcessingMaxBytes       int64
	SourceProcessingMaxRunes       int
	SourceAdmissionMode            sourceadmission.Mode
	SourceAdmissionQueryTimeout    time.Duration
	BraveSearchAPIKey              string
	SourceDiscoveryLease           time.Duration
	SourceDiscoveryPoll            time.Duration
	AgentInteractiveConcurrency    int
	SourceProcessingConcurrency    int
	ReplayKeyID                    string
	ReplayKEK                      []byte
	MailSMTPAddr                   string
	MailFrom                       string
	WebBaseURL                     string
	MailLeaseDuration              time.Duration
	MailPollInterval               time.Duration
	MailSMTPTimeout                time.Duration
}

type purgeOutboxSender interface {
	Run(context.Context, time.Duration) error
	ForceFlush(context.Context) error
}

type notConfiguredWebSearchProvider struct{}

func (notConfiguredWebSearchProvider) ResearchAvailable() bool { return false }

func (notConfiguredWebSearchProvider) Search(context.Context, websearch.Request) ([]websearch.Candidate, error) {
	return nil, websearch.ErrNotConfigured
}

const (
	developmentBaselineVersionID   = "riv_dev_baseline_v1"
	developmentBootstrapProvenance = "dev-bootstrap-v1"
)

type retrievalAuthority interface {
	BootstrapDevelopment(context.Context, string, string, retrieval.IndexConfig) (retrieval.IndexVersion, bool, error)
	RequireActive(context.Context) error
}

// retryUntilReady retries check against a bounded window (30s, 1s between
// attempts) to absorb the startup race against control-plane's migrations
// (see the call sites in main). Returns the last error if the window
// elapses without success.
func retryUntilReady(ctx context.Context, label string, check func() error) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for attempt := 1; ; attempt++ {
		lastErr = check()
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		slog.Warn(label+" not yet ready, retrying", "attempt", attempt, "error", lastErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := loadWorkerConfig()
	if err != nil {
		slog.Error("worker configuration invalid", "error", err)
		os.Exit(1)
	}
	db, err := app.OpenDB(ctx, config.DatabaseURL)
	if err != nil {
		slog.Error("worker database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	// control-plane runs RunMigrations (which registers the embedded catalog)
	// on its own startup path, with no compose-level dependency ordering
	// worker on it — the two containers start concurrently once postgres is
	// healthy. Retry these readiness checks for a bounded window instead of
	// failing on the first race, rather than relying on the container
	// restart policy to paper over a startup-ordering race.
	if err := retryUntilReady(ctx, "worker Prompt Catalog readiness", func() error {
		return app.VerifyEmbeddedPromptCatalog(ctx, db)
	}); err != nil {
		slog.Error("worker Prompt Catalog readiness failed", "error", err)
		os.Exit(1)
	}
	definitionCatalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		slog.Error("worker Agent Catalog invalid", "error", err)
		os.Exit(1)
	}
	promptCatalog, err := promptcatalog.LoadEmbedded()
	if err != nil {
		slog.Error("worker Prompt Catalog invalid", "error", err)
		os.Exit(1)
	}
	skillCatalog, err := skillcatalog.LoadEmbedded()
	if err != nil {
		slog.Error("worker Skill Catalog invalid", "error", err)
		os.Exit(1)
	}
	if err := retryUntilReady(ctx, "worker Skill Catalog readiness", func() error {
		return app.VerifySkillCatalogReady(ctx, db, skillCatalog)
	}); err != nil {
		slog.Error("worker Skill Catalog readiness failed", "error", err)
		os.Exit(1)
	}
	var activeRelease agentcatalog.ReleaseManifest
	if err := retryUntilReady(ctx, "worker Agent Catalog readiness", func() error {
		var readyErr error
		activeRelease, readyErr = app.VerifyAgentCatalogReady(ctx, db, definitionCatalog, config.AgentRelease)
		return readyErr
	}); err != nil {
		slog.Error("worker Agent Catalog readiness failed", "release", config.AgentRelease, "error", err)
		os.Exit(1)
	}
	chatRoot, ok := activeRelease.Roots["chat"]
	if !ok {
		slog.Error("worker Agent Catalog release has no Chat root", "release", config.AgentRelease)
		os.Exit(1)
	}
	chatDefinition, ok := definitionCatalog.ResolveDefinition(chatRoot)
	if !ok || len(chatDefinition.Children) > 1 {
		slog.Error("worker Chat root has invalid configured child topology", "definition", chatRoot)
		os.Exit(1)
	}
	researchChild := agentcatalog.MustParseReference("research.source-discovery@2")
	if len(chatDefinition.Children) == 1 {
		researchChild = chatDefinition.Children[0]
	}
	researchDefinition, ok := definitionCatalog.ResolveDefinition(researchChild)
	if !ok {
		slog.Error("worker Research child Definition is missing", "definition", researchChild)
		os.Exit(1)
	}
	researchResultContract, ok := definitionCatalog.ResolveContract(researchDefinition.Contracts.Result)
	if !ok {
		slog.Error("worker Research child Result Contract is missing", "contract", researchDefinition.Contracts.Result)
		os.Exit(1)
	}
	researchPlannerRoot, ok := activeRelease.Roots["research_planner"]
	if !ok {
		slog.Error("worker Agent Catalog release has no Research Planner root", "release", config.AgentRelease)
		os.Exit(1)
	}
	deepResearchRoot, ok := activeRelease.Roots["research"]
	if !ok {
		slog.Error("worker Agent Catalog release has no Research root", "release", config.AgentRelease)
		os.Exit(1)
	}
	_, supportedAgentConfiguration, err := agent.DefaultAgentConfigurationBundle(
		config.AgentConfigurationID, config.LeaderModel, config.ResearchModel, agent.DefaultRunConfig(config.AgentConfigurationID),
	)
	if err != nil {
		slog.Error("worker Agent Configuration readiness failed", "error", err)
		os.Exit(1)
	}
	if err := app.VerifyAgentConfigurationReady(ctx, db, supportedAgentConfiguration); err != nil {
		slog.Error("worker Agent Configuration readiness failed", "error", err)
		os.Exit(1)
	}
	indexVersion, bootstrapped, err := prepareRetrievalAuthority(ctx, retrieval.NewVersionStore(db.Pool()), config)
	if err != nil {
		slog.Error("worker retrieval authority unavailable", "mode", config.RetrievalBootstrapMode, "error", err)
		os.Exit(1)
	}
	if bootstrapped {
		slog.Info("development Retrieval Index baseline activated", "index_version_id", indexVersion.ID)
	}
	shutdownTelemetry, err := telemetry.Start(ctx, "nano-worker")
	if err != nil {
		slog.Error("worker telemetry unavailable", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()
	telemetry.StartupSpan(ctx, "nano-worker")

	metricsRegistry := metrics.NewRegistry()
	metricsCatalog := metrics.NewCatalog(metricsRegistry)
	taskMetrics := agent.NewTaskMetricsRecorder(metricsCatalog, config.LeaderModel, config.ResearchModel, config.SourceVisionModel, config.SourceTranscriptionModel)
	metricsAddr := env("NANO_WORKER_METRICS_ADDR", "0.0.0.0:9092")
	metricsServer := metrics.NewAdminServer(metricsAddr, metricsRegistry)
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker metrics listener failed", "error", err)
		}
	}()
	go metrics.ObservePoolStats(ctx, metricsCatalog, "worker", 15*time.Second, func() metrics.PoolStat {
		stat := db.Pool().Stat()
		return metrics.PoolStat{
			AcquiredConns: stat.AcquiredConns(), IdleConns: stat.IdleConns(),
			TotalConns: stat.TotalConns(), MaxConns: stat.MaxConns(),
		}
	})

	modelClient := models.NewBifrostClient(env("NANO_BIFROST_URL", "http://127.0.0.1:56666"), &http.Client{}, 32*1024)
	toolResultStore, err := agent.NewRedisToolResultStore(agent.RedisToolResultStoreConfig{
		URL: config.ToolResultRedisURL, KeyPrefix: config.ToolResultKeyPrefix,
		OperationTimeout: config.ToolResultOperationTimeout, MaximumValueBytes: config.ToolResultMaximumBytes,
	})
	if err != nil {
		slog.Error("Tool Result cache configuration invalid", "error", err)
		os.Exit(1)
	}
	toolResultStore.WithMetrics(agent.NewToolResultCacheMetrics(metricsCatalog))
	defer toolResultStore.Close()
	if err := toolResultStore.CheckReady(ctx); err != nil {
		slog.Warn("Tool Result cache unavailable; long read-only results will degrade to not_cached previews", "error", err)
	}
	go toolResultStore.ObserveRedisEvictions(ctx, 15*time.Second)
	toolResultExternalizer := &agent.ToolResultExternalizer{
		Store:  toolResultStore,
		Policy: agent.ToolResultCachePolicy{TTL: config.ToolResultCacheTTL, MaximumValueBytes: config.ToolResultMaximumBytes},
	}
	traceBridge, err := otelbridge.New(otel.Tracer("nano-agent-observability"))
	if err != nil {
		slog.Error("Agent Trace telemetry bridge unavailable", "error", err)
		os.Exit(1)
	}
	defer traceBridge.Shutdown(context.Background())
	stagingObjects, err := objectstore.NewS3Store(config.ReplayStagingS3)
	if err != nil {
		slog.Error("Replay staging object Store invalid", "error", err)
		os.Exit(1)
	}
	if err := stagingObjects.CheckReady(ctx); err != nil {
		slog.Error("Replay staging object Store unavailable", "error", err)
		os.Exit(1)
	}
	sourceObjects, err := objectstore.NewS3Store(config.SourceS3)
	if err != nil {
		slog.Error("Source object Store invalid", "error", err)
		os.Exit(1)
	}
	if err := sourceObjects.CheckReady(ctx); err != nil {
		slog.Error("Source object Store unavailable", "error", err)
		os.Exit(1)
	}
	workspaceObjects, err := objectstore.NewS3Store(config.ResearchWorkspaceS3)
	if err != nil {
		slog.Error("Research workspace object Store invalid", "error", err)
		os.Exit(1)
	}
	if err := workspaceObjects.CheckReady(ctx); err != nil {
		slog.Error("Research workspace object Store unavailable", "error", err)
		os.Exit(1)
	}
	qdrant, err := qdrantstore.New(qdrantstore.Config{
		BaseURL: config.QdrantURL, APIKey: config.QdrantAPIKey, Collection: config.QdrantCollection,
		DenseDimensions: config.QdrantDenseDimensions, RequestTimeout: config.HTTPTimeout,
		HTTPClient: &http.Client{Timeout: config.HTTPTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("Qdrant projection Store invalid", "error", err)
		os.Exit(1)
	}
	if err := qdrant.EnsureCollection(ctx); err != nil {
		slog.Error("Qdrant projection Store unavailable", "error", err)
		os.Exit(1)
	}
	keyProvider, err := replay.NewDevelopmentKeyProvider(config.ReplayKeyID, config.ReplayKEK)
	if err != nil {
		slog.Error("Replay key provider invalid", "error", err)
		os.Exit(1)
	}
	sealer, err := replay.NewSealer(keyProvider)
	if err != nil {
		slog.Error("Replay envelope encryption invalid", "error", err)
		os.Exit(1)
	}
	replayStager, err := replay.NewObjectStager(sealer, stagingObjects, replay.StagerConfig{})
	if err != nil {
		slog.Error("Replay Stager invalid", "error", err)
		os.Exit(1)
	}
	traceSink, err := agentbatch.NewManagedKafkaTraceSink(ctx, agentbatch.ManagedKafkaTraceSinkConfig{
		ProducerID: config.ProducerID, Brokers: config.TraceKafkaBrokers,
		Topic: config.TraceKafkaTopic, ClientID: config.TraceKafkaClientID,
		MaxBufferedRecords: agentbatch.DefaultKafkaTraceMaxBufferedRecords,
		MaxBufferedBytes:   agentbatch.DefaultKafkaTraceMaxBufferedBytes,
		DeliveryTimeout:    agentbatch.DefaultKafkaTraceDeliveryTimeout,
		Linger:             agentbatch.DefaultKafkaTraceLinger, ReadinessTimeout: agentbatch.DefaultKafkaTraceReadinessTimeout,
		MaxMessageBytes: agentbatch.DefaultKafkaTraceMaxMessageBytes, Observer: metricsCatalog,
	})
	if err != nil {
		slog.Error("Agent Trace Kafka Sink unavailable", "error", err)
		os.Exit(1)
	}
	purgePostgres, err := agentoutbox.NewPurgeStore(db.Pool(), agentoutbox.PurgeStoreConfig{
		ProducerID: config.ProducerID, MaxCommands: config.PurgeMaxCommands,
		LeaseDuration: config.PurgeLeaseDuration,
		BaseBackoff:   config.PurgeBaseBackoff, MaxBackoff: config.PurgeMaxBackoff,
		StagingObjects: stagingObjects,
	})
	if err != nil {
		slog.Error("Agent Trace purge Store invalid", "error", err)
		os.Exit(1)
	}
	var purgeSender purgeOutboxSender
	var purgeKafkaProducer *agentbatch.FranzKafkaProducer
	reportPurgeError := func(err error) {
		slog.Error("Agent Trace purge delivery failed; durable command retained", "error", err)
	}
	purgeKafkaProducer, err = agentbatch.NewFranzKafkaProducer(agentbatch.FranzKafkaConfig{
		Brokers: config.TraceKafkaBrokers, ClientID: config.TraceKafkaPurgeClientID,
		MaxBufferedRecords: 1_000, MaxBufferedBytes: 8 * 1024 * 1024,
		DeliveryTimeout: 10 * time.Second, Linger: 5 * time.Millisecond,
	})
	if err == nil {
		readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = purgeKafkaProducer.Ping(readyCtx)
		cancel()
	}
	if err == nil {
		purgeSender, err = agentoutbox.NewKafkaPurgeSender(purgePostgres, agentoutbox.KafkaPurgeSenderConfig{
			Topic: config.TraceKafkaPurgeTopic, Producer: purgeKafkaProducer, ReportError: reportPurgeError,
		})
	}
	if err != nil {
		if purgeKafkaProducer != nil {
			purgeKafkaProducer.Close()
		}
		slog.Error("Agent Trace purge Kafka Sender invalid", "error", err)
		os.Exit(1)
	}
	var searchProvider websearch.Provider = notConfiguredWebSearchProvider{}
	if config.BraveSearchAPIKey != "" {
		searchProvider, err = websearch.NewBraveProvider(websearch.BraveConfig{
			APIKey:     config.BraveSearchAPIKey,
			HTTPClient: &http.Client{Timeout: config.HTTPTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		})
		if err != nil {
			slog.Error("Brave Web Search Provider invalid", "error", err)
			os.Exit(1)
		}
	}
	webReaderAdapter, err := webreader.NewHTTPAdapter(webreader.HTTPConfig{
		Endpoint: config.WebReaderURL, ServiceToken: config.WebReaderServiceToken,
		HTTPClient: &http.Client{Timeout: config.WebReaderTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("web reader Adapter invalid", "error", err)
		os.Exit(1)
	}
	documentRenderer, err := documentrender.NewHTTPAdapter(documentrender.HTTPConfig{
		Endpoint: config.DocumentRendererURL, ServiceToken: config.DocumentRendererServiceToken,
		HTTPClient: &http.Client{Timeout: config.DocumentRenderTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("document renderer Adapter invalid", "error", err)
		os.Exit(1)
	}
	sourceMapParser, err := sourcemap.NewHTTPAdapter(sourcemap.HTTPConfig{
		Endpoint: config.SourceMapParserURL, ServiceToken: config.SourceMapParserServiceToken,
		HTTPClient: &http.Client{Timeout: config.SourceMapParserTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("Source Map parser Adapter invalid", "error", err)
		os.Exit(1)
	}
	researchPDFExtractor := documentreading.NewPDFExtractor(modelClient, documentreading.PDFExtractorConfig{
		VisionModel: config.SourceVisionModel, VisionPromptVersion: config.SourceVisionPromptVersion,
		MaxVisionPages: config.SourceMaxVisionPages,
	})
	researchURLReader := agent.NewResearchURLContentReader(
		webReaderAdapter, documentRenderer, researchPDFExtractor, workspaceObjects,
		agent.ResearchURLReaderConfig{
			ExtractionConfigID: "research-pdf-v1",
			RenderConfigID:     config.DocumentRenderConfigID, RenderMaxPages: config.DocumentRenderMaxPages,
			RenderDPI: config.DocumentRenderDPI, RenderMaxPixelsPerPage: config.DocumentRenderMaxPixelsPerPage,
			RenderMaxOutputBytes: config.DocumentRenderMaxOutputBytes,
			MaxNormalizedRunes:   config.SourceProcessingMaxRunes, MaxModelChars: agent.ResearchReadURLMaxChars,
			MaxPageRead: 20, MaxPDFConcurrent: 2, ReadTimeout: 120 * time.Second,
		},
	)
	grounder := agent.NewGroundingService(db.Pool())
	runtime := agent.NewPostgresRuntime(db.Pool(), agent.BareSystemPrompt, nil,
		agent.WithTraceSink(traceSink), agent.WithBestEffortTraceExporter(traceBridge),
		agent.WithReplayStager(replayStager), agent.WithGroundingService(grounder), agent.WithTaskMetrics(taskMetrics))
	evidenceSearch := agent.NewEvidenceSearchService(db.Pool(), qdrant, modelClient).
		WithSourceMapObjects(sourceObjects).
		WithMetrics(taskMetrics)
	candidateValidator := sourcediscovery.NewImportabilityValidator(webReaderAdapter, sourcediscovery.ImportabilityValidatorConfig{
		ExtractionConfigID: config.SourceExtractionConfigID,
		MaxBytes:           config.SourceProcessingMaxBytes, MaxNormalizedRunes: config.SourceProcessingMaxRunes,
	})
	calculateTool := agent.NewCalculateAction()
	currentTimeTool := agent.NewCurrentTimeAction(nil)
	rewriteTodoListTool := agent.NewRewriteTodoListAction(runtime)
	updateTodoStatusTool := agent.NewUpdateTodoStatusAction(runtime)
	searchEvidenceTool := agent.NewSearchEvidenceAction(evidenceSearch)
	discoverSourcesTool := agent.NewDiscoverSourcesAction(
		agent.NewPostgresDiscoverSourcesBackend(db.Pool(), searchProvider),
		agent.ResearchAvailabilityFrom(searchProvider),
	)
	inspectSourceTool := agent.NewInspectSourceAction(agent.NewSourceInspectionService(db.Pool(), sourceObjects))
	webSearchTool := agent.NewResearchDeduplicatingAction(db.Pool(), agent.NewWebSearchAction(searchProvider))
	readSkillTool := agent.NewReadSkillAction(definitionCatalog, skillCatalog)
	toolResultReader := agent.ToolResultReader{
		Store: toolResultStore, MaximumPageBytes: config.ToolResultPageBytes,
		MaximumOutputBytes: config.ToolResultPageBytes,
	}
	readToolResultTool := agent.NewReadToolResultAction(toolResultReader)
	researchURLTools := agent.NewVersionedResearchURLActions(researchURLReader, webReaderAdapter, webReaderAdapter)
	readURLTool := agent.NewResearchDeduplicatingAction(db.Pool(), researchURLTools[0])
	readDocumentPagesTool := researchURLTools[1]
	saveURLAsSourceTool := agent.NewSaveURLAsSourceAction(researchsource.NewService(db.Pool(), webReaderAdapter, sourceObjects))
	workspaceTools, err := agent.NewResearchWorkspaceActions(db.Pool(), workspaceObjects)
	if err != nil {
		slog.Error("Research workspace Tools invalid", "error", err)
		os.Exit(1)
	}
	registryTools := []agent.Action{
		calculateTool, currentTimeTool, discoverSourcesTool, rewriteTodoListTool, inspectSourceTool, searchEvidenceTool, updateTodoStatusTool,
		webSearchTool, readSkillTool, readToolResultTool, readURLTool, readDocumentPagesTool, saveURLAsSourceTool,
	}
	registryTools = append(registryTools, workspaceTools...)
	registry, err := agent.NewActionRegistry(registryTools...)
	if err != nil {
		slog.Error("worker Action registry invalid", "error", err)
		os.Exit(1)
	}
	mcpToolRegistrations := []agent.MCPToolRegistration{
		agent.MCPToolRegistration{Action: calculateTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: currentTimeTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: discoverSourcesTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: rewriteTodoListTool, Scheduling: agentcatalog.ToolOrderedSync, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: inspectSourceTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: searchEvidenceTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: updateTodoStatusTool, Scheduling: agentcatalog.ToolOrderedSync, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: webSearchTool, Scheduling: agentcatalog.ToolOrderedSync, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: readSkillTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: readToolResultTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: readURLTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: readDocumentPagesTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		agent.MCPToolRegistration{Action: saveURLAsSourceTool, Scheduling: agentcatalog.ToolOrderedSync, CrashReplaySafe: true},
	}
	for _, workspaceTool := range workspaceTools {
		scheduling := agentcatalog.ToolParallel
		if workspaceTool.Definition().Name == "write_research_file" || workspaceTool.Definition().Name == "assemble_research_report" {
			scheduling = agentcatalog.ToolOrderedSync
		}
		mcpToolRegistrations = append(mcpToolRegistrations, agent.MCPToolRegistration{
			Action: workspaceTool, Scheduling: scheduling, CrashReplaySafe: true,
		})
	}
	configuredDelegationTools, err := agent.NewConfiguredDelegationToolRegistrations(definitionCatalog, db.Pool(), agent.ResearchAvailabilityFrom(searchProvider), traceSink)
	if err != nil {
		slog.Error("configured Delegation Tools invalid", "error", err)
		os.Exit(1)
	}
	mcpToolRegistrations = append(mcpToolRegistrations, configuredDelegationTools...)
	mcpToolRegistry, err := agent.NewMCPToolRegistry(mcpToolRegistrations...)
	if err != nil {
		slog.Error("worker MCP Tool Registry invalid", "error", err)
		os.Exit(1)
	}
	mcpToolHost, err := agent.NewMCPToolHost(definitionCatalog, mcpToolRegistry, runtime)
	if err != nil {
		slog.Error("worker MCP Tool Host invalid", "error", err)
		os.Exit(1)
	}
	mcpToolHost.WithMetrics(taskMetrics)
	controller := agent.NewMCPController(runtime, modelClient, registry, mcpToolHost, chatRoot).
		WithControllerMetrics(taskMetrics).WithToolResultCache(toolResultExternalizer, config.ToolResultInlineBytes)
	researchPlanningRuntime, err := agent.NewResearchPlanningRuntime(db.Pool(), promptCatalog, skillCatalog)
	if err != nil {
		slog.Error("Research Planning Runtime invalid", "error", err)
		os.Exit(1)
	}
	researchRuntime, err := agent.NewResearchRuntime(db.Pool(), promptCatalog, workspaceObjects)
	if err != nil {
		slog.Error("Research Runtime invalid", "error", err)
		os.Exit(1)
	}
	researchRuntime.WithToolResultReader(toolResultReader)
	researchPlanningController := agent.NewMCPController(
		researchPlanningRuntime, modelClient, registry, mcpToolHost, researchPlannerRoot,
	).WithControllerMetrics(taskMetrics).WithToolResultCache(toolResultExternalizer, config.ToolResultInlineBytes)
	researchController := agent.NewMCPController(
		researchRuntime, modelClient, registry, mcpToolHost, deepResearchRoot,
	).WithControllerMetrics(taskMetrics).WithToolResultCache(toolResultExternalizer, config.ToolResultInlineBytes)
	studioExecutor, err := agent.NewStudioDefinitionExecutor(db.Pool(), runtime, modelClient, registry, mcpToolHost, definitionCatalog, taskMetrics)
	if err != nil {
		slog.Error("Studio Executor invalid", "error", err)
		os.Exit(1)
	}
	sourceExtractor := sourceprocessing.NewNativeExtractorWithWebReader(modelClient, webReaderAdapter, sourceprocessing.NativeExtractorConfig{
		VisionModel: config.SourceVisionModel, TranscriptionModel: config.SourceTranscriptionModel,
		VisionPromptVersion: config.SourceVisionPromptVersion, MaxVisionPages: config.SourceMaxVisionPages,
	})
	roleRuntime := agent.NewLeaderExecutor(
		db.Pool(), controller, agent.NewModelResearchPlanner(modelClient), searchProvider,
		agent.WithLeaderTraceSink(traceSink),
		agent.WithLeaderReplayStager(replayStager), agent.WithResearchCandidateValidator(candidateValidator),
		agent.WithResearchMCPToolPlane(mcpToolHost, researchChild),
		agent.WithResearchResultContract(researchResultContract),
	)
	leaderExecutor := agent.NewLeaderRoleExecutor(roleRuntime)
	researchExecutor := agent.NewResearchRoleExecutor(roleRuntime)
	configuredRegistry, err := agent.NewNanoExecutorRegistry(
		definitionCatalog, promptCatalog, skillCatalog,
		agent.NewChatLeaderDefinitionExecutor(roleRuntime), agent.NewResearchDefinitionExecutor(roleRuntime),
		agent.NewResearchPlanningDefinitionExecutor(researchPlanningController),
		agent.NewResearchRootDefinitionExecutor(researchController),
		studioExecutor,
	)
	if err != nil {
		slog.Error("Agent Executor Registry invalid", "error", err)
		os.Exit(1)
	}
	legacyRegistry, err := agent.NewRoleRegistry(
		agent.RoleRegistration{Role: agent.RoleLeader, ExecutorVersion: supportedAgentConfiguration.Profiles[agent.RoleLeader].ExecutorVersion, Executor: leaderExecutor},
		agent.RoleRegistration{Role: agent.RoleResearch, ExecutorVersion: supportedAgentConfiguration.Profiles[agent.RoleResearch].ExecutorVersion, Executor: researchExecutor},
	)
	if err != nil {
		slog.Error("Agent Role Registry invalid", "error", err)
		os.Exit(1)
	}
	executionHost, err := agent.NewAgentExecutionHost(db.Pool(), legacyRegistry, configuredRegistry)
	if err != nil {
		slog.Error("Agent Execution Host invalid", "error", err)
		os.Exit(1)
	}
	mailSender := mailoutbox.NewSender(
		mailoutbox.NewQueue(db.Pool(), config.MailLeaseDuration),
		mailoutbox.NewSMTPMailer(config.MailSMTPAddr, config.MailFrom, config.MailSMTPTimeout),
		config.WebBaseURL,
	)
	mailDone := make(chan error, 1)
	go func() { mailDone <- mailSender.Run(ctx, config.MailPollInterval) }()
	jobQueue := jobs.NewQueueWithTraceSink(db.Pool(), traceSink).WithMetrics(taskMetrics)
	workerService := agentworker.NewServiceWithConcurrency(db.Pool(), jobQueue, executionHost, 5*time.Second, 210*time.Second, config.AgentInteractiveConcurrency).WithMetrics(metricsCatalog)
	workerDone := make(chan error, 1)
	go func() {
		err := workerService.Run(ctx)
		workerDone <- err
		if err != nil && ctx.Err() == nil {
			slog.Error("agent worker failed", "error", err)
			stop()
		}
	}()
	purgeDone := make(chan error, 1)
	go func() { purgeDone <- purgeSender.Run(ctx, config.PurgePollInterval) }()
	sourcePurgeDone := make(chan error, 1)
	sourcePurgeProcessor := sourcepurge.NewProcessorWithProjectionPurger(db.Pool(), sourceObjects, qdrant, config.SourcePurgeLease)
	go func() { sourcePurgeDone <- sourcePurgeProcessor.Run(ctx, config.SourcePurgePoll) }()
	sourceQueue := sourcejobs.NewQueue(db.Pool(), config.SourceProcessingLease).WithMetrics(taskMetrics)
	sourceProcessor := sourceprocessing.NewProcessorWithExtractorTraceAndRenderer(
		db.Pool(), sourceQueue, evidence.NewPublisher(db.Pool(), sourceObjects), sourceObjects,
		sourceprojection.New(db.Pool(), qdrant, modelClient),
		sourceExtractor, documentRenderer, traceSink,
		sourceprocessing.Config{
			ExtractionConfigID: config.SourceExtractionConfigID,
			ExtractorAdapterID: "native-with-isolated-renderer",
			MaxSourceBytes:     config.SourceProcessingMaxBytes, MaxNormalizedRunes: config.SourceProcessingMaxRunes,
			RenderConfigID: config.DocumentRenderConfigID, RenderMaxPages: config.DocumentRenderMaxPages,
			RenderDPI: config.DocumentRenderDPI, RenderMaxPixelsPerPage: config.DocumentRenderMaxPixelsPerPage,
			RenderMaxOutputBytes: config.DocumentRenderMaxOutputBytes,
		},
	)
	sourceMapMaterializer, err := sourcemap.NewMaterializer(
		sourceMapParser, sourcemap.NewPostgresRepository(db.Pool()), sourceObjects,
		sourcemap.Config{MaxPages: sourcemap.MaxParserPages, MaxOutputBytes: sourcemap.MaxParserOutput},
	)
	if err != nil {
		slog.Error("Source Map Materializer invalid", "error", err)
		os.Exit(1)
	}
	sourceProcessor.WithSourceMap(sourceMapMaterializer)
	admissionProviderID := "not-configured"
	if config.BraveSearchAPIKey != "" {
		admissionProviderID = "brave"
	}
	admissionVerifierConfig := sourceadmission.DefaultVerifierConfig(admissionProviderID)
	admissionVerifierConfig.QueryTimeout = config.SourceAdmissionQueryTimeout
	admissionVerifier, err := sourceadmission.NewVerifier(searchProvider, admissionVerifierConfig)
	if err != nil {
		slog.Error("Source Admission Verifier invalid", "error", err)
		os.Exit(1)
	}
	admissionService, err := sourceadmission.NewService(
		sourceadmission.NewStore(db.Pool()), admissionVerifier, config.SourceAdmissionMode,
	)
	if err != nil {
		slog.Error("Source Admission Service invalid", "error", err)
		os.Exit(1)
	}
	sourceProcessor.WithAdmission(admissionService)
	sourceProcessingService := sourceprocessing.NewServiceWithConcurrency(
		sourceQueue, sourceProcessor, config.SourceProcessingHeartbeat, config.SourceProcessingPoll, config.SourceProcessingConcurrency,
	).WithPostgresNotifications(db.Pool())
	sourceProcessingDone := make(chan error, 1)
	go func() { sourceProcessingDone <- sourceProcessingService.Run(ctx) }()
	discoveryQueue := sourcediscovery.NewQueue(db.Pool(), config.SourceDiscoveryLease)
	discoveryProcessor := sourcediscovery.NewProcessorWithValidator(db.Pool(), discoveryQueue, searchProvider, candidateValidator)
	discoveryService := sourcediscovery.NewService(discoveryProcessor, config.SourceDiscoveryPoll)
	discoveryDone := make(chan error, 1)
	go func() { discoveryDone <- discoveryService.Run(ctx) }()

	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		writeWorkerJSON(w, http.StatusOK, `{"status":"live","service":"worker","mode":"agent"}`)
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if db.Pool().Ping(pingCtx) != nil {
			writeWorkerJSON(w, http.StatusServiceUnavailable, `{"status":"not_ready","service":"worker"}`)
			return
		}
		writeWorkerJSON(w, http.StatusOK, `{"status":"ready","service":"worker","mode":"agent"}`)
	})

	httpServer := &http.Server{Addr: config.Addr, Handler: otelhttp.NewHandler(mux, "worker"), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("worker listening", "addr", httpServer.Addr, "mode", "agent", "provider_credentials_required", true)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("worker shutdown failed", "error", err)
		os.Exit(1)
	}
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("worker metrics listener shutdown incomplete", "error", err)
	}
	select {
	case err := <-mailDone:
		if err != nil {
			slog.Error("mail Sender shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("mail Sender did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-workerDone:
		if err != nil {
			slog.Error("agent worker shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("agent worker did not release its lease before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-purgeDone:
		if err != nil {
			slog.Error("Agent Trace purge Sender shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Agent Trace purge Sender did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-sourcePurgeDone:
		if err != nil {
			slog.Error("Source purge Processor shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Source purge Processor did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-sourceProcessingDone:
		if err != nil {
			slog.Error("Source processing Service shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Source processing Service did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-discoveryDone:
		if err != nil {
			slog.Error("Source Discovery Service shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Source Discovery Service did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	if err := purgeSender.ForceFlush(shutdownCtx); err != nil {
		slog.Warn("Agent Trace purge flush incomplete; durable command remains for restart", "error", err)
	}
	if purgeKafkaProducer != nil {
		purgeKafkaProducer.Close()
	}
	if err := traceSink.Shutdown(shutdownCtx); err != nil {
		slog.Warn("Agent Trace Kafka flush incomplete; buffered records may be lost on process exit", "error", err)
	}
	slog.Info("worker stopped")
}

func prepareRetrievalAuthority(ctx context.Context, authority retrievalAuthority, config workerConfig) (retrieval.IndexVersion, bool, error) {
	if authority == nil {
		return retrieval.IndexVersion{}, false, errors.New("Retrieval authority is unavailable")
	}
	if config.RetrievalBootstrapMode == "required" {
		if err := authority.RequireActive(ctx); err != nil {
			return retrieval.IndexVersion{}, false, fmt.Errorf("active Retrieval Index Version is required: %w", err)
		}
		return retrieval.IndexVersion{}, false, nil
	}
	if err := authority.RequireActive(ctx); err == nil {
		return retrieval.IndexVersion{}, false, nil
	} else if !errors.Is(err, retrieval.ErrVersionNotFound) {
		return retrieval.IndexVersion{}, false, fmt.Errorf("check active Retrieval Index Version: %w", err)
	}
	payload, err := os.ReadFile(config.RetrievalBootstrapConfigPath)
	if err != nil {
		return retrieval.IndexVersion{}, false, fmt.Errorf("read Retrieval bootstrap config: %w", err)
	}
	var pinned struct {
		Index retrieval.IndexConfig `json:"index"`
	}
	if err := json.Unmarshal(payload, &pinned); err != nil {
		return retrieval.IndexVersion{}, false, fmt.Errorf("parse Retrieval bootstrap config: %w", err)
	}
	version, created, err := authority.BootstrapDevelopment(
		ctx, developmentBaselineVersionID, developmentBootstrapProvenance, pinned.Index,
	)
	if err != nil {
		return retrieval.IndexVersion{}, false, fmt.Errorf("bootstrap development Retrieval Index: %w", err)
	}
	return version, created, nil
}

func loadWorkerConfig() (workerConfig, error) {
	agentRelease, err := agentcatalog.ParseReference(env("NANO_AGENT_RELEASE", "nano.default@26"))
	if err != nil {
		return workerConfig{}, fmt.Errorf("parse NANO_AGENT_RELEASE: %w", err)
	}
	purgeMaxCommands, err := workerEnvInt("NANO_TRACE_PURGE_MAX_COMMANDS", 16)
	if err != nil {
		return workerConfig{}, err
	}
	purgeLeaseDuration, err := workerEnvDuration("NANO_TRACE_PURGE_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	purgePollInterval, err := workerEnvDuration("NANO_TRACE_PURGE_POLL_INTERVAL", 100*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	httpTimeout, err := workerEnvDuration("NANO_TRACE_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	purgeBaseBackoff, err := workerEnvDuration("NANO_TRACE_PURGE_BASE_BACKOFF", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	purgeMaxBackoff, err := workerEnvDuration("NANO_TRACE_PURGE_MAX_BACKOFF", time.Minute)
	if err != nil {
		return workerConfig{}, err
	}
	replayUseTLS, err := workerEnvBool("NANO_REPLAY_STAGING_S3_USE_TLS", false)
	if err != nil {
		return workerConfig{}, err
	}
	sourceUseTLS, err := workerEnvBool("NANO_SOURCE_S3_USE_TLS", false)
	if err != nil {
		return workerConfig{}, err
	}
	workspaceUseTLS, err := workerEnvBool("NANO_RESEARCH_WORKSPACE_S3_USE_TLS", false)
	if err != nil {
		return workerConfig{}, err
	}
	toolResultCacheTTL, err := workerEnvDuration("NANO_TOOL_RESULT_CACHE_TTL", 30*time.Minute)
	if err != nil {
		return workerConfig{}, err
	}
	toolResultOperationTimeout, err := workerEnvDuration("NANO_TOOL_RESULT_REDIS_OPERATION_TIMEOUT", 750*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	toolResultInlineBytes, err := workerEnvInt("NANO_TOOL_RESULT_INLINE_BYTES", 50*1024)
	if err != nil {
		return workerConfig{}, err
	}
	toolResultPageBytes, err := workerEnvInt("NANO_TOOL_RESULT_PAGE_BYTES", 50*1024)
	if err != nil {
		return workerConfig{}, err
	}
	toolResultMaximumBytes, err := workerEnvInt("NANO_TOOL_RESULT_MAXIMUM_BYTES", 2*1024*1024)
	if err != nil {
		return workerConfig{}, err
	}
	sourcePurgeLease, err := workerEnvDuration("NANO_SOURCE_PURGE_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourcePurgePoll, err := workerEnvDuration("NANO_SOURCE_PURGE_POLL_INTERVAL", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingLease, err := workerEnvDuration("NANO_SOURCE_PROCESSING_LEASE_DURATION", 2*time.Minute)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingHeartbeat, err := workerEnvDuration("NANO_SOURCE_PROCESSING_HEARTBEAT_INTERVAL", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingPoll, err := workerEnvDuration("NANO_SOURCE_PROCESSING_POLL_INTERVAL", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	qdrantDenseDimensions, err := workerEnvInt("NANO_QDRANT_DENSE_DIMENSIONS", 768)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingMaxBytes, err := workerEnvInt("NANO_SOURCE_PROCESSING_MAX_BYTES", 100*1024*1024)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingMaxRunes, err := workerEnvInt("NANO_SOURCE_PROCESSING_MAX_RUNES", 20_000_000)
	if err != nil {
		return workerConfig{}, err
	}
	sourceAdmissionQueryTimeout, err := workerEnvDuration("NANO_SOURCE_ADMISSION_QUERY_TIMEOUT", 5*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceDiscoveryLease, err := workerEnvDuration("NANO_SOURCE_DISCOVERY_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceDiscoveryPoll, err := workerEnvDuration("NANO_SOURCE_DISCOVERY_POLL_INTERVAL", 500*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	documentRenderTimeout, err := workerEnvDuration("NANO_DOCUMENT_RENDER_TIMEOUT", 100*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceMapParserTimeout, err := workerEnvDuration("NANO_SOURCE_MAP_PARSER_TIMEOUT", 120*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	webReaderTimeout, err := workerEnvDuration("NANO_WEB_READER_TIMEOUT", 90*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	documentRenderMaxPages, err := workerEnvInt("NANO_DOCUMENT_RENDER_MAX_PAGES", 500)
	if err != nil {
		return workerConfig{}, err
	}
	documentRenderDPI, err := workerEnvInt("NANO_DOCUMENT_RENDER_DPI", 144)
	if err != nil {
		return workerConfig{}, err
	}
	documentRenderMaxPixels, err := workerEnvInt("NANO_DOCUMENT_RENDER_MAX_PIXELS_PER_PAGE", 20_000_000)
	if err != nil {
		return workerConfig{}, err
	}
	documentRenderMaxOutput, err := workerEnvInt("NANO_DOCUMENT_RENDER_MAX_OUTPUT_BYTES", 256*1024*1024)
	if err != nil {
		return workerConfig{}, err
	}
	sourceMaxVisionPages, err := workerEnvInt("NANO_SOURCE_MAX_VISION_PAGES", 20)
	if err != nil {
		return workerConfig{}, err
	}
	agentInteractiveConcurrency, err := workerEnvInt("NANO_AGENT_INTERACTIVE_CONCURRENCY", workload.DefaultAgentConcurrency)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingConcurrency, err := workerEnvInt("NANO_SOURCE_PROCESSING_CONCURRENCY", workload.DefaultSourceConcurrency)
	if err != nil {
		return workerConfig{}, err
	}
	mailLeaseDuration, err := workerEnvDuration("NANO_MAIL_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	mailPollInterval, err := workerEnvDuration("NANO_MAIL_POLL_INTERVAL", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	mailSMTPTimeout, err := workerEnvDuration("NANO_MAIL_SMTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	replayKEK, err := base64.StdEncoding.DecodeString(env("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA="))
	if err != nil {
		return workerConfig{}, fmt.Errorf("parse NANO_REPLAY_KEK_BASE64: %w", err)
	}
	config := workerConfig{
		DatabaseURL:             env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable"),
		AgentConfigurationID:    env("NANO_AGENT_CONFIGURATION_ID", "nano-interactive-v1"),
		AgentRelease:            agentRelease,
		LeaderModel:             env("NANO_CHAT_MODEL", "aliyun/qwen-plus"),
		ResearchModel:           env("NANO_RESEARCH_MODEL", env("NANO_CHAT_MODEL", "aliyun/qwen-plus")),
		Addr:                    env("NANO_WORKER_ADDR", ":8081"),
		ProducerID:              env("NANO_COLLECTOR_PRODUCER_ID", "nano-worker"),
		TraceKafkaBrokers:       splitTraceKafkaBrokers(env("NANO_AGENT_TRACE_KAFKA_BROKERS", "127.0.0.1:59092")),
		TraceKafkaTopic:         env("NANO_AGENT_TRACE_KAFKA_TOPIC", "nano.observability.agent-trace.v1"),
		TraceKafkaClientID:      env("NANO_AGENT_TRACE_KAFKA_CLIENT_ID", "nano-worker-agent-trace"),
		TraceKafkaPurgeTopic:    env("NANO_AGENT_TRACE_KAFKA_PURGE_TOPIC", "nano.observability.agent-trace-purge.v1"),
		TraceKafkaPurgeClientID: env("NANO_AGENT_TRACE_KAFKA_PURGE_CLIENT_ID", "nano-worker-agent-trace-purge"),
		HTTPTimeout:             httpTimeout, PurgeMaxCommands: purgeMaxCommands,
		PurgeLeaseDuration: purgeLeaseDuration, PurgePollInterval: purgePollInterval,
		PurgeBaseBackoff: purgeBaseBackoff, PurgeMaxBackoff: purgeMaxBackoff,
		ReplayStagingS3: objectstore.S3Config{
			Endpoint:        env("NANO_REPLAY_STAGING_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_REPLAY_STAGING_S3_BUCKET", "nano-agent-replay-staging"),
			Region:          env("NANO_REPLAY_STAGING_S3_REGION", "us-east-1"), UseTLS: replayUseTLS,
		},
		SourceS3: objectstore.S3Config{
			Endpoint:        env("NANO_SOURCE_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_SOURCE_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_SOURCE_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_SOURCE_S3_BUCKET", "nano-sources"),
			Region:          env("NANO_SOURCE_S3_REGION", "us-east-1"), UseTLS: sourceUseTLS,
		},
		ResearchWorkspaceS3: objectstore.S3Config{
			Endpoint:        env("NANO_RESEARCH_WORKSPACE_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_RESEARCH_WORKSPACE_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_RESEARCH_WORKSPACE_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_RESEARCH_WORKSPACE_S3_BUCKET", "nano-research-workspaces"),
			Region:          env("NANO_RESEARCH_WORKSPACE_S3_REGION", "us-east-1"), UseTLS: workspaceUseTLS,
		},
		ToolResultRedisURL:  env("NANO_TOOL_RESULT_REDIS_URL", "redis://:nano-tool-results@127.0.0.1:56379/0"),
		ToolResultKeyPrefix: env("NANO_TOOL_RESULT_REDIS_KEY_PREFIX", "nano:tool-result:v2:"),
		ToolResultCacheTTL:  toolResultCacheTTL, ToolResultInlineBytes: toolResultInlineBytes,
		ToolResultPageBytes: toolResultPageBytes, ToolResultMaximumBytes: toolResultMaximumBytes,
		ToolResultOperationTimeout: toolResultOperationTimeout,
		SourcePurgeLease:           sourcePurgeLease, SourcePurgePoll: sourcePurgePoll,
		QdrantURL:                    env("NANO_QDRANT_URL", "http://127.0.0.1:56333"),
		QdrantAPIKey:                 strings.TrimSpace(os.Getenv("NANO_QDRANT_API_KEY")),
		QdrantCollection:             env("NANO_QDRANT_COLLECTION", "nano-source-evidence-gemini-2-768-v1"),
		QdrantDenseDimensions:        qdrantDenseDimensions,
		RetrievalBootstrapMode:       env("NANO_RETRIEVAL_BOOTSTRAP_MODE", "development"),
		RetrievalBootstrapConfigPath: env("NANO_RETRIEVAL_BOOTSTRAP_CONFIG_PATH", "evals/rag/pinned-config-v1.json"),
		SourceProcessingLease:        sourceProcessingLease, SourceProcessingHeartbeat: sourceProcessingHeartbeat,
		SourceProcessingPoll: sourceProcessingPoll, SourceExtractionConfigID: env("NANO_SOURCE_EXTRACTION_CONFIG_ID", "extract-text-v1"),
		SourceVisionModel:            env("NANO_SOURCE_VISION_MODEL", "gemini/gemini-2.5-flash"),
		SourceTranscriptionModel:     env("NANO_SOURCE_TRANSCRIPTION_MODEL", "openai/whisper-1"),
		SourceVisionPromptVersion:    env("NANO_SOURCE_VISION_PROMPT_VERSION", models.ImageEvidenceNormalizerPromptVersion),
		SourceMaxVisionPages:         sourceMaxVisionPages,
		DocumentRendererURL:          strings.TrimRight(env("NANO_DOCUMENT_RENDERER_URL", "http://127.0.0.1:8084"), "/"),
		DocumentRendererServiceToken: env("NANO_DOCUMENT_RENDERER_SERVICE_TOKEN", "nano-local-renderer-token"),
		DocumentRenderConfigID:       env("NANO_DOCUMENT_RENDER_CONFIG_ID", "pdfium-libreoffice-v1"),
		DocumentRenderTimeout:        documentRenderTimeout, DocumentRenderMaxPages: documentRenderMaxPages,
		DocumentRenderDPI: documentRenderDPI, DocumentRenderMaxPixelsPerPage: int64(documentRenderMaxPixels),
		DocumentRenderMaxOutputBytes: int64(documentRenderMaxOutput),
		SourceMapParserURL:           strings.TrimRight(env("NANO_SOURCE_MAP_PARSER_URL", "http://127.0.0.1:8086"), "/"),
		SourceMapParserServiceToken:  env("NANO_SOURCE_MAP_PARSER_SERVICE_TOKEN", "nano-local-source-map-parser-token"),
		SourceMapParserTimeout:       sourceMapParserTimeout,
		WebReaderURL:                 strings.TrimRight(env("NANO_WEB_READER_URL", "http://127.0.0.1:8085"), "/"),
		WebReaderServiceToken:        env("NANO_WEB_READER_SERVICE_TOKEN", "nano-local-reader-token"),
		WebReaderTimeout:             webReaderTimeout,
		SourceProcessingMaxBytes:     int64(sourceProcessingMaxBytes), SourceProcessingMaxRunes: sourceProcessingMaxRunes,
		SourceAdmissionMode:         sourceadmission.Mode(strings.ToLower(strings.TrimSpace(env("NANO_SOURCE_ADMISSION_MODE", "shadow")))),
		SourceAdmissionQueryTimeout: sourceAdmissionQueryTimeout,
		BraveSearchAPIKey:           strings.TrimSpace(os.Getenv("NANO_BRAVE_SEARCH_API_KEY")),
		SourceDiscoveryLease:        sourceDiscoveryLease, SourceDiscoveryPoll: sourceDiscoveryPoll,
		AgentInteractiveConcurrency: agentInteractiveConcurrency, SourceProcessingConcurrency: sourceProcessingConcurrency,
		ReplayKeyID: env("NANO_REPLAY_KEY_ID", "nano-local-replay-key-v1"), ReplayKEK: replayKEK,
		MailSMTPAddr:      env("NANO_MAIL_SMTP_ADDR", "127.0.0.1:51025"),
		MailFrom:          env("NANO_MAIL_FROM", "nano@localhost"),
		WebBaseURL:        strings.TrimRight(env("NANO_WEB_BASE_URL", "http://localhost:5173"), "/"),
		MailLeaseDuration: mailLeaseDuration, MailPollInterval: mailPollInterval, MailSMTPTimeout: mailSMTPTimeout,
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.AgentConfigurationID) == "" ||
		config.AgentRelease.Identity == "" ||
		strings.TrimSpace(config.LeaderModel) == "" || strings.TrimSpace(config.ResearchModel) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(config.ProducerID) == "" || config.HTTPTimeout <= 0 ||
		config.PurgeMaxCommands < 1 || config.PurgeLeaseDuration <= 0 || config.PurgePollInterval <= 0 ||
		config.PurgeBaseBackoff <= 0 || config.PurgeMaxBackoff < config.PurgeBaseBackoff || strings.TrimSpace(config.ReplayStagingS3.Endpoint) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.AccessKeyID) == "" || strings.TrimSpace(config.ReplayStagingS3.SecretAccessKey) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.Bucket) == "" || strings.TrimSpace(config.SourceS3.Endpoint) == "" ||
		strings.TrimSpace(config.SourceS3.AccessKeyID) == "" || strings.TrimSpace(config.SourceS3.SecretAccessKey) == "" ||
		strings.TrimSpace(config.SourceS3.Bucket) == "" || strings.TrimSpace(config.ResearchWorkspaceS3.Endpoint) == "" ||
		strings.TrimSpace(config.ResearchWorkspaceS3.AccessKeyID) == "" || strings.TrimSpace(config.ResearchWorkspaceS3.SecretAccessKey) == "" ||
		strings.TrimSpace(config.ResearchWorkspaceS3.Bucket) == "" || config.SourcePurgeLease <= 0 || config.SourcePurgePoll <= 0 ||
		strings.TrimSpace(config.ToolResultRedisURL) == "" || strings.TrimSpace(config.ToolResultKeyPrefix) == "" ||
		config.ToolResultCacheTTL != 30*time.Minute || config.ToolResultInlineBytes < 512 || config.ToolResultPageBytes < 512 ||
		config.ToolResultMaximumBytes < config.ToolResultInlineBytes || config.ToolResultOperationTimeout <= 0 ||
		strings.TrimSpace(config.QdrantURL) == "" || strings.TrimSpace(config.QdrantCollection) == "" || config.QdrantDenseDimensions <= 0 ||
		(config.RetrievalBootstrapMode != "development" && config.RetrievalBootstrapMode != "required") ||
		strings.TrimSpace(config.RetrievalBootstrapConfigPath) == "" ||
		config.SourceProcessingLease <= 0 || config.SourceProcessingHeartbeat <= 0 || config.SourceProcessingHeartbeat >= config.SourceProcessingLease ||
		config.SourceProcessingPoll <= 0 || strings.TrimSpace(config.SourceExtractionConfigID) == "" ||
		strings.TrimSpace(config.SourceVisionModel) == "" || strings.TrimSpace(config.SourceTranscriptionModel) == "" ||
		strings.TrimSpace(config.SourceVisionPromptVersion) == "" || config.SourceMaxVisionPages < 1 || config.SourceMaxVisionPages > 500 ||
		strings.TrimSpace(config.DocumentRendererURL) == "" || strings.TrimSpace(config.DocumentRendererServiceToken) == "" ||
		strings.TrimSpace(config.DocumentRenderConfigID) == "" || config.DocumentRenderTimeout <= 0 ||
		config.DocumentRenderMaxPages < 1 || config.DocumentRenderMaxPages > 500 || config.DocumentRenderDPI < 72 || config.DocumentRenderDPI > 300 ||
		config.DocumentRenderMaxPixelsPerPage < 1 || config.DocumentRenderMaxPixelsPerPage > 100_000_000 ||
		config.DocumentRenderMaxOutputBytes < 1 || config.DocumentRenderMaxOutputBytes > 2<<30 ||
		strings.TrimSpace(config.SourceMapParserURL) == "" || strings.TrimSpace(config.SourceMapParserServiceToken) == "" ||
		config.SourceMapParserTimeout <= 0 || config.SourceMapParserTimeout > 5*time.Minute ||
		config.SourceProcessingMaxBytes <= 0 || config.SourceProcessingMaxBytes > 100*1024*1024 || config.SourceProcessingMaxRunes <= 0 ||
		(config.SourceAdmissionMode != sourceadmission.ModeShadow && config.SourceAdmissionMode != sourceadmission.ModeEnforcement) ||
		config.SourceAdmissionQueryTimeout <= 0 || config.SourceAdmissionQueryTimeout > config.HTTPTimeout ||
		strings.TrimSpace(config.WebReaderURL) == "" || strings.TrimSpace(config.WebReaderServiceToken) == "" || config.WebReaderTimeout <= 0 ||
		config.SourceDiscoveryLease <= 0 || config.SourceDiscoveryPoll <= 0 ||
		workload.ValidateInteractiveCapacity(config.AgentInteractiveConcurrency, config.SourceProcessingConcurrency) != nil ||
		strings.TrimSpace(config.ReplayKeyID) == "" || len(config.ReplayKEK) != 32 ||
		strings.TrimSpace(config.MailSMTPAddr) == "" || strings.TrimSpace(config.MailFrom) == "" || strings.TrimSpace(config.WebBaseURL) == "" ||
		config.MailLeaseDuration <= 0 || config.MailPollInterval <= 0 || config.MailSMTPTimeout <= 0 {
		return workerConfig{}, errors.New("worker configuration is incomplete or inconsistent")
	}
	if len(config.TraceKafkaBrokers) == 0 || strings.TrimSpace(config.TraceKafkaTopic) == "" || strings.TrimSpace(config.TraceKafkaClientID) == "" ||
		strings.TrimSpace(config.TraceKafkaPurgeTopic) == "" || strings.TrimSpace(config.TraceKafkaPurgeClientID) == "" || config.TraceKafkaPurgeTopic == config.TraceKafkaTopic {
		return workerConfig{}, errors.New("worker Agent Trace Kafka configuration is incomplete")
	}
	return config, nil
}

func splitTraceKafkaBrokers(value string) []string {
	var brokers []string
	for _, broker := range strings.Split(value, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}
	return brokers
}

func workerEnvBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func workerEnvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func workerEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func writeWorkerJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
