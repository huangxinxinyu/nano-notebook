package rageval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProductRunManifest struct {
	SchemaVersion  int              `json:"schema_version"`
	IndexVersionID string           `json:"index_version_id"`
	Cases          []ProductRunCase `json:"cases"`
}

type ProductRunCase struct {
	CaseID         string              `json:"case_id"`
	RunID          string              `json:"run_id"`
	FixtureSources map[string]string   `json:"fixture_sources"`
	EvidenceUnits  map[string][]string `json:"evidence_units"`
}

type ProductRunExecutor struct {
	pool     *pgxpool.Pool
	manifest ProductRunManifest
	byCase   map[string]ProductRunCase
}

func NewProductRunExecutor(pool *pgxpool.Pool, manifest ProductRunManifest) (*ProductRunExecutor, error) {
	if pool == nil || manifest.SchemaVersion != 1 || strings.TrimSpace(manifest.IndexVersionID) == "" || len(manifest.Cases) == 0 {
		return nil, errors.New("invalid product Run manifest")
	}
	executor := &ProductRunExecutor{pool: pool, manifest: manifest, byCase: make(map[string]ProductRunCase, len(manifest.Cases))}
	for _, item := range manifest.Cases {
		if strings.TrimSpace(item.CaseID) == "" || strings.TrimSpace(item.RunID) == "" || len(item.FixtureSources) == 0 || len(item.EvidenceUnits) == 0 {
			return nil, errors.New("invalid product Run manifest Case")
		}
		if _, duplicate := executor.byCase[item.CaseID]; duplicate {
			return nil, errors.New("duplicated product Run manifest Case")
		}
		seenSources := make(map[string]struct{}, len(item.FixtureSources))
		for fixtureID, sourceID := range item.FixtureSources {
			if strings.TrimSpace(fixtureID) == "" || strings.TrimSpace(sourceID) == "" {
				return nil, errors.New("invalid fixture Source mapping")
			}
			if _, duplicate := seenSources[sourceID]; duplicate {
				return nil, errors.New("duplicated fixture Source mapping")
			}
			seenSources[sourceID] = struct{}{}
		}
		seenUnits := make(map[string]string)
		for alias, unitIDs := range item.EvidenceUnits {
			if strings.TrimSpace(alias) == "" || len(unitIDs) == 0 {
				return nil, errors.New("invalid expected Evidence mapping")
			}
			for _, unitID := range unitIDs {
				if strings.TrimSpace(unitID) == "" {
					return nil, errors.New("invalid expected Evidence Unit")
				}
				if previous, duplicate := seenUnits[unitID]; duplicate && previous != alias {
					return nil, errors.New("ambiguous expected Evidence Unit")
				}
				seenUnits[unitID] = alias
			}
		}
		executor.byCase[item.CaseID] = item
	}
	return executor, nil
}

