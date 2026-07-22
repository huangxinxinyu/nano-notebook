package agent

import (
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
)

const TraceSemanticConventionVersion = 1

const (
	TraceSpanAgentExecution = semconv.AgentExecution
	TraceSpanJobAttempt     = "nano.job.attempt"
	TraceSpanPublication    = "nano.publication"
	TraceSpanGrounding      = "nano.grounding"
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

const (
	TraceEventRunAdmitted        = "nano.run.admitted"
	TraceEventRunTerminal        = "nano.run.terminal"
	TraceEventLeaseExpired       = "nano.lease.expired"
	TraceEventCheckpointAccepted = "nano.checkpoint.accepted"
	TraceEventCancellation       = "nano.run.cancellation_requested"
	TraceEventDeadlineExpired    = "nano.run.deadline_expired"
	TraceEventRecoveryExhausted  = "nano.run.recovery_exhausted"
	TraceEventRetryAdmitted      = "nano.run.retry_admitted"
	TraceEventPublicationPassed  = "nano.publication.passed"
	TraceEventPublicationFailed  = "nano.publication.failed"
	TraceEventMigrationAdopted   = "nano.migration.adopted"
)

const (
	TraceKeyRunID                      = "nano.run.id"
	TraceKeyRunStatus                  = "nano.run.status"
	TraceKeyRunModel                   = "nano.run.model"
	TraceKeyPromptVersion              = "nano.run.prompt_version"
	TraceKeyJobID                      = "nano.job.id"
	TraceKeyAttemptNumber              = "nano.attempt.number"
	TraceKeyCheckpointKind             = "nano.checkpoint.kind"
	TraceKeyDecisionNumber             = "nano.decision.number"
	TraceKeyActionIndex                = "nano.action.index"
	TraceKeyErrorCode                  = "nano.error.code"
	TraceKeySearchPurpose              = "nano.rag.search.purpose"
	TraceKeyDenseCompleted             = "nano.rag.dense.completed"
	TraceKeyDenseCandidateCount        = "nano.rag.dense.candidate_count"
	TraceKeyDenseCandidateIDs          = "nano.rag.dense.candidate_ids"
	TraceKeyBM25Completed              = "nano.rag.bm25.completed"
	TraceKeyBM25CandidateCount         = "nano.rag.bm25.candidate_count"
	TraceKeyBM25CandidateIDs           = "nano.rag.bm25.candidate_ids"
	TraceKeyRRFTransitionIDs           = "nano.rag.rrf.candidate_ids"
	TraceKeyEvidenceLoadIDs            = "nano.rag.evidence_load.candidate_ids"
	TraceKeyRerankTransitionIDs        = "nano.rag.rerank.candidate_ids"
	TraceKeySelectedEvidenceCount      = "nano.rag.selected_evidence.count"
	TraceKeyRetrievalDegraded          = "nano.rag.retrieval.degraded"
	TraceKeyRetrievalDegradations      = "nano.rag.retrieval.degradations"
	TraceKeyRetrievalCompleteEmpty     = "nano.rag.retrieval.complete_empty"
	TraceKeyDenseDuration              = "nano.rag.dense.duration_ns"
	TraceKeyBM25Duration               = "nano.rag.bm25.duration_ns"
	TraceKeyRRFDuration                = "nano.rag.rrf.duration_ns"
	TraceKeyEvidenceLoadDuration       = "nano.rag.evidence_load.duration_ns"
	TraceKeyRerankDuration             = "nano.rag.rerank.duration_ns"
	TraceKeyGroundingOutcome           = "nano.rag.grounding.outcome"
	TraceKeyGroundingResearchPerformed = "nano.rag.grounding.research_performed"
	TraceKeyGroundingResearchComplete  = "nano.rag.grounding.research_complete"
	TraceKeyGroundingResearchDegraded  = "nano.rag.grounding.research_degraded"
	TraceKeyEligibleSourceCount        = "nano.rag.source_reference.eligible_source_count"
	TraceKeyValidSourceReferenceCount  = "nano.rag.source_reference.valid_count"
	TraceKeyDiscardedSourceMarkerCount = "nano.rag.source_reference.discarded_marker_count"
)
