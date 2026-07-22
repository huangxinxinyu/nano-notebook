package rageval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProductSourceManifest struct {
	SchemaVersion  int                 `json:"schema_version"`
	IndexVersionID string              `json:"index_version_id"`
	UserID         string              `json:"user_id"`
	NotebookID     string              `json:"notebook_id"`
	Cases          []ProductSourceCase `json:"cases"`
}

type ProductSourceCase struct {
	CaseID         string              `json:"case_id"`
	FixtureSources map[string]string   `json:"fixture_sources"`
	EvidenceUnits  map[string][]string `json:"evidence_units"`
}

type liveProductModel interface {
	agent.DecisionModel
	Embed(context.Context, models.EmbeddingRequest) (models.EmbeddingOutcome, error)
	Rerank(context.Context, models.RerankRequest) (models.RerankOutcome, error)
}

type LiveProductExecutor struct {
	pool       *pgxpool.Pool
	manifest   ProductSourceManifest
	caseByID   map[string]ProductSourceCase
	controller *agent.Controller
	meter      *meteredProductModel
}

func NewLiveProductExecutor(pool *pgxpool.Pool, vectors *qdrantstore.Client, model liveProductModel, manifest ProductSourceManifest) (*LiveProductExecutor, error) {
	if pool == nil || vectors == nil || model == nil || manifest.SchemaVersion != 1 || strings.TrimSpace(manifest.IndexVersionID) == "" ||
		strings.TrimSpace(manifest.UserID) == "" || strings.TrimSpace(manifest.NotebookID) == "" || len(manifest.Cases) == 0 {
		return nil, errors.New("invalid live product Eval configuration")
	}
	caseByID := make(map[string]ProductSourceCase, len(manifest.Cases))
	for _, item := range manifest.Cases {
		if strings.TrimSpace(item.CaseID) == "" || len(item.FixtureSources) == 0 || len(item.EvidenceUnits) == 0 {
			return nil, errors.New("invalid live product Eval Case")
		}
		if _, duplicate := caseByID[item.CaseID]; duplicate {
			return nil, errors.New("duplicated live product Eval Case")
		}
		if _, err := NewProductRunExecutor(pool, ProductRunManifest{
			SchemaVersion: 1, IndexVersionID: manifest.IndexVersionID,
			Cases: []ProductRunCase{{CaseID: item.CaseID, RunID: "pending", FixtureSources: item.FixtureSources, EvidenceUnits: item.EvidenceUnits}},
		}); err != nil {
			return nil, err
		}
		caseByID[item.CaseID] = item
	}
	meter := &meteredProductModel{next: model}
	evidenceSearch := agent.NewEvidenceSearchService(pool, vectors, meter)
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction(), agent.NewCurrentTimeAction(nil), agent.NewSearchEvidenceAction(evidenceSearch))
	if err != nil {
		return nil, err
	}
	grounder := agent.NewGroundingService(pool)
	runtime := agent.NewPostgresRuntime(pool, agent.GroundedSystemPrompt, nil, agent.WithGroundingService(grounder))
	return &LiveProductExecutor{
		pool: pool, manifest: manifest, caseByID: caseByID, meter: meter,
		controller: agent.NewController(runtime, meter, registry),
	}, nil
}

