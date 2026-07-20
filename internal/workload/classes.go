package workload

import "errors"

type Class string

const (
	AgentInteractive Class = "agent_interactive"
	SourceProcessing Class = "source_processing"
	OfflineEval      Class = "offline_eval"
	Reindex          Class = "reindex"

	TargetInteractiveConcurrency = 10
	DefaultAgentConcurrency      = 6
	DefaultSourceConcurrency     = 4
	DefaultBackgroundConcurrency = 1
)

func ValidateInteractiveCapacity(agent, source int) error {
	if agent < 1 || source < 1 || agent+source > TargetInteractiveConcurrency {
		return errors.New("interactive Workload Class capacity is invalid")
	}
	return nil
}
