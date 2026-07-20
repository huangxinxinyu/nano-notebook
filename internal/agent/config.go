package agent

import "time"

type RunConfig struct {
	ID                     string
	ActionDecisionLimit    int
	FinalDecisionLimit     int
	ActionLimit            int
	ActionBatchLimit       int
	ActionResultByteLimit  int
	ActionResultsByteLimit int
	Deadline               time.Duration
}
