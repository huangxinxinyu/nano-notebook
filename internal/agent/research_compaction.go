package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const (
	researchArchivalBatchMaxSteps   = 24
	researchArchivalCapsuleMaxBytes = 8 * 1024
	researchArchivalPromptVersion   = "agent.deep-research-archival-compactor@1"
	researchTaskMemoryMaxBytes      = 32 * 1024
	researchTaskMemoryPromptVersion = "agent.deep-research-task-memory-compactor@1"
)

const ContextUnitResearchTaskMemory ContextUnitKind = "research_task_memory"

type researchArchivalStep struct {
	DecisionNo         int
	StartCheckpointSeq int
	EndCheckpointSeq   int
	SourceSHA256       string
	Unit               ContextUnit
}

type researchArchivalCapsule struct {
	DecisionNo         int
	StartCheckpointSeq int
	EndCheckpointSeq   int
	SourceSHA256       string
	CapsuleJSON        json.RawMessage
	CapsuleSHA256      string
}

type researchTaskMemory struct {
	FirstDecisionNo      int
	LastDecisionNo       int
	StartCheckpointSeq   int
	EndCheckpointSeq     int
	SourceCapsulesSHA256 string
	MemoryJSON           json.RawMessage
	MemorySHA256         string
}

type researchCapsuleBatch struct {
	SchemaVersion string                   `json:"schema_version"`
	Capsules      []researchCapsuleContent `json:"capsules"`
}

type researchCapsuleContent struct {
	SchemaVersion      string   `json:"schema_version"`
	DecisionNo         int      `json:"decision_no"`
	StartCheckpointSeq int      `json:"start_checkpoint_seq"`
	EndCheckpointSeq   int      `json:"end_checkpoint_seq"`
	ObjectiveAdvanced  string   `json:"objective_advanced"`
	Conclusions        []string `json:"conclusions"`
	Decisions          []string `json:"decisions"`
	Constraints        []string `json:"constraints"`
	DurableRefs        []string `json:"durable_refs"`
	Contradictions     []string `json:"contradictions"`
	Verification       []string `json:"verification"`
	FollowUp           []string `json:"follow_up"`
}

type researchTaskMemoryContent struct {
	SchemaVersion      string   `json:"schema_version"`
	FirstDecisionNo    int      `json:"first_decision_no"`
	LastDecisionNo     int      `json:"last_decision_no"`
	StartCheckpointSeq int      `json:"start_checkpoint_seq"`
	EndCheckpointSeq   int      `json:"end_checkpoint_seq"`
	Goal               string   `json:"goal"`
	Phase              string   `json:"phase"`
	Conclusions        []string `json:"conclusions"`
	Decisions          []string `json:"decisions"`
	Constraints        []string `json:"constraints"`
	DurableRefs        []string `json:"durable_refs"`
	Contradictions     []string `json:"contradictions"`
	FailedPaths        []string `json:"failed_paths"`
	Verification       []string `json:"verification"`
	ReportState        []string `json:"report_state"`
	FollowUp           []string `json:"follow_up"`
}

type researchCompactionMessage struct {
	Role         models.ModelRole               `json:"role"`
	Content      string                         `json:"content,omitempty"`
	ActionCalls  []researchCompactionActionCall `json:"action_calls,omitempty"`
	ActionCallID string                         `json:"action_call_id,omitempty"`
}

type researchCompactionActionCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type researchArchivalModelStep struct {
	DecisionNo         int                         `json:"decision_no"`
	StartCheckpointSeq int                         `json:"start_checkpoint_seq"`
	EndCheckpointSeq   int                         `json:"end_checkpoint_seq"`
	Messages           []researchCompactionMessage `json:"messages"`
}

type researchTaskMemoryModelStep struct {
	DecisionNo         int                         `json:"decision_no"`
	StartCheckpointSeq int                         `json:"start_checkpoint_seq"`
	EndCheckpointSeq   int                         `json:"end_checkpoint_seq"`
	Capsule            json.RawMessage             `json:"capsule"`
	RetainedTrajectory []researchCompactionMessage `json:"retained_trajectory"`
}

func researchCompactionMessages(messages []models.ModelMessage) []researchCompactionMessage {
	result := make([]researchCompactionMessage, 0, len(messages))
	for _, message := range messages {
		value := researchCompactionMessage{Role: message.Role, Content: message.Content, ActionCallID: message.ActionCallID}
		for _, call := range message.ActionCalls {
			value.ActionCalls = append(value.ActionCalls, researchCompactionActionCall{ID: call.ID, Name: call.Name, Input: append(json.RawMessage(nil), call.Input...)})
		}
		result = append(result, value)
	}
	return result
}

