package agent

import (
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

func normalizeTraceRecord(record agentobs.Record) agentobs.Record {
	record.OccurredAt = record.OccurredAt.UTC().Truncate(time.Microsecond)
	return record
}