func (e *LiveProductExecutor) ExecuteCase(ctx context.Context, evalCase Case, config PinnedConfig) (Observation, error) {
	if e == nil || e.pool == nil || e.controller == nil {
		return Observation{}, errors.New("nil live product Eval Executor")
	}
	manifestCase, ok := e.caseByID[evalCase.ID]
	if !ok {
		return Observation{}, fmt.Errorf("live product sources for Case %q are missing", evalCase.ID)
	}
	if config.PromptVersion != agent.GroundedPromptVersion {
		return Observation{}, errors.New("live product Eval requires the grounded Agent prompt")
	}
	if err := validateProductRunCase(evalCase, ProductRunCase{CaseID: evalCase.ID, RunID: "pending", FixtureSources: manifestCase.FixtureSources, EvidenceUnits: manifestCase.EvidenceUnits}); err != nil {
		return Observation{}, err
	}
	usageBefore := e.meter.snapshot()
	runID, attempt, err := e.admit(ctx, evalCase, config, manifestCase)
	if err != nil {
		return Observation{}, err
	}
	if err := e.controller.Execute(ctx, attempt); err != nil {
		return Observation{}, fmt.Errorf("execute live product Run %s: %w", runID, err)
	}
	observer, err := NewProductRunExecutor(e.pool, ProductRunManifest{
		SchemaVersion: 1, IndexVersionID: e.manifest.IndexVersionID,
		Cases: []ProductRunCase{{CaseID: evalCase.ID, RunID: runID, FixtureSources: manifestCase.FixtureSources, EvidenceUnits: manifestCase.EvidenceUnits}},
	})
	if err != nil {
		return Observation{}, err
	}
	observation, err := observer.ExecuteCase(ctx, evalCase, config)
	if err != nil {
		return Observation{}, err
	}
	usage := e.meter.snapshot().subtract(usageBefore)
	observation.InputTokens = usage.inputTokens
	observation.TotalTokens = usage.totalTokens
	observation.EstimatedCostUSD = usage.costUSD
	if usage.calls == 0 || usage.unknownCosts != 0 || usage.incompleteTokens != 0 {
		observation.InvariantFailures = append(observation.InvariantFailures, "model_usage_incomplete")
	}
	return observation, nil
}

type modelUsage struct {
	calls            int
	inputTokens      int64
	totalTokens      int64
	costUSD          float64
	unknownCosts     int
	incompleteTokens int
}

func (u modelUsage) subtract(previous modelUsage) modelUsage {
	return modelUsage{
		calls: u.calls - previous.calls, inputTokens: u.inputTokens - previous.inputTokens,
		totalTokens: u.totalTokens - previous.totalTokens, costUSD: u.costUSD - previous.costUSD,
		unknownCosts: u.unknownCosts - previous.unknownCosts, incompleteTokens: u.incompleteTokens - previous.incompleteTokens,
	}
}

type meteredProductModel struct {
	next  liveProductModel
	mu    sync.Mutex
	usage modelUsage
}

func (m *meteredProductModel) snapshot() modelUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

func (m *meteredProductModel) add(input, total *int64, cost models.ModelCost) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage.calls++
	if input == nil || total == nil {
		m.usage.incompleteTokens++
	}
	if input != nil {
		m.usage.inputTokens += *input
	}
	if total != nil {
		value := *total
		if input != nil && value < *input {
			value = *input
			m.usage.incompleteTokens++
		}
		m.usage.totalTokens += value
	} else if input != nil {
		m.usage.totalTokens += *input
	}
	if !cost.Known || cost.Amount == nil || cost.Currency != "USD" {
		m.usage.unknownCosts++
		return
	}
	m.usage.costUSD += *cost.Amount
}

func (m *meteredProductModel) Decide(ctx context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	outcome, err := m.next.Decide(ctx, request)
	m.add(outcome.Metadata.InputTokens, outcome.Metadata.TotalTokens, outcome.Metadata.Cost)
	return outcome, err
}

func (m *meteredProductModel) Embed(ctx context.Context, request models.EmbeddingRequest) (models.EmbeddingOutcome, error) {
	outcome, err := m.next.Embed(ctx, request)
	m.add(outcome.Metadata.InputTokens, outcome.Metadata.TotalTokens, outcome.Metadata.Cost)
	return outcome, err
}

func (m *meteredProductModel) Rerank(ctx context.Context, request models.RerankRequest) (models.RerankOutcome, error) {
	outcome, err := m.next.Rerank(ctx, request)
	m.add(outcome.Metadata.InputTokens, outcome.Metadata.TotalTokens, outcome.Metadata.Cost)
	return outcome, err
}