func (r *ResearchRuntime) loadResearchArchivalCapsules(ctx context.Context, runID string) (map[int]researchArchivalCapsule, error) {
	rows, err := r.pool.Query(ctx, `
		select decision_no,start_checkpoint_seq,end_checkpoint_seq,source_checkpoint_sha256,
			capsule_json,capsule_sha256,capsule_bytes
		from research_archival_capsules where run_id=$1 order by decision_no
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int]researchArchivalCapsule)
	for rows.Next() {
		var value researchArchivalCapsule
		var storedJSON []byte
		var storedBytes int
		if err := rows.Scan(&value.DecisionNo, &value.StartCheckpointSeq, &value.EndCheckpointSeq, &value.SourceSHA256,
			&storedJSON, &value.CapsuleSHA256, &storedBytes); err != nil {
			return nil, err
		}
		var content researchCapsuleContent
		decoder := json.NewDecoder(bytes.NewReader(storedJSON))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&content); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, projectionError("invalid stored Research Capsule %d", value.DecisionNo)
		}
		batchJSON, err := json.Marshal(researchCapsuleBatch{SchemaVersion: "nano.research-capsules@1", Capsules: []researchCapsuleContent{content}})
		if err != nil {
			return nil, err
		}
		decoded, err := decodeResearchCapsuleBatch(batchJSON, []researchArchivalStep{{
			DecisionNo: value.DecisionNo, StartCheckpointSeq: value.StartCheckpointSeq,
			EndCheckpointSeq: value.EndCheckpointSeq, SourceSHA256: value.SourceSHA256,
		}})
		if err != nil || len(decoded) != 1 || decoded[0].CapsuleSHA256 != value.CapsuleSHA256 ||
			len(decoded[0].CapsuleJSON) != storedBytes {
			return nil, projectionError("stored Research Capsule %d changed", value.DecisionNo)
		}
		value.CapsuleJSON = decoded[0].CapsuleJSON
		if _, duplicate := result[value.DecisionNo]; duplicate {
			return nil, projectionError("duplicate stored Research Capsule %d", value.DecisionNo)
		}
		result[value.DecisionNo] = value
	}
	return result, rows.Err()
}

func (r *ResearchRuntime) loadResearchTaskMemories(ctx context.Context, runID string, archived map[int]researchArchivalCapsule) ([]researchTaskMemory, error) {
	rows, err := r.pool.Query(ctx, `
		select first_decision_no,last_decision_no,start_checkpoint_seq,end_checkpoint_seq,
			source_capsules_sha256,memory_json,memory_sha256,memory_bytes
		from research_task_memories where run_id=$1 order by first_decision_no
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]researchTaskMemory, 0)
	for rows.Next() {
		var stored researchTaskMemory
		var raw []byte
		var storedBytes int
		if err := rows.Scan(&stored.FirstDecisionNo, &stored.LastDecisionNo, &stored.StartCheckpointSeq, &stored.EndCheckpointSeq,
			&stored.SourceCapsulesSHA256, &raw, &stored.MemorySHA256, &storedBytes); err != nil {
			return nil, err
		}
		capsules := make([]researchArchivalCapsule, 0, stored.LastDecisionNo-stored.FirstDecisionNo+1)
		for decision := stored.FirstDecisionNo; decision <= stored.LastDecisionNo; decision++ {
			capsule, ok := archived[decision]
			if !ok {
				return nil, projectionError("Research Task Memory references missing Capsule %d", decision)
			}
			capsules = append(capsules, capsule)
		}
		decoded, err := decodeResearchTaskMemory(raw, capsules)
		if err != nil || decoded.SourceCapsulesSHA256 != stored.SourceCapsulesSHA256 || decoded.MemorySHA256 != stored.MemorySHA256 || len(decoded.MemoryJSON) != storedBytes {
			return nil, projectionError("stored Research Task Memory %d..%d changed", stored.FirstDecisionNo, stored.LastDecisionNo)
		}
		result = append(result, decoded)
	}
	return result, rows.Err()
}

