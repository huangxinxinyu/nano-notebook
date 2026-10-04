package agent

import (
	"context"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type Execution struct {
	Attempt
	ChatID                 string
	UserID                 string
	InputMessageID         string
	Model                  string
	ModelInvocation        models.ModelInvocationPolicy
	ModelContext           agentcatalog.ResolvedModelContextPolicy
	PromptVersion          string
	AgentConfigID          string
	TimeZone               string
	DeadlineAt             time.Time
	ActionDecisionLimit    int
	FinalDecisionLimit     int
	ActionLimit            int
	ActionBatchLimit       int
	ActionResultByteLimit  int
	ActionResultsByteLimit int
	SelectedSourceCount    int
	MemberRole             string
	ParentRunID            string
	SubagentTask           string
	ExistingChildCount     int

	// ReplayOnly marks an Execution reconstructed by LoadForReplay (offline
	// decision-replay tooling, internal/agenteval) rather than by the live
	// Controller's Load. Code paths that would otherwise re-verify a live
	// worker lease (e.g. loadSearchEvidenceModelCandidates) must check this
	// and use the lease-free replay variant instead — a historical run is
	// never 'running', so the normal lease check would always fail it.
	ReplayOnly bool
}

type Attempt struct {
	JobID      string
	RunID      string
	AttemptNo  int
	LeaseToken string
}

func terminalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}
