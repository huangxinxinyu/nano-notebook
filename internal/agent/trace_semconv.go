package agent

import (
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
)

const TraceSemanticConventionVersion = 1

const (
	ModelPhaseAnswerComposition      = "answer_composition"
	ModelPhaseQueryContextualization = "query_contextualization"
	ModelPhaseResearchQueryExpansion = "research_query_expansion"
)

const (
	TraceSpanAgentExecution         = semconv.AgentExecution
	TraceSpanJobAttempt             = "nano.job.attempt"
	TraceSpanPublication            = "nano.publication"
	TraceSpanGrounding              = "nano.grounding"
	TraceSpanQueryContextualization = "nano.rag.query_contextualization"
	TraceSpanContextCompaction      = "nano.context.compaction"
)

func TraceAttemptStartIdentity(runID string, attemptNo int) string {
	return fmt.Sprintf("run/%s/attempt/%d/start", runID, attemptNo)
}

func TraceActionStartIdentity(runID string, attemptNo int, logicalActionID string) string {
	return fmt.Sprintf("run/%s/attempt/%d/action/%s/start", runID, attemptNo, logicalActionID)
}

func TraceModelStartIdentity(runID string, attemptNo, decisionNo int) string {
	return fmt.Sprintf("run/%s/attempt/%d/model/%d/start", runID, attemptNo, decisionNo)
}

func TraceCompactionStartIdentity(runID string, attemptNo int, predecessorID, triggerReason string) string {
	if predecessorID == "" {
		predecessorID = "root"
	}
	return fmt.Sprintf("run/%s/attempt/%d/context-compaction/%s/%s/start", runID, attemptNo, predecessorID, triggerReason)
}

func TraceQueryContextModelStartIdentity(runID string, attemptNo, decisionNo int) string {
	return fmt.Sprintf("run/%s/attempt/%d/query-context/model/%d/start", runID, attemptNo, decisionNo)
}

func TraceResearchPlanModelStartIdentity(runID string, attemptNo int) string {
	return fmt.Sprintf("run/%s/attempt/%d/research-plan/model/start", runID, attemptNo)
}

func TraceQueryContextStartIdentity(runID string, attemptNo int) string {
	return fmt.Sprintf("run/%s/attempt/%d/query-context/start", runID, attemptNo)
}

const (
	TraceEventRunAdmitted        = "nano.run.admitted"
	TraceEventRunTerminal        = "nano.run.terminal"
	TraceEventLeaseExpired       = "nano.lease.expired"
	TraceEventCheckpointAccepted = "nano.checkpoint.accepted"
	TraceEventCancellation       = "nano.run.cancellation_requested"
	TraceEventDeadlineExpired    = "nano.run.deadline_expired"
	TraceEventRecoveryExhausted  = "nano.run.recovery_exhausted"
	TraceEventRetryAdmitted      = "nano.run.retry_admitted"
	TraceEventAttemptDisposition = "nano.attempt.disposition"
	TraceEventToolFiltered       = "nano.tool.filtered"
	TraceEventDelegationCreated  = "nano.delegation.created"
	TraceEventDelegationTerminal = "nano.delegation.terminal"
	TraceEventDelegationWake     = "nano.delegation.parent_wake"
	TraceEventDelegationConsumed = "nano.delegation.consumed"
	TraceEventPublicationPassed  = "nano.publication.passed"
	TraceEventPublicationFailed  = "nano.publication.failed"
	TraceEventMigrationAdopted   = "nano.migration.adopted"
)