func (r *ResearchRuntime) PrepareDecisionRequest(
	ctx context.Context,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
	model DecisionModel,
	triggerReason string,
) (models.ModelRequest, error) {
	if !isThresholdResearchExecution(execution) {
		return r.BuildDecisionRequest(ctx, execution, prefix, definitions)
	}
	request, err := r.buildDecisionRequest(ctx, execution, prefix, definitions, nil, nil)
	if err != nil {
		return models.ModelRequest{}, err
	}
	request, err = r.FinalizeDecisionRequest(ctx, execution, prefix, request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	before, err := EstimateModelRequestTokens(request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	attachResearchContextTelemetry(&request, execution, before, "", 0, 0)
	if triggerReason == "" && before.Tokens < execution.ModelContext.Budgets.CompactionTriggerTokens {
		return request, nil
	}
	if triggerReason == "" {
		triggerReason = CompactionTriggerThreshold
	}
	if triggerReason != CompactionTriggerThreshold && triggerReason != CompactionTriggerProviderOverflow {
		return models.ModelRequest{}, errors.New("invalid Research Compaction trigger")
	}
	if model == nil || execution.ModelContext.Policy.KeepRecentTokens < 1 {
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}

	rawUnits, err := ProjectChatLane(ctx, ChatLane{Turns: []ChatLaneTurn{{
		MessageID: execution.InputMessageID,
		Content:   researchOriginalRequestFromDecisionRequest(request),
		Runs:      []ChatLaneRun{{RunID: execution.RunID, Prefix: &prefix}},
	}}}, nil)
	if err != nil || len(rawUnits) < 2 {
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	rawTrajectory := rawUnits[1:]
	if len(rawTrajectory) < 2 {
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	archived, err := r.loadResearchArchivalCapsules(ctx, execution.RunID)
	if err != nil {
		return models.ModelRequest{}, err
	}
	selectedUnits, _, err := selectResearchArchivalSteps(rawTrajectory, archived, execution.ModelContext.Policy.KeepRecentTokens, researchArchivalBatchMaxSteps)
	if err != nil {
		if triggerReason == CompactionTriggerThreshold && before.Tokens <= execution.ModelContext.Budgets.SafeInputTokens {
			attachResearchContextTelemetry(&request, execution, before, triggerReason, 0, 0)
			return request, nil
		}
		if len(archived) > 0 {
			planJSON, phase, stateErr := r.loadResearchCompactionState(ctx, execution.RunID)
			if stateErr != nil {
				return models.ModelRequest{}, stateErr
			}
			_, taskMemoryPrompt, promptErr := r.loadResearchCompactionPrompts(ctx, execution.RunID)
			if promptErr != nil {
				return models.ModelRequest{}, promptErr
			}
			candidate, after, compactErr := r.compactResearchTaskMemory(ctx, execution, prefix, definitions, model, rawTrajectory, archived, before, planJSON, phase, taskMemoryPrompt)
			if compactErr != nil {
				return models.ModelRequest{}, compactErr
			}
			attachResearchContextTelemetry(&candidate, execution, after, triggerReason, before.Tokens, after.Tokens)
			return candidate, nil
		}
		r.recordResearchCompactionFailure(ctx, execution, "archival", "no_archival_range", 0, 0, before.Tokens, 0)
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	steps, err := r.loadResearchArchivalSteps(ctx, execution.RunID, selectedUnits)
	if err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "archival", "checkpoint_range_invalid", 0, 0, before.Tokens, 0)
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	planJSON, phase, err := r.loadResearchCompactionState(ctx, execution.RunID)
	if err != nil {
		return models.ModelRequest{}, err
	}
	archivalPrompt, taskMemoryPrompt, err := r.loadResearchCompactionPrompts(ctx, execution.RunID)
	if err != nil {
		return models.ModelRequest{}, err
	}
	for len(steps) > 0 {
		modelRequest, buildErr := buildResearchArchivalModelRequest(execution, steps, planJSON, phase, archivalPrompt)
		if buildErr != nil {
			return models.ModelRequest{}, buildErr
		}
		count, countErr := EstimateModelRequestTokens(modelRequest)
		if countErr != nil {
			return models.ModelRequest{}, countErr
		}
		if count.Tokens <= execution.ModelContext.Budgets.SafeInputTokens {
			break
		}
		steps = steps[:len(steps)-1]
	}
	if len(steps) == 0 {
		r.recordResearchCompactionFailure(ctx, execution, "archival", "summarizer_input_exceeded", 0, 0, before.Tokens, 0)
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	capsules, err := generateResearchArchivalCapsules(ctx, model, execution, steps, planJSON, phase, archivalPrompt)
	if err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "archival", "model_output_invalid", steps[0].StartCheckpointSeq, steps[len(steps)-1].EndCheckpointSeq, before.Tokens, 0)
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	candidateArchives := make(map[int]researchArchivalCapsule, len(archived)+len(capsules))
	for decision, capsule := range archived {
		candidateArchives[decision] = capsule
	}
	for _, capsule := range capsules {
		candidateArchives[capsule.DecisionNo] = capsule
	}
	candidate, err := r.buildDecisionRequest(ctx, execution, prefix, definitions, candidateArchives, nil)
	if err == nil {
		candidate, err = r.FinalizeDecisionRequest(ctx, execution, prefix, candidate)
	}
	if err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "archival", "reconstruction_failed", steps[0].StartCheckpointSeq, steps[len(steps)-1].EndCheckpointSeq, before.Tokens, 0)
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	after, err := EstimateModelRequestTokens(candidate)
	if err != nil {
		return models.ModelRequest{}, err
	}
	if err := r.appendResearchArchivalCapsules(ctx, execution, capsules); err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "archival", "persistence_conflict", steps[0].StartCheckpointSeq, steps[len(steps)-1].EndCheckpointSeq, before.Tokens, after.Tokens)
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	if after.Tokens > execution.ModelContext.Budgets.SafeInputTokens {
		candidate, after, err = r.compactResearchTaskMemory(ctx, execution, prefix, definitions, model, rawTrajectory, candidateArchives, after, planJSON, phase, taskMemoryPrompt)
		if err != nil {
			return models.ModelRequest{}, err
		}
	}
	attachResearchContextTelemetry(&candidate, execution, after, triggerReason, before.Tokens, after.Tokens)
	return candidate, nil
}

func (r *ResearchRuntime) compactResearchTaskMemory(
	ctx context.Context,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
	model DecisionModel,
	rawUnits []ContextUnit,
	archived map[int]researchArchivalCapsule,
	before ContextTokenCount,
	planJSON, phase, systemPrompt string,
) (models.ModelRequest, ContextTokenCount, error) {
	existing, err := r.loadResearchTaskMemories(ctx, execution.RunID, archived)
	if err != nil {
		return models.ModelRequest{}, ContextTokenCount{}, err
	}
	covered := make(map[int]bool)
	for _, memory := range existing {
		for decision := memory.FirstDecisionNo; decision <= memory.LastDecisionNo; decision++ {
			covered[decision] = true
		}
	}
	capsules := make([]researchArchivalCapsule, 0)
	selectedUnits := make([]ContextUnit, 0)
	started := false
	for _, unit := range rawUnits {
		capsule, ok := archived[unit.DecisionNo]
		if covered[unit.DecisionNo] {
			if started {
				break
			}
			continue
		}
		if !ok {
			if started {
				break
			}
			continue
		}
		if len(capsules) > 0 && (capsule.DecisionNo != capsules[len(capsules)-1].DecisionNo+1 || capsule.StartCheckpointSeq != capsules[len(capsules)-1].EndCheckpointSeq+1) {
			break
		}
		started = true
		capsules = append(capsules, capsule)
		selectedUnits = append(selectedUnits, cloneResearchContextUnit(unit))
	}
	if len(capsules) == 0 {
		r.recordResearchCompactionFailure(ctx, execution, "task_memory", "no_task_memory_range", 0, 0, before.Tokens, 0)
		return models.ModelRequest{}, ContextTokenCount{}, ErrContextBudgetExceeded
	}
	for len(capsules) > 0 {
		modelRequest, buildErr := buildResearchTaskMemoryModelRequest(execution, capsules, selectedUnits, planJSON, phase, systemPrompt)
		if buildErr != nil {
			return models.ModelRequest{}, ContextTokenCount{}, buildErr
		}
		count, countErr := EstimateModelRequestTokens(modelRequest)
		if countErr != nil {
			return models.ModelRequest{}, ContextTokenCount{}, countErr
		}
		if count.Tokens <= execution.ModelContext.Budgets.SafeInputTokens {
			break
		}
		capsules = capsules[:len(capsules)-1]
		selectedUnits = selectedUnits[:len(selectedUnits)-1]
	}
	if len(capsules) == 0 {
		r.recordResearchCompactionFailure(ctx, execution, "task_memory", "summarizer_input_exceeded", 0, 0, before.Tokens, 0)
		return models.ModelRequest{}, ContextTokenCount{}, ErrContextBudgetExceeded
	}
	memory, err := generateResearchTaskMemory(ctx, model, execution, capsules, selectedUnits, planJSON, phase, systemPrompt)
	if err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "task_memory", "model_output_invalid", capsules[0].StartCheckpointSeq, capsules[len(capsules)-1].EndCheckpointSeq, before.Tokens, 0)
		return models.ModelRequest{}, ContextTokenCount{}, ErrContextBudgetExceeded
	}
	candidateMemories := append(append([]researchTaskMemory(nil), existing...), memory)
	candidate, err := r.buildDecisionRequest(ctx, execution, prefix, definitions, archived, candidateMemories)
	if err == nil {
		candidate, err = r.FinalizeDecisionRequest(ctx, execution, prefix, candidate)
	}
	if err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "task_memory", "reconstruction_failed", memory.StartCheckpointSeq, memory.EndCheckpointSeq, before.Tokens, 0)
		return models.ModelRequest{}, ContextTokenCount{}, ErrContextBudgetExceeded
	}
	after, err := EstimateModelRequestTokens(candidate)
	if err != nil {
		return models.ModelRequest{}, ContextTokenCount{}, err
	}
	// A Task Memory is the final compaction layer. Validate that its complete
	// candidate request actually fits before making the immutable artifact
	// visible to future projections. Otherwise a failed request could leave a
	// durable memory that is silently applied on the next attempt.
	if after.Tokens > execution.ModelContext.Budgets.SafeInputTokens {
		r.recordResearchCompactionFailure(ctx, execution, "task_memory", "safe_budget_exceeded", memory.StartCheckpointSeq, memory.EndCheckpointSeq, before.Tokens, after.Tokens)
		return models.ModelRequest{}, ContextTokenCount{}, ErrContextBudgetExceeded
	}
	if err := r.appendResearchTaskMemory(ctx, execution, memory); err != nil {
		r.recordResearchCompactionFailure(ctx, execution, "task_memory", "persistence_conflict", memory.StartCheckpointSeq, memory.EndCheckpointSeq, before.Tokens, after.Tokens)
		return models.ModelRequest{}, ContextTokenCount{}, ErrContextBudgetExceeded
	}
	return candidate, after, nil
}

