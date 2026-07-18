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
	TraceKeyRunID          = "nano.run.id"
	TraceKeyRunStatus      = "nano.run.status"
	TraceKeyRunModel       = "nano.run.model"
	TraceKeyPromptVersion  = "nano.run.prompt_version"
	TraceKeyJobID          = "nano.job.id"
	TraceKeyAttemptNumber  = "nano.attempt.number"
	TraceKeyCheckpointKind = "nano.checkpoint.kind"
	TraceKeyDecisionNumber = "nano.decision.number"
	TraceKeyActionIndex    = "nano.action.index"
	TraceKeyErrorCode      = "nano.error.code"
)
