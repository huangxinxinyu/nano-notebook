package agent

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"regexp"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/websearch"
)

type AttemptDisposition string

const (
	AttemptCompleted AttemptDisposition = "completed"
	AttemptWaiting   AttemptDisposition = "waiting"
	AttemptRetryable AttemptDisposition = "retryable"
	AttemptTerminal  AttemptDisposition = "terminal"
	AttemptAbandoned AttemptDisposition = "abandoned"

	AttemptCauseLeaseLost = "lease_lost"
	AttemptCauseCancelled = "cancelled"
)

type AttemptResolution struct {
	Disposition AttemptDisposition
	ErrorCode   string
	Backoff     time.Duration
}

var safeAttemptErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func (r AttemptResolution) Valid() bool {
	switch r.Disposition {
	case AttemptCompleted, AttemptWaiting:
		return r.ErrorCode == "" && r.Backoff == 0
	case AttemptRetryable, AttemptTerminal:
		return safeAttemptErrorCode.MatchString(r.ErrorCode) && r.Backoff >= 0
	case AttemptAbandoned:
		return (r.ErrorCode == AttemptCauseLeaseLost || r.ErrorCode == AttemptCauseCancelled) && r.Backoff == 0
	default:
		return false
	}
}

func ClassifyAttempt(err, contextCause error) AttemptResolution {
	if err == nil {
		return AttemptResolution{Disposition: AttemptCompleted}
	}
	if errors.Is(err, ErrLeaseLost) || errors.Is(contextCause, ErrLeaseLost) {
		return AttemptResolution{Disposition: AttemptAbandoned, ErrorCode: AttemptCauseLeaseLost}
	}
	if errors.Is(err, context.Canceled) || errors.Is(contextCause, context.Canceled) {
		return AttemptResolution{Disposition: AttemptAbandoned, ErrorCode: AttemptCauseCancelled}
	}
	var modelErr *models.ModelError
	if errors.As(err, &modelErr) {
		disposition := AttemptTerminal
		if modelErr.Kind == models.ErrorTimeout || modelErr.Kind == models.ErrorUnavailable {
			disposition = AttemptRetryable
		}
		return AttemptResolution{Disposition: disposition, ErrorCode: string(modelErr.Kind)}
	}
	var toolErr *ToolCallError
	if errors.As(err, &toolErr) && safeAttemptErrorCode.MatchString(toolErr.Code) {
		disposition := AttemptTerminal
		if toolErr.Kind == ToolErrorInfrastructure {
			disposition = AttemptRetryable
		}
		return AttemptResolution{Disposition: disposition, ErrorCode: toolErr.Code}
	}
	switch {
	case errors.Is(err, websearch.ErrTimeout):
		return AttemptResolution{Disposition: AttemptRetryable, ErrorCode: "discovery_timeout"}
	case errors.Is(err, websearch.ErrRateLimited):
		return AttemptResolution{Disposition: AttemptRetryable, ErrorCode: "discovery_rate_limited"}
	case errors.Is(err, websearch.ErrUnavailable):
		return AttemptResolution{Disposition: AttemptRetryable, ErrorCode: "discovery_unavailable"}
	case errors.Is(err, websearch.ErrNotConfigured):
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "discovery_not_configured"}
	case errors.Is(err, websearch.ErrInvalidQuery):
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "discovery_invalid_query"}
	case errors.Is(err, websearch.ErrInvalidResponse):
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "discovery_invalid_response"}
	case errors.Is(err, context.DeadlineExceeded):
		return AttemptResolution{Disposition: AttemptRetryable, ErrorCode: "attempt_timeout"}
	case errors.Is(err, ErrRunDeadlineExceeded):
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "run_deadline_exceeded"}
	case errors.Is(err, ErrCheckpointInvalid):
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "checkpoint_invalid"}
	case errors.Is(err, ErrResearchAuthorityLost):
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "research_authority_lost"}
	default:
		// The Attempt record keeps only this generic code, so log the cause.
		slog.Error("Agent Attempt failed with an unclassified error", "error", err)
		return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "agent_execution_failed"}
	}
}

func AttemptRetryBackoff(attemptNo int, jitterKey string) time.Duration {
	if attemptNo < 1 {
		attemptNo = 1
	}
	backoff := time.Second
	for ordinal := 1; ordinal < attemptNo && backoff < 30*time.Second; ordinal++ {
		backoff *= 2
		if backoff > 30*time.Second {
			return 30 * time.Second
		}
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(jitterKey))
	jitterPermille := int64(750 + hash.Sum32()%501)
	backoff = time.Duration(int64(backoff) * jitterPermille / 1000)
	if backoff > 30*time.Second {
		return 30 * time.Second
	}
	return backoff
}