func generateResearchTaskMemory(ctx context.Context, model DecisionModel, execution Execution, capsules []researchArchivalCapsule, units []ContextUnit, planJSON, phase, systemPrompt string) (researchTaskMemory, error) {
	request, err := buildResearchTaskMemoryModelRequest(execution, capsules, units, planJSON, phase, systemPrompt)
	if err != nil {
		return researchTaskMemory{}, err
	}
	outcome, err := model.Decide(ctx, request)
	if err != nil || outcome.ModelDecision.Validate() != nil || outcome.Final == nil {
		return researchTaskMemory{}, errors.New("Research Task Memory model did not return a Final")
	}
	return decodeResearchTaskMemory([]byte(strings.TrimSpace(outcome.Final.Text)), capsules)
}

func buildResearchTaskMemoryModelRequest(execution Execution, capsules []researchArchivalCapsule, units []ContextUnit, planJSON, phase, systemPrompt string) (models.ModelRequest, error) {
	if len(capsules) == 0 || len(capsules) != len(units) || !json.Valid([]byte(planJSON)) || strings.TrimSpace(systemPrompt) == "" {
		return models.ModelRequest{}, errors.New("invalid Research Task Memory input")
	}
	payload := struct {
		SchemaVersion string                        `json:"schema_version"`
		AcceptedPlan  json.RawMessage               `json:"accepted_plan"`
		CurrentPhase  string                        `json:"current_phase"`
		Steps         []researchTaskMemoryModelStep `json:"steps"`
	}{SchemaVersion: "nano.research-task-memory-input@1", AcceptedPlan: json.RawMessage(planJSON), CurrentPhase: phase}
	for index, capsule := range capsules {
		projected, err := applyResearchArchivalCapsules([]ContextUnit{units[index]}, map[int]researchArchivalCapsule{capsule.DecisionNo: capsule})
		if err != nil {
			return models.ModelRequest{}, err
		}
		payload.Steps = append(payload.Steps, researchTaskMemoryModelStep{
			DecisionNo: capsule.DecisionNo, StartCheckpointSeq: capsule.StartCheckpointSeq, EndCheckpointSeq: capsule.EndCheckpointSeq,
			Capsule: capsule.CapsuleJSON, RetainedTrajectory: researchCompactionMessages(projected[0].Messages),
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return models.ModelRequest{}, err
	}
	temperature := 0.0
	maxOutput := execution.ModelInvocation.MaxOutputTokens
	if maxOutput > 16_384 {
		maxOutput = 16_384
	}
	return models.ModelRequest{
		Model:            execution.Model,
		Messages:         []models.ModelMessage{{Role: models.RoleSystem, Content: systemPrompt}, {Role: models.RoleUser, Content: string(encoded)}},
		InvocationPolicy: models.ModelInvocationPolicy{Temperature: &temperature, MaxOutputTokens: maxOutput, Timeout: execution.ModelInvocation.Timeout, EnableThinking: execution.ModelInvocation.EnableThinking},
	}, nil
}

func (r *ResearchRuntime) appendResearchTaskMemory(ctx context.Context, execution Execution, memory researchTaskMemory) error {
	if err := r.CheckAuthority(ctx, execution.Attempt); err != nil {
		return err
	}
	tx, err := r.base.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,0))`, execution.RunID); err != nil {
		return err
	}
	var sessionID string
	if err := tx.QueryRow(ctx, `select id from research_sessions where execution_run_id=nano_research_root_run($1) and status='running' for update`, execution.RunID).Scan(&sessionID); err != nil {
		return err
	}
	id := researchArtifactIdentity("rmem_", execution.RunID, fmt.Sprint(memory.StartCheckpointSeq), fmt.Sprint(memory.EndCheckpointSeq), memory.SourceCapsulesSHA256, execution.ModelContext.Policy.SHA256)
	tag, err := tx.Exec(ctx, `
		insert into research_task_memories(
			id,session_id,run_id,first_decision_no,last_decision_no,start_checkpoint_seq,end_checkpoint_seq,
			source_capsules_sha256,memory_json,memory_sha256,memory_bytes,summarizer_model,prompt_version,
			model_context_policy_identity,model_context_policy_version,model_context_policy_sha256
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13,$14,$15,$16)
		on conflict(run_id,start_checkpoint_seq,end_checkpoint_seq,model_context_policy_identity,model_context_policy_version) do nothing
	`, id, sessionID, execution.RunID, memory.FirstDecisionNo, memory.LastDecisionNo, memory.StartCheckpointSeq, memory.EndCheckpointSeq,
		memory.SourceCapsulesSHA256, string(memory.MemoryJSON), memory.MemorySHA256, len(memory.MemoryJSON), execution.Model,
		researchTaskMemoryPromptVersion, execution.ModelContext.Policy.Identity, execution.ModelContext.Policy.Version, execution.ModelContext.Policy.SHA256)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var storedID, storedSHA, storedSource string
		if err := tx.QueryRow(ctx, `
			select id,memory_sha256,source_capsules_sha256 from research_task_memories
			where run_id=$1 and start_checkpoint_seq=$2 and end_checkpoint_seq=$3
			  and model_context_policy_identity=$4 and model_context_policy_version=$5
		`, execution.RunID, memory.StartCheckpointSeq, memory.EndCheckpointSeq,
			execution.ModelContext.Policy.Identity, execution.ModelContext.Policy.Version).Scan(&storedID, &storedSHA, &storedSource); err != nil {
			return err
		}
		if storedID != id || storedSHA != memory.MemorySHA256 || storedSource != memory.SourceCapsulesSHA256 {
			return errors.New("Research Task Memory persistence conflict")
		}
	}
	return tx.Commit(ctx)
}

func (r *ResearchRuntime) FinalizeDecisionRequest(ctx context.Context, execution Execution, prefix CheckpointPrefix, request models.ModelRequest) (models.ModelRequest, error) {
	if !isThresholdResearchExecution(execution) {
		return request, nil
	}
	return r.base.FinalizeDecisionRequest(ctx, execution, prefix, request)
}

func researchOriginalRequestFromDecisionRequest(request models.ModelRequest) string {
	for _, message := range request.Messages {
		if message.Role == models.RoleUser && strings.HasPrefix(message.Content, "Original request:\n") {
			value := strings.TrimPrefix(message.Content, "Original request:\n")
			if index := strings.Index(value, "\n\nAccepted Research Plan (immutable):\n"); index >= 0 {
				return value[:index]
			}
		}
	}
	return "Research request"
}

func (r *ResearchRuntime) loadResearchArchivalSteps(ctx context.Context, runID string, units []ContextUnit) ([]researchArchivalStep, error) {
	if len(units) == 0 {
		return nil, ErrContextBudgetExceeded
	}
	rows, err := r.pool.Query(ctx, `
		select sequence_no,decision_no,kind,payload_sha256
		from agent_run_checkpoints
		where run_id=$1 and decision_no between $2 and $3
		order by sequence_no
	`, runID, units[0].DecisionNo, units[len(units)-1].DecisionNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type rowValue struct {
		sequence int
		decision int
		kind     string
		sha      string
	}
	byDecision := make(map[int][]rowValue, len(units))
	for rows.Next() {
		var value rowValue
		if err := rows.Scan(&value.sequence, &value.decision, &value.kind, &value.sha); err != nil {
			return nil, err
		}
		byDecision[value.decision] = append(byDecision[value.decision], value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	steps := make([]researchArchivalStep, 0, len(units))
	for _, unit := range units {
		covered := byDecision[unit.DecisionNo]
		if len(covered) != len(unit.Messages) || len(covered) < 2 || covered[0].kind != string(CheckpointActionProposal) {
			return nil, projectionError("Research Step %d checkpoint range changed", unit.DecisionNo)
		}
		digest := sha256.New()
		for index, checkpoint := range covered {
			if index > 0 && checkpoint.kind != string(CheckpointActionResult) {
				return nil, projectionError("Research Step %d checkpoint order changed", unit.DecisionNo)
			}
			fmt.Fprintf(digest, "%d\x00%d\x00%s\x00%s\x00", checkpoint.sequence, checkpoint.decision, checkpoint.kind, checkpoint.sha)
		}
		steps = append(steps, researchArchivalStep{
			DecisionNo: unit.DecisionNo, StartCheckpointSeq: covered[0].sequence,
			EndCheckpointSeq: covered[len(covered)-1].sequence, SourceSHA256: hex.EncodeToString(digest.Sum(nil)),
			Unit: cloneResearchContextUnit(unit),
		})
	}
	return steps, nil
}

func (r *ResearchRuntime) loadResearchCompactionState(ctx context.Context, runID string) (string, string, error) {
	var plan string
	err := r.pool.QueryRow(ctx, `
		select case when subagent.child_run_id is null then plan.plan_json
			else jsonb_build_object('assigned_task',subagent.message,'parent_plan',plan.plan_json) end::text
		from research_sessions session
		join research_plan_versions plan on plan.session_id=session.id and plan.version=session.accepted_plan_version
		left join agent_subagents subagent on subagent.child_run_id=$1
		where session.execution_run_id=nano_research_root_run($1) and session.status in ('running','publishing')
	`, runID).Scan(&plan)
	return plan, "execution", err
}

func (r *ResearchRuntime) loadResearchCompactionPrompts(ctx context.Context, runID string) (string, string, error) {
	var archivalReference, taskMemoryReference string
	if err := r.pool.QueryRow(ctx, `
		select definition.prompt_bindings->>'archival_compactor',definition.prompt_bindings->>'task_memory_compactor'
		from agent_runs run
		join agent_definition_versions definition
		  on definition.definition_identity=run.definition_identity and definition.definition_version=run.definition_version
		where run.id=$1
	`, runID).Scan(&archivalReference, &taskMemoryReference); err != nil {
		return "", "", err
	}
	archival, err := r.resolvePrompt(archivalReference)
	if err != nil {
		return "", "", err
	}
	taskMemory, err := r.resolvePrompt(taskMemoryReference)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(archival.Content), strings.TrimSpace(taskMemory.Content), nil
}

func generateResearchArchivalCapsules(ctx context.Context, model DecisionModel, execution Execution, steps []researchArchivalStep, planJSON, phase, systemPrompt string) ([]researchArchivalCapsule, error) {
	request, err := buildResearchArchivalModelRequest(execution, steps, planJSON, phase, systemPrompt)
	if err != nil {
		return nil, err
	}
	outcome, err := model.Decide(ctx, request)
	if err != nil || outcome.ModelDecision.Validate() != nil || outcome.Final == nil {
		return nil, errors.New("Research Capsule model did not return a Final")
	}
	return decodeResearchCapsuleBatch([]byte(strings.TrimSpace(outcome.Final.Text)), steps)
}

func buildResearchArchivalModelRequest(execution Execution, steps []researchArchivalStep, planJSON, phase, systemPrompt string) (models.ModelRequest, error) {
	payload := struct {
		SchemaVersion string                      `json:"schema_version"`
		AcceptedPlan  json.RawMessage             `json:"accepted_plan"`
		CurrentPhase  string                      `json:"current_phase"`
		Steps         []researchArchivalModelStep `json:"steps"`
	}{SchemaVersion: "nano.research-capsule-input@1", AcceptedPlan: json.RawMessage(planJSON), CurrentPhase: phase}
	if !json.Valid(payload.AcceptedPlan) || len(steps) == 0 || strings.TrimSpace(systemPrompt) == "" {
		return models.ModelRequest{}, errors.New("invalid Research Capsule input")
	}
	for _, step := range steps {
		payload.Steps = append(payload.Steps, researchArchivalModelStep{step.DecisionNo, step.StartCheckpointSeq, step.EndCheckpointSeq, researchCompactionMessages(step.Unit.Messages)})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return models.ModelRequest{}, err
	}
	temperature := 0.0
	maxOutput := execution.ModelInvocation.MaxOutputTokens
	if maxOutput > 16_384 {
		maxOutput = 16_384
	}
	return models.ModelRequest{
		Model:            execution.Model,
		Messages:         []models.ModelMessage{{Role: models.RoleSystem, Content: systemPrompt}, {Role: models.RoleUser, Content: string(encoded)}},
		InvocationPolicy: models.ModelInvocationPolicy{Temperature: &temperature, MaxOutputTokens: maxOutput, Timeout: execution.ModelInvocation.Timeout, EnableThinking: execution.ModelInvocation.EnableThinking},
	}, nil
}

func (r *ResearchRuntime) appendResearchArchivalCapsules(ctx context.Context, execution Execution, capsules []researchArchivalCapsule) error {
	if err := r.CheckAuthority(ctx, execution.Attempt); err != nil {
		return err
	}
	tx, err := r.base.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,0))`, execution.RunID); err != nil {
		return err
	}
	var sessionID string
	if err := tx.QueryRow(ctx, `select id from research_sessions where execution_run_id=nano_research_root_run($1) and status='running' for update`, execution.RunID).Scan(&sessionID); err != nil {
		return err
	}
	for _, capsule := range capsules {
		id := researchArtifactIdentity("rcap_", execution.RunID, fmt.Sprint(capsule.DecisionNo), fmt.Sprint(capsule.StartCheckpointSeq), fmt.Sprint(capsule.EndCheckpointSeq), capsule.SourceSHA256, execution.ModelContext.Policy.SHA256)
		tag, err := tx.Exec(ctx, `
			insert into research_archival_capsules(
				id,session_id,run_id,decision_no,start_checkpoint_seq,end_checkpoint_seq,source_checkpoint_sha256,
				capsule_json,capsule_sha256,capsule_bytes,summarizer_model,prompt_version,
				model_context_policy_identity,model_context_policy_version,model_context_policy_sha256
			) values($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14,$15)
			on conflict(run_id,decision_no) do nothing
		`, id, sessionID, execution.RunID, capsule.DecisionNo, capsule.StartCheckpointSeq, capsule.EndCheckpointSeq,
			capsule.SourceSHA256, string(capsule.CapsuleJSON), capsule.CapsuleSHA256, len(capsule.CapsuleJSON), execution.Model,
			researchArchivalPromptVersion, execution.ModelContext.Policy.Identity, execution.ModelContext.Policy.Version, execution.ModelContext.Policy.SHA256)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var storedID, storedSHA, storedSource string
			if err := tx.QueryRow(ctx, `select id,capsule_sha256,source_checkpoint_sha256 from research_archival_capsules where run_id=$1 and decision_no=$2`, execution.RunID, capsule.DecisionNo).Scan(&storedID, &storedSHA, &storedSource); err != nil {
				return err
			}
			if storedID != id || storedSHA != capsule.CapsuleSHA256 || storedSource != capsule.SourceSHA256 {
				return errors.New("Research Capsule persistence conflict")
			}
		}
	}
	return tx.Commit(ctx)
}