func (e *LiveProductExecutor) admit(ctx context.Context, evalCase Case, config PinnedConfig, manifestCase ProductSourceCase) (string, agent.Attempt, error) {
	runID := "evalrun_" + uuid.NewString()
	chatID := "evalchat_" + uuid.NewString()
	messageID := "evalmsg_" + uuid.NewString()
	jobID := "evaljob_" + uuid.NewString()
	leaseToken := uuid.NewString()
	traceScope, err := agent.NewTraceScope(agent.DiscardTraceSink{})
	if err != nil {
		return "", agent.Attempt{}, err
	}
	defer traceScope.Rollback()
	traceContext := agent.ContextWithTraceScope(ctx, traceScope)
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return "", agent.Attempt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return "", agent.Attempt{}, err
	}
	var authorized bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from notebook_memberships where notebook_id=$1 and user_id=$2)`, e.manifest.NotebookID, e.manifest.UserID).Scan(&authorized); err != nil {
		return "", agent.Attempt{}, err
	}
	if !authorized {
		return "", agent.Attempt{}, errors.New("live product Eval principal is not a Notebook member")
	}
	if _, err := tx.Exec(ctx, `insert into chat_chats(id,notebook_id,creator_user_id,title) values($1,$2,$3,$4)`, chatID, e.manifest.NotebookID, e.manifest.UserID, "Offline Eval: "+evalCase.ID); err != nil {
		return "", agent.Attempt{}, err
	}
	if _, err := tx.Exec(ctx, `insert into chat_messages(id,chat_id,role,content) values($1,$2,'user',$3)`, messageID, chatID, evalCase.Question); err != nil {
		return "", agent.Attempt{}, err
	}
	runConfig := agent.RunConfig{
		ID: config.AgentConfigID, ActionDecisionLimit: 4, FinalDecisionLimit: 1, ActionLimit: 8, ActionBatchLimit: 4,
		ActionResultByteLimit: 16 * 1024, ActionResultsByteLimit: 64 * 1024, Deadline: 10 * time.Minute,
	}
	store := agent.NewStore(tx)
	if err := store.CreateQueued(ctx, runID, e.manifest.UserID, chatID, messageID, config.ComposerModel, config.PromptVersion, "UTC", runConfig); err != nil {
		return "", agent.Attempt{}, err
	}
	sourceIDs := make([]string, 0, len(evalCase.Fixtures))
	for _, fixture := range evalCase.Fixtures {
		sourceIDs = append(sourceIDs, manifestCase.FixtureSources[fixture.ID])
	}
	if err := store.PinEvidenceSetVersion(ctx, runID, e.manifest.UserID, e.manifest.IndexVersionID, sourceIDs); err != nil {
		return "", agent.Attempt{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into agent_jobs(id,kind,run_id,status,attempt_no,lease_token,lease_expires_at,started_at)
		values($1,'agent_run',$2,'running',1,$3::uuid,now()+interval '10 minutes',now())
	`, jobID, runID, leaseToken); err != nil {
		return "", agent.Attempt{}, err
	}
	runTag, err := tx.Exec(ctx, `update agent_runs set status='running',started_at=now(),updated_at=now() where id=$1 and status='queued'`, runID)
	if err != nil {
		return "", agent.Attempt{}, err
	}
	if runTag.RowsAffected() != 1 {
		return "", agent.Attempt{}, errors.New("live product Eval Run admission lost authority")
	}
	if err := agent.StartRunTraceInTx(traceContext, tx, runID, config.ComposerModel, config.PromptVersion, nil); err != nil {
		return "", agent.Attempt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", agent.Attempt{}, err
	}
	_ = traceScope.PublishAfterCommit(traceContext)
	return runID, agent.Attempt{JobID: jobID, RunID: runID, AttemptNo: 1, LeaseToken: leaseToken}, nil
}