const (
	TraceKeyRunID                        = "nano.run.id"
	TraceKeyRuntimeKind                  = "nano.agent.runtime_kind"
	TraceKeyDefinitionIdentity           = "nano.agent.definition"
	TraceKeyExecutorIdentity             = "nano.agent.executor"
	TraceKeyAgentRole                    = "nano.agent.role"
	TraceKeyConfigurationSetID           = "nano.configuration_set.id"
	TraceKeyConfigurationSetSHA256       = "nano.configuration_set.sha256"
	TraceKeyPromptSetID                  = "nano.prompt_set.id"
	TraceKeyPromptSetSHA256              = "nano.prompt_set.sha256"
	TraceKeyRoleProfileSHA256            = "nano.role_profile.sha256"
	TraceKeyExecutorVersion              = "nano.executor.version"
	TraceKeyRunStatus                    = "nano.run.status"
	TraceKeyRunModel                     = "nano.run.model"
	TraceKeyModelPhase                   = "nano.model.phase"
	TraceKeyQueryContextHistoryPairCount = "nano.rag.query_context.history_pair_count"
	TraceKeyQueryContextFallbackUsed     = "nano.rag.query_context.fallback_used"
	TraceKeyPromptVersion                = "nano.run.prompt_version"
	TraceKeyPromptIdentity               = "nano.prompt.identity"
	TraceKeyPromptVersionNumber          = "nano.prompt.version"
	TraceKeyPromptSHA256                 = "nano.prompt.sha256"
	TraceKeyPromptContract               = "nano.prompt.contract"
	TraceKeyRequestedModel               = "nano.model.requested"
	TraceKeyModelValidationOutcome       = "nano.model.validation_outcome"
	TraceKeyProviderCapability           = "nano.context.provider_capability"
	TraceKeyContextPolicy                = "nano.context.policy"
	TraceKeyContextWindowTokens          = "nano.context.window_tokens"
	TraceKeyProviderMaxInputTokens       = "nano.context.provider_max_input_tokens"
	TraceKeyProviderMaxOutputTokens      = "nano.context.provider_max_output_tokens"
	TraceKeyPinnedMaxOutputTokens        = "nano.context.pinned_max_output_tokens"
	TraceKeyEstimationSafetyTokens       = "nano.context.estimation_safety_tokens"
	TraceKeyHardInputTokens              = "nano.context.hard_input_tokens"
	TraceKeySafeInputTokens              = "nano.context.safe_input_tokens"
	TraceKeyCompactionTriggerTokens      = "nano.context.compaction_trigger_tokens"
	TraceKeyContextInputTokens           = "nano.context.input_tokens"
	TraceKeyContextInputTokenSource      = "nano.context.input_token_source"
	TraceKeyExactSuffixTokens            = "nano.context.exact_suffix_tokens"
	TraceKeyAgedResultCount              = "nano.context.aged_result_count"
	TraceKeySystemPromptSHA256           = "nano.context.system_sha256"
	TraceKeyActionDefinitionsSHA256      = "nano.context.tools_sha256"
	TraceKeyAgedResultBytes              = "nano.context.aged_result_bytes"
	TraceKeyCompactionID                 = "nano.context.compaction_id"
	TraceKeySummarizedThrough            = "nano.context.summarized_through"
	TraceKeyCompactionTriggerReason      = "nano.context.compaction_trigger_reason"
	TraceKeyBeforeCompactionTokens       = "nano.context.before_compaction_tokens"
	TraceKeyAfterCompactionTokens        = "nano.context.after_compaction_tokens"
	TraceKeyOverflowRecoveryAttempt      = "nano.context.overflow_recovery_attempt"
	TraceKeyAgentStatusInjected          = "nano.agent_status.injected"
	TraceKeyAgentStatusBytes             = "nano.agent_status.bytes"
	TraceKeyAgentStatusTokens            = "nano.agent_status.tokens"
	TraceKeyTodoRevision                 = "nano.agent_status.todo_revision"
	TraceKeyTodoPendingCount             = "nano.agent_status.todo.pending"
	TraceKeyTodoInProgressCount          = "nano.agent_status.todo.in_progress"
	TraceKeyTodoCompletedCount           = "nano.agent_status.todo.completed"
	TraceKeyTodoCancelledCount           = "nano.agent_status.todo.cancelled"
	TraceKeyMaxToolInputRepeatCount      = "nano.agent_status.tool.max_exact_repeat"
	TraceKeyToolName                     = "nano.tool.name"
	TraceKeyToolReasonCode               = "nano.tool.reason_code"
	TraceKeyParentRunID                  = "nano.delegation.parent_run_id"
	TraceKeyChildRunID                   = "nano.delegation.child_run_id"
	TraceKeyDelegationState              = "nano.delegation.state"
	TraceKeyDelegationOrdinal            = "nano.delegation.ordinal"
	TraceKeyDelegationDepth              = "nano.delegation.depth"
	TraceKeyParentWoken                  = "nano.delegation.parent_woken"
	TraceKeyJobID                        = "nano.job.id"
	TraceKeyAttemptNumber                = "nano.attempt.number"
	TraceKeyAttemptDisposition           = "nano.attempt.disposition"
	TraceKeyAttemptBackoffMilliseconds   = "nano.attempt.backoff_ms"
	TraceKeyAttemptAbandonedCause        = "nano.attempt.abandoned_cause"
	TraceKeyCheckpointKind               = "nano.checkpoint.kind"
	TraceKeyCheckpointStep               = "nano.checkpoint.step"
	TraceKeyCheckpointOrdinal            = "nano.checkpoint.ordinal"
	TraceKeyCheckpointPayloadSHA256      = "nano.checkpoint.payload_sha256"
	TraceKeyDecisionNumber               = "nano.decision.number"
	TraceKeyActionIndex                  = "nano.action.index"
	TraceKeyErrorCode                    = "nano.error.code"
	TraceKeySearchPurpose                = "nano.rag.search.purpose"
	TraceKeyDenseCompleted               = "nano.rag.dense.completed"
	TraceKeyDenseCandidateCount          = "nano.rag.dense.candidate_count"
	TraceKeyDenseCandidateIDs            = "nano.rag.dense.candidate_ids"
	TraceKeyBM25Completed                = "nano.rag.bm25.completed"
	TraceKeyBM25CandidateCount           = "nano.rag.bm25.candidate_count"
	TraceKeyBM25CandidateIDs             = "nano.rag.bm25.candidate_ids"
	TraceKeyRRFTransitionIDs             = "nano.rag.rrf.candidate_ids"
	TraceKeyEvidenceLoadIDs              = "nano.rag.evidence_load.candidate_ids"
	TraceKeyRerankTransitionIDs          = "nano.rag.rerank.candidate_ids"
	TraceKeyRelevanceFilteredCount       = "nano.rag.relevance_filter.count"
	TraceKeyRelevanceFilteredIDs         = "nano.rag.relevance_filter.candidate_ids"
	TraceKeySelectedEvidenceCount        = "nano.rag.selected_evidence.count"
	TraceKeyRetrievalDegraded            = "nano.rag.retrieval.degraded"
	TraceKeyRetrievalDegradations        = "nano.rag.retrieval.degradations"
	TraceKeyRetrievalCompleteEmpty       = "nano.rag.retrieval.complete_empty"
	TraceKeyDenseDuration                = "nano.rag.dense.duration_ns"
	TraceKeyBM25Duration                 = "nano.rag.bm25.duration_ns"
	TraceKeyRRFDuration                  = "nano.rag.rrf.duration_ns"
	TraceKeyEvidenceLoadDuration         = "nano.rag.evidence_load.duration_ns"
	TraceKeyRerankDuration               = "nano.rag.rerank.duration_ns"
	TraceKeyGroundingOutcome             = "nano.rag.grounding.outcome"
	TraceKeyGroundingResearchPerformed   = "nano.rag.grounding.research_performed"
	TraceKeyGroundingResearchComplete    = "nano.rag.grounding.research_complete"
	TraceKeyGroundingResearchDegraded    = "nano.rag.grounding.research_degraded"
	TraceKeyEligibleSourceCount          = "nano.rag.source_reference.eligible_source_count"
	TraceKeyValidSourceReferenceCount    = "nano.rag.source_reference.valid_count"
	TraceKeyDiscardedSourceMarkerCount   = "nano.rag.source_reference.discarded_marker_count"
)