func (r *ResearchRuntime) recordResearchCompactionFailure(ctx context.Context, execution Execution, layer, reason string, start, end, before, after int) {
	if before < 1 || start < 0 || end < start {
		return
	}
	terminalCtx := context.WithoutCancel(ctx)
	tx, err := r.base.workerTx(terminalCtx)
	if err != nil {
		return
	}
	defer tx.Rollback(terminalCtx)
	identity := researchArtifactIdentity("rcfail_", execution.RunID, fmt.Sprint(execution.AttemptNo), layer, reason, fmt.Sprint(start), fmt.Sprint(end), execution.ModelContext.Policy.SHA256)
	if _, err := tx.Exec(terminalCtx, `
		insert into research_compaction_failures(
			id,session_id,run_id,attempt_no,layer,reason_code,start_checkpoint_seq,end_checkpoint_seq,
			before_tokens,after_tokens,model_context_policy_identity,model_context_policy_version,model_context_policy_sha256
		) select $1,session.id,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12
		from research_sessions session where session.execution_run_id=nano_research_root_run($2)
		on conflict(id) do nothing
	`, identity, execution.RunID, execution.AttemptNo, layer, reason, start, end, before, after,
		execution.ModelContext.Policy.Identity, execution.ModelContext.Policy.Version, execution.ModelContext.Policy.SHA256); err == nil {
		_ = tx.Commit(terminalCtx)
	}
}