func (e *ProductRunExecutor) ExecuteCase(ctx context.Context, evalCase Case, config PinnedConfig) (Observation, error) {
	if e == nil || e.pool == nil {
		return Observation{}, errors.New("nil product Run Executor")
	}
	manifestCase, ok := e.byCase[evalCase.ID]
	if !ok {
		return Observation{}, fmt.Errorf("product Run for Case %q is missing", evalCase.ID)
	}
	if err := validateProductRunCase(evalCase, manifestCase); err != nil {
		return Observation{}, err
	}
	version, err := retrieval.NewVersionStore(e.pool).ByID(ctx, e.manifest.IndexVersionID)
	if err != nil {
		return Observation{}, err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return Observation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return Observation{}, err
	}
	observation := Observation{CaseID: evalCase.ID, FixtureSHA256: make(map[string]string, len(evalCase.Fixtures))}
	var status, model, promptVersion, agentConfigID, question, answer string
	err = tx.QueryRow(ctx, `
		select r.status,r.model,r.prompt_version,r.agent_config_id,input.content,output.content,
			greatest(0, round(extract(epoch from (r.finished_at-r.started_at))*1000))::bigint
		from agent_runs r
		join chat_messages input on input.id=r.input_message_id and input.role='user'
		join chat_messages output on output.id=r.output_message_id and output.role='assistant'
		where r.id=$1
	`, manifestCase.RunID).Scan(&status, &model, &promptVersion, &agentConfigID, &question, &answer, &observation.LatencyMilliseconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return Observation{}, errors.New("product Eval Run is missing or has no published answer")
	}
	if err != nil {
		return Observation{}, err
	}
	if status != "completed" {
		return Observation{}, errors.New("product Eval Run is not completed")
	}
	if question != evalCase.Question {
		observation.InvariantFailures = append(observation.InvariantFailures, "question_identity_mismatch")
	}
	if model != config.ComposerModel || promptVersion != config.PromptVersion || agentConfigID != config.AgentConfigID || !reflect.DeepEqual(version.Config, config.Index) {
		observation.InvariantFailures = append(observation.InvariantFailures, "pinned_configuration_mismatch")
	}
	coveragePassed, err := loadFixtureIdentity(ctx, tx, manifestCase, evalCase, config, e.manifest.IndexVersionID, observation.FixtureSHA256)
	if err != nil {
		return Observation{}, err
	}
	observation.CoveragePassed = coveragePassed
	unitAliases := make(map[string]string)
	for alias, unitIDs := range manifestCase.EvidenceUnits {
		for _, unitID := range unitIDs {
			unitAliases[unitID] = alias
		}
	}
	retrievedUnits, err := loadRetrievedUnits(ctx, tx, manifestCase.RunID)
	if err != nil {
		return Observation{}, err
	}
	observation.RetrievedEvidenceIDs = aliasesForUnits(retrievedUnits, unitAliases)
	citationUnits, err := loadCitationAndClaimFacts(ctx, tx, manifestCase.RunID, config.VerifierModel, config.VerifierPromptVersion, &observation)
	if err != nil {
		return Observation{}, err
	}
	observation.CitationEvidenceIDs = aliasesForUnits(citationUnits, unitAliases)
	for _, fact := range evalCase.RequiredFacts {
		if strings.Contains(answer, fact) {
			observation.RequiredFactsFound = append(observation.RequiredFactsFound, fact)
		}
	}
	for _, claim := range evalCase.ForbiddenClaims {
		if strings.Contains(answer, claim) {
			observation.ForbiddenClaimsFound = append(observation.ForbiddenClaimsFound, claim)
		}
	}
	if err := loadModelUsage(ctx, tx, manifestCase.RunID, &observation); err != nil {
		return Observation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func validateProductRunCase(evalCase Case, manifestCase ProductRunCase) error {
	if len(manifestCase.FixtureSources) != len(evalCase.Fixtures) {
		return errors.New("product Run fixture Source set does not match Eval Case")
	}
	for _, fixture := range evalCase.Fixtures {
		if manifestCase.FixtureSources[fixture.ID] == "" {
			return errors.New("product Run fixture Source is missing")
		}
	}
	expected := expectedEvidence(evalCase)
	for alias := range expected {
		if len(manifestCase.EvidenceUnits[alias]) == 0 {
			return errors.New("product Run expected Evidence mapping is missing")
		}
	}
	return nil
}

func loadFixtureIdentity(ctx context.Context, tx pgx.Tx, manifestCase ProductRunCase, evalCase Case, config PinnedConfig, versionID string, observed map[string]string) (bool, error) {
	fixtureByID := make(map[string]Fixture, len(evalCase.Fixtures))
	for _, fixture := range evalCase.Fixtures {
		fixtureByID[fixture.ID] = fixture
	}
	coveragePassed := true
	for fixtureID, sourceID := range manifestCase.FixtureSources {
		fixture := fixtureByID[fixtureID]
		var contentSHA, coverage, extractionConfigID, artifactSchemaVersion, pinnedVersion string
		err := tx.QueryRow(ctx, `
			select s.content_sha256,c.status,r.extraction_config_id,r.artifact_schema_version,e.index_version_id
			from agent_run_evidence_set e
			join source_sources s on s.id=e.source_id and s.state='ready'
			join source_evidence_revisions r on r.id=e.evidence_revision_id and r.source_id=s.id and r.status='active'
			join source_evidence_coverage c on c.revision_id=r.id
			join retrieval_source_index_builds b on b.revision_id=r.id and b.index_version_id=e.index_version_id and b.status='verified'
			where e.run_id=$1 and e.source_id=$2
		`, manifestCase.RunID, sourceID).Scan(&contentSHA, &coverage, &extractionConfigID, &artifactSchemaVersion, &pinnedVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errors.New("product Run fixture Source authority is missing")
		}
		if err != nil {
			return false, err
		}
		observed[fixtureID] = contentSHA
		expectedSchemaVersion := fmt.Sprintf("nano.normalized-source.v%d", config.EvidenceSchemaVersion)
		if coverage != "complete" || extractionConfigID != config.ExtractionConfigID || artifactSchemaVersion != expectedSchemaVersion || pinnedVersion != versionID || contentSHA != fixture.SHA256 {
			coveragePassed = false
		}
	}
	return coveragePassed, nil
}

func loadRetrievedUnits(ctx context.Context, tx pgx.Tx, runID string) ([]string, error) {
	rows, err := tx.Query(ctx, `select kind,payload from agent_run_checkpoints where run_id=$1 and kind in ('action_proposal','action_result') order by sequence_no`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	searchActions := make(map[string]bool)
	results := make([]json.RawMessage, 0)
	for rows.Next() {
		var kind string
		var payload json.RawMessage
		if err := rows.Scan(&kind, &payload); err != nil {
			return nil, err
		}
		if kind == "action_proposal" {
			var proposal struct {
				Actions []struct {
					ActionID string `json:"action_id"`
					Name     string `json:"name"`
				} `json:"actions"`
			}
			if json.Unmarshal(payload, &proposal) != nil {
				return nil, errors.New("invalid product Run Action Proposal")
			}
			for _, action := range proposal.Actions {
				if action.Name == "search_evidence" {
					searchActions[action.ActionID] = true
				}
			}
		} else {
			results = append(results, append(json.RawMessage(nil), payload...))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	units := make([]string, 0)
	for _, payload := range results {
		var result struct {
			ActionID string `json:"action_id"`
			Status   string `json:"status"`
			Output   struct {
				Evidence []struct {
					Ranges []retrieval.UnitRef `json:"evidence_ranges"`
				} `json:"evidence"`
			} `json:"output"`
		}
		if json.Unmarshal(payload, &result) != nil {
			return nil, errors.New("invalid product Run Action Result")
		}
		if !searchActions[result.ActionID] || result.Status != "succeeded" {
			continue
		}
		for _, evidence := range result.Output.Evidence {
			for _, ref := range evidence.Ranges {
				units = append(units, ref.UnitID)
			}
		}
	}
	return units, nil
}

func loadCitationAndClaimFacts(ctx context.Context, tx pgx.Tx, runID, verifierModel, verifierPromptVersion string, observation *Observation) ([]string, error) {
	var storedVerifier, storedPrompt string
	if err := tx.QueryRow(ctx, `select verifier_model,verifier_prompt_version from agent_run_grounding_plans where run_id=$1`, runID).Scan(&storedVerifier, &storedPrompt); err != nil {
		return nil, err
	}
	if storedVerifier != verifierModel || storedPrompt != verifierPromptVersion {
		observation.InvariantFailures = append(observation.InvariantFailures, "pinned_verifier_mismatch")
	}
	if err := tx.QueryRow(ctx, `select count(*) from agent_claim_support_records where run_id=$1`, runID).Scan(&observation.MaterialClaimCount); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `select count(distinct claim_ordinal) from chat_citations where run_id=$1`, runID).Scan(&observation.CitedClaimCount); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select unit_id from chat_citations where run_id=$1 order by claim_ordinal,citation_ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	units := make([]string, 0)
	for rows.Next() {
		var unitID string
		if err := rows.Scan(&unitID); err != nil {
			return nil, err
		}
		units = append(units, unitID)
	}
	return units, rows.Err()
}

func loadModelUsage(ctx context.Context, tx pgx.Tx, runID string, observation *Observation) error {
	rows, err := tx.Query(ctx, `
		select tr.payload from agent_trace_records tr join agent_traces t on t.trace_id=tr.trace_id
		where t.run_id=$1 and tr.record_kind='span_ended'
	`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var document struct {
			Attributes []struct {
				Key     string   `json:"key"`
				Int64   *int64   `json:"int64"`
				Float64 *float64 `json:"float64"`
				String  *string  `json:"string"`
			} `json:"attributes"`
		}
		if json.Unmarshal(payload, &document) != nil {
			return errors.New("invalid product Run Trace payload")
		}
		currency := ""
		cost := 0.0
		costKnown := false
		for _, attribute := range document.Attributes {
			switch attribute.Key {
			case semconv.TokenInputKey:
				if attribute.Int64 != nil {
					observation.InputTokens += *attribute.Int64
				}
			case semconv.TokenTotalKey:
				if attribute.Int64 != nil {
					observation.TotalTokens += *attribute.Int64
				}
			case semconv.CostAmountKey:
				if attribute.Float64 != nil {
					cost, costKnown = *attribute.Float64, true
				}
			case semconv.CostCurrencyKey:
				if attribute.String != nil {
					currency = *attribute.String
				}
			}
		}
		if costKnown {
			if currency != "USD" {
				observation.InvariantFailures = append(observation.InvariantFailures, "unsupported_cost_currency")
			} else {
				observation.EstimatedCostUSD += cost
			}
		}
	}
	return rows.Err()
}

func aliasesForUnits(unitIDs []string, aliases map[string]string) []string {
	result := make([]string, 0, len(unitIDs))
	seen := make(map[string]struct{})
	for _, unitID := range unitIDs {
		alias := aliases[unitID]
		if alias == "" {
			continue
		}
		if _, duplicate := seen[alias]; duplicate {
			continue
		}
		seen[alias] = struct{}{}
		result = append(result, alias)
	}
	return result
}
