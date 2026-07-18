package collector

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

const CommandPurgeTrace = "purge_trace"

type PurgeStatus string

const (
	PurgeAcknowledged PurgeStatus = "acknowledged"
	PurgeRejected     PurgeStatus = "rejected"
)

type PurgeBatch struct {
	ProtocolVersion int            `json:"protocol_version"`
	BatchID         string         `json:"batch_id"`
	ProducerID      string         `json:"producer_id"`
	CreatedAt       time.Time      `json:"created_at"`
	Commands        []PurgeCommand `json:"commands"`
}

type PurgeCommand struct {
	CommandID      string           `json:"command_id"`
	CommandVersion int              `json:"command_version"`
	Kind           string           `json:"kind"`
	TraceID        agentobs.TraceID `json:"trace_id"`
	RunID          string           `json:"run_id"`
	RequestedAt    time.Time        `json:"requested_at"`
	ProducerID     string           `json:"-"`
}

type PurgeBatchResult struct {
	BatchID  string               `json:"batch_id"`
	Commands []PurgeCommandResult `json:"commands"`
}

type PurgeCommandResult struct {
	TraceID agentobs.TraceID `json:"trace_id"`
	Status  PurgeStatus      `json:"status"`
	Code    string           `json:"code,omitempty"`
}

type PurgeCommandError struct {
	Code string
	Err  error
}

func (e *PurgeCommandError) Error() string {
	if e == nil || e.Err == nil {
		return "Collector purge command rejected"
	}
	return e.Err.Error()
}

func (e *PurgeCommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type PurgeStore interface {
	TombstoneTrace(context.Context, PurgeCommand) error
}

type PurgerConfig struct {
	ProducerID string
	Store      PurgeStore
}

type Purger struct {
	producerID string
	store      PurgeStore
}

func NewPurger(config PurgerConfig) (*Purger, error) {
	if strings.TrimSpace(config.ProducerID) == "" || config.Store == nil {
		return nil, errors.New("Collector Purger configuration is incomplete")
	}
	return &Purger{producerID: config.ProducerID, store: config.Store}, nil
}

func (p *Purger) Purge(ctx context.Context, batch PurgeBatch) (PurgeBatchResult, error) {
	if p == nil || p.store == nil {
		return PurgeBatchResult{}, errors.New("nil Collector Purger")
	}
	if batch.ProtocolVersion != ProtocolVersion || strings.TrimSpace(batch.BatchID) == "" ||
		batch.ProducerID != p.producerID || batch.CreatedAt.IsZero() || len(batch.Commands) == 0 {
		return PurgeBatchResult{}, ErrInvalidBatch
	}
	result := PurgeBatchResult{BatchID: batch.BatchID, Commands: make([]PurgeCommandResult, 0, len(batch.Commands))}
	for _, command := range batch.Commands {
		if !validPurgeCommand(command) {
			result.Commands = append(result.Commands, PurgeCommandResult{
				TraceID: command.TraceID, Status: PurgeRejected, Code: CodeInvalidChunk,
			})
			continue
		}
		command.ProducerID = batch.ProducerID
		if err := p.store.TombstoneTrace(ctx, command); err != nil {
			var commandErr *PurgeCommandError
			if errors.As(err, &commandErr) {
				result.Commands = append(result.Commands, PurgeCommandResult{
					TraceID: command.TraceID, Status: PurgeRejected, Code: commandErr.Code,
				})
				continue
			}
			return PurgeBatchResult{}, err
		}
		result.Commands = append(result.Commands, PurgeCommandResult{TraceID: command.TraceID, Status: PurgeAcknowledged})
	}
	return result, nil
}

func validPurgeCommand(command PurgeCommand) bool {
	return validDescriptorText(command.CommandID, 160) && command.CommandVersion == 1 &&
		command.Kind == CommandPurgeTrace && validDescriptorText(string(command.TraceID), 128) &&
		validDescriptorText(command.RunID, 128) && !command.RequestedAt.IsZero()
}