func researchArtifactIdentity(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + hex.EncodeToString(digest[:])
}

func attachResearchContextTelemetry(request *models.ModelRequest, execution Execution, count ContextTokenCount, trigger string, before, after int) {
	if request == nil {
		return
	}
	status := request.ContextTelemetry
	request.ContextTelemetry = models.ModelContextTelemetry{
		ProviderCapabilityIdentity: execution.ModelContext.Capability.Reference().String(),
		ContextPolicyIdentity:      execution.ModelContext.Policy.Reference().String(),
		ContextWindowTokens:        execution.ModelContext.Capability.ContextWindowTokens,
		ProviderMaxInputTokens:     execution.ModelContext.Capability.MaxInputTokens,
		ProviderMaxOutputTokens:    execution.ModelContext.Capability.MaxOutputTokens,
		PinnedMaxOutputTokens:      execution.ModelContext.Policy.PinnedMaxOutputTokens,
		EstimationSafetyTokens:     execution.ModelContext.Policy.EstimationSafetyTokens,
		HardInputTokens:            execution.ModelContext.Budgets.HardInputTokens,
		SafeInputTokens:            execution.ModelContext.Budgets.SafeInputTokens,
		CompactionTriggerTokens:    execution.ModelContext.Budgets.CompactionTriggerTokens,
		InputTokens:                count.Tokens, InputTokenSource: string(count.Source),
		CompactionTriggerReason: trigger, BeforeCompactionTokens: before, AfterCompactionTokens: after,
		AgentStatusInjected: status.AgentStatusInjected, AgentStatusBytes: status.AgentStatusBytes, AgentStatusTokens: status.AgentStatusTokens,
		TodoRevision: status.TodoRevision, TodoPendingCount: status.TodoPendingCount, TodoInProgressCount: status.TodoInProgressCount,
		TodoCompletedCount: status.TodoCompletedCount, TodoCancelledCount: status.TodoCancelledCount,
		MaxToolInputRepeatCount: status.MaxToolInputRepeatCount,
	}
}

var _ ContextPreparationRuntime = (*ResearchRuntime)(nil)
var _ DecisionRequestFinalizerRuntime = (*ResearchRuntime)(nil)

func selectResearchArchivalSteps(units []ContextUnit, archived map[int]researchArchivalCapsule, keepRecentTokens, maxSteps int) ([]ContextUnit, int, error) {
	if keepRecentTokens < 1 || maxSteps < 1 || len(units) < 2 {
		return nil, 0, ErrContextBudgetExceeded
	}
	if maxSteps > researchArchivalBatchMaxSteps {
		maxSteps = researchArchivalBatchMaxSteps
	}
	tokens := 0
	cut := len(units)
	for index := len(units) - 1; index >= 0; index-- {
		if units[index].Kind != ContextUnitAgentStep || units[index].DecisionNo < 1 {
			return nil, 0, projectionError("Research archival trajectory contains a non-Step unit")
		}
		unitTokens, err := estimateContextUnitTokens(units[index])
		if err != nil {
			return nil, 0, err
		}
		if tokens > 0 && tokens+unitTokens > keepRecentTokens {
			break
		}
		tokens += unitTokens
		cut = index
		if tokens >= keepRecentTokens {
			break
		}
	}
	if cut <= 0 || cut >= len(units) {
		return nil, 0, ErrContextBudgetExceeded
	}
	selected := make([]ContextUnit, 0, maxSteps)
	started := false
	for _, unit := range units[:cut] {
		_, alreadyArchived := archived[unit.DecisionNo]
		if alreadyArchived {
			if started {
				break
			}
			continue
		}
		started = true
		selected = append(selected, cloneResearchContextUnit(unit))
		if len(selected) == maxSteps {
			break
		}
	}
	if len(selected) == 0 {
		return nil, units[cut].DecisionNo, ErrContextBudgetExceeded
	}
	return selected, units[cut].DecisionNo, nil
}

func applyResearchArchivalCapsules(units []ContextUnit, archived map[int]researchArchivalCapsule) ([]ContextUnit, error) {
	projected := make([]ContextUnit, 0, len(units))
	for _, unit := range units {
		capsule, compacted := archived[unit.DecisionNo]
		if !compacted {
			projected = append(projected, cloneResearchContextUnit(unit))
			continue
		}
		if unit.Kind != ContextUnitAgentStep || len(unit.Messages) < 2 || unit.Messages[0].Role != models.RoleAssistant ||
			len(unit.Messages[0].ActionCalls) != len(unit.Messages)-1 || len(capsule.CapsuleJSON) == 0 {
			return nil, projectionError("invalid archived Research Step %d", unit.DecisionNo)
		}
		compactedUnit := ContextUnit{
			Kind: unit.Kind, MessageID: unit.MessageID, RunID: unit.RunID, DecisionNo: unit.DecisionNo,
			Messages: []models.ModelMessage{{
				Role:    models.RoleUser,
				Content: "<research_step_capsule>" + string(capsule.CapsuleJSON) + "</research_step_capsule>",
			}},
		}
		proposal := unit.Messages[0]
		proposal.ActionCalls = cloneModelActionCalls(proposal.ActionCalls)
		compactedUnit.Messages = append(compactedUnit.Messages, proposal)
		for index, original := range unit.Messages[1:] {
			call := proposal.ActionCalls[index]
			if original.Role != models.RoleAction || original.ActionCallID != call.ID {
				return nil, projectionError("Research Step %d lost Tool/Result pairing", unit.DecisionNo)
			}
			var accepted actionResultCheckpointPayload
			decoder := json.NewDecoder(strings.NewReader(original.Content))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&accepted); err != nil || accepted.ActionID != call.ID ||
				(accepted.Status != ActionSucceeded && accepted.Status != ActionDomainError) {
				return nil, projectionError("Research Step %d has invalid accepted Result", unit.DecisionNo)
			}
			errorCode := accepted.ErrorCode
			if errorCode == "" && accepted.Error != nil {
				errorCode = accepted.Error.Code
			}
			shell := struct {
				ActionID     string             `json:"action_id"`
				Status       ActionResultStatus `json:"status"`
				ErrorCode    string             `json:"error_code,omitempty"`
				ContentState string             `json:"content_state"`
				ResultRef    string             `json:"result_ref"`
				ReadTool     string             `json:"read_tool,omitempty"`
				Rehydratable bool               `json:"rehydratable"`
			}{
				ActionID: call.ID, Status: accepted.Status, ErrorCode: errorCode,
				ContentState: "compacted", ResultRef: fmt.Sprintf("run:%s/checkpoint:%s", unit.RunID, call.ID),
				Rehydratable: accepted.Status == ActionSucceeded,
			}
			if shell.Rehydratable {
				shell.ReadTool = ToolResultReadTool
			}
			encoded, err := json.Marshal(shell)
			if err != nil {
				return nil, err
			}
			compactedUnit.Messages = append(compactedUnit.Messages, models.ModelMessage{
				Role: models.RoleAction, ActionCallID: call.ID, Content: string(encoded),
			})
		}
		projected = append(projected, compactedUnit)
	}
	return projected, nil
}

func applyResearchTaskMemories(units []ContextUnit, archived map[int]researchArchivalCapsule, memories []researchTaskMemory) ([]ContextUnit, error) {
	byFirst := make(map[int]researchTaskMemory, len(memories))
	covered := make(map[int]bool)
	for _, memory := range memories {
		if memory.FirstDecisionNo < 1 || memory.LastDecisionNo < memory.FirstDecisionNo || len(memory.MemoryJSON) == 0 {
			return nil, projectionError("invalid Research Task Memory range")
		}
		if _, duplicate := byFirst[memory.FirstDecisionNo]; duplicate {
			return nil, projectionError("duplicate Research Task Memory range")
		}
		byFirst[memory.FirstDecisionNo] = memory
		for decision := memory.FirstDecisionNo; decision <= memory.LastDecisionNo; decision++ {
			if covered[decision] {
				return nil, projectionError("overlapping Research Task Memory range")
			}
			covered[decision] = true
		}
	}
	projected := make([]ContextUnit, 0, len(units))
	for index := 0; index < len(units); {
		unit := units[index]
		if memory, ok := byFirst[unit.DecisionNo]; ok {
			lastIndex := index
			for lastIndex < len(units) && units[lastIndex].DecisionNo <= memory.LastDecisionNo {
				if units[lastIndex].DecisionNo != memory.FirstDecisionNo+(lastIndex-index) || archived[units[lastIndex].DecisionNo].DecisionNo == 0 {
					return nil, projectionError("Research Task Memory is not backed by a contiguous Capsule range")
				}
				lastIndex++
			}
			if lastIndex == index || units[lastIndex-1].DecisionNo != memory.LastDecisionNo {
				return nil, projectionError("Research Task Memory range is absent from trajectory")
			}
			projected = append(projected, ContextUnit{
				Kind: ContextUnitResearchTaskMemory, RunID: unit.RunID, DecisionNo: memory.LastDecisionNo,
				Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "<research_task_memory>" + string(memory.MemoryJSON) + "</research_task_memory>"}},
			})
			index = lastIndex
			continue
		}
		if covered[unit.DecisionNo] {
			return nil, projectionError("Research Task Memory begins outside trajectory")
		}
		archivedUnit, err := applyResearchArchivalCapsules([]ContextUnit{unit}, archived)
		if err != nil {
			return nil, err
		}
		projected = append(projected, archivedUnit...)
		index++
	}
	return projected, nil
}

func decodeResearchCapsuleBatch(payload []byte, steps []researchArchivalStep) ([]researchArchivalCapsule, error) {
	if len(payload) == 0 || len(steps) == 0 || len(steps) > researchArchivalBatchMaxSteps {
		return nil, errors.New("invalid Research Capsule batch")
	}
	var batch researchCapsuleBatch
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&batch); err != nil {
		return nil, errors.New("invalid Research Capsule batch")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || batch.SchemaVersion != "nano.research-capsules@1" || len(batch.Capsules) != len(steps) {
		return nil, errors.New("invalid Research Capsule batch")
	}
	result := make([]researchArchivalCapsule, 0, len(steps))
	for index, content := range batch.Capsules {
		step := steps[index]
		if content.SchemaVersion != "nano.research-capsule@1" || content.DecisionNo != step.DecisionNo ||
			content.StartCheckpointSeq != step.StartCheckpointSeq || content.EndCheckpointSeq != step.EndCheckpointSeq ||
			!boundedResearchCapsuleText(content.ObjectiveAdvanced, 2048, false) ||
			!validResearchCapsuleList(content.Conclusions) || !validResearchCapsuleList(content.Decisions) ||
			!validResearchCapsuleList(content.Constraints) || !validResearchCapsuleList(content.DurableRefs) ||
			!validResearchCapsuleList(content.Contradictions) || !validResearchCapsuleList(content.Verification) ||
			!validResearchCapsuleList(content.FollowUp) {
			return nil, errors.New("invalid Research Capsule content")
		}
		canonical, err := json.Marshal(content)
		if err != nil || len(canonical) > researchArchivalCapsuleMaxBytes {
			return nil, errors.New("Research Capsule exceeds canonical budget")
		}
		result = append(result, researchArchivalCapsule{
			DecisionNo: content.DecisionNo, StartCheckpointSeq: content.StartCheckpointSeq,
			EndCheckpointSeq: content.EndCheckpointSeq, SourceSHA256: step.SourceSHA256,
			CapsuleJSON: canonical, CapsuleSHA256: hashPayload(canonical),
		})
	}
	return result, nil
}

func decodeResearchTaskMemory(payload []byte, capsules []researchArchivalCapsule) (researchTaskMemory, error) {
	if len(payload) == 0 || len(capsules) == 0 {
		return researchTaskMemory{}, errors.New("invalid Research Task Memory")
	}
	var content researchTaskMemoryContent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&content); err != nil {
		return researchTaskMemory{}, errors.New("invalid Research Task Memory")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return researchTaskMemory{}, errors.New("invalid Research Task Memory")
	}
	first := capsules[0]
	last := capsules[len(capsules)-1]
	if content.SchemaVersion != "nano.research-task-memory@1" ||
		content.FirstDecisionNo != first.DecisionNo || content.LastDecisionNo != last.DecisionNo ||
		content.StartCheckpointSeq != first.StartCheckpointSeq || content.EndCheckpointSeq != last.EndCheckpointSeq ||
		!boundedResearchCapsuleText(content.Goal, 4096, false) || !boundedResearchCapsuleText(content.Phase, 1024, false) ||
		!validResearchCapsuleList(content.Conclusions) || !validResearchCapsuleList(content.Decisions) ||
		!validResearchCapsuleList(content.Constraints) || !validResearchCapsuleList(content.DurableRefs) ||
		!validResearchCapsuleList(content.Contradictions) || !validResearchCapsuleList(content.FailedPaths) ||
		!validResearchCapsuleList(content.Verification) || !validResearchCapsuleList(content.ReportState) ||
		!validResearchCapsuleList(content.FollowUp) {
		return researchTaskMemory{}, errors.New("invalid Research Task Memory content")
	}
	for index, capsule := range capsules {
		if capsule.DecisionNo != first.DecisionNo+index || capsule.StartCheckpointSeq < first.StartCheckpointSeq ||
			(index > 0 && capsule.StartCheckpointSeq != capsules[index-1].EndCheckpointSeq+1) {
			return researchTaskMemory{}, errors.New("Research Task Memory Capsule range is not contiguous")
		}
	}
	canonical, err := json.Marshal(content)
	if err != nil || len(canonical) > researchTaskMemoryMaxBytes {
		return researchTaskMemory{}, errors.New("Research Task Memory exceeds canonical budget")
	}
	return researchTaskMemory{
		FirstDecisionNo: first.DecisionNo, LastDecisionNo: last.DecisionNo,
		StartCheckpointSeq: first.StartCheckpointSeq, EndCheckpointSeq: last.EndCheckpointSeq,
		SourceCapsulesSHA256: researchCapsulesSourceSHA(capsules), MemoryJSON: canonical, MemorySHA256: hashPayload(canonical),
	}, nil
}

func researchCapsulesSourceSHA(capsules []researchArchivalCapsule) string {
	digest := sha256.New()
	for _, capsule := range capsules {
		fmt.Fprintf(digest, "%d\x00%d\x00%d\x00%s\x00%s\x00", capsule.DecisionNo, capsule.StartCheckpointSeq,
			capsule.EndCheckpointSeq, capsule.SourceSHA256, capsule.CapsuleSHA256)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validResearchCapsuleList(values []string) bool {
	if values == nil || len(values) > 32 {
		return false
	}
	for _, value := range values {
		if !boundedResearchCapsuleText(value, 1024, true) {
			return false
		}
	}
	return true
}

func boundedResearchCapsuleText(value string, limit int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	return allowEmpty || value != ""
}

func cloneResearchContextUnit(unit ContextUnit) ContextUnit {
	cloned := unit
	cloned.Messages = make([]models.ModelMessage, len(unit.Messages))
	for index, message := range unit.Messages {
		cloned.Messages[index] = message
		cloned.Messages[index].ActionCalls = cloneModelActionCalls(message.ActionCalls)
	}
	return cloned
}

func cloneModelActionCalls(calls []models.ModelActionCall) []models.ModelActionCall {
	cloned := make([]models.ModelActionCall, len(calls))
	for index, call := range calls {
		cloned[index] = call
		cloned[index].Input = append(json.RawMessage(nil), call.Input...)
	}
	return cloned
}
