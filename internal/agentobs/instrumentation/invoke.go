package instrumentation

import (
	"context"
	"errors"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type Terminal[T any] func(result T, err error) agentobs.SpanEnd

type RecordingPhase string

const (
	RecordingStart    RecordingPhase = "start"
	RecordingLink     RecordingPhase = "link"
	RecordingTerminal RecordingPhase = "terminal"
)

type RecordingError struct {
	Phase RecordingPhase
	Err   error
}

func (e *RecordingError) Error() string {
	return "instrumentation " + string(e.Phase) + " recording failed: " + e.Err.Error()
}

func (e *RecordingError) Unwrap() error { return e.Err }

func Invoke[T any](
	ctx context.Context,
	tracer *agentobs.Tracer,
	start agentobs.SpanStart,
	call func(context.Context) (T, error),
	terminal Terminal[T],
) (T, error) {
	var zero T
	if tracer == nil {
		return zero, errors.New("instrumentation requires a Tracer")
	}
	if call == nil {
		return zero, errors.New("instrumentation requires a wrapped call")
	}
	callContext, _, err := tracer.StartSpan(ctx, start)
	if err != nil {
		return zero, &RecordingError{Phase: RecordingStart, Err: err}
	}
	result, callErr := call(callContext)
	end := defaultTerminal(start.Name, callErr)
	if terminal != nil {
		end = terminal(result, callErr)
		if end.Name == "" {
			end.Name = start.Name
		}
		if end.Status == "" {
			end.Status = statusFromError(callErr)
		}
	}
	recordErr := tracer.EndSpan(callContext, end)
	if recordErr != nil {
		recordErr = &RecordingError{Phase: RecordingTerminal, Err: recordErr}
	}
	return result, errors.Join(callErr, recordErr)
}

func defaultTerminal(name string, err error) agentobs.SpanEnd {
	return agentobs.SpanEnd{Name: name, Status: statusFromError(err)}
}

func statusFromError(err error) agentobs.Status {
	if err == nil {
		return agentobs.StatusOK
	}
	if errors.Is(err, context.Canceled) {
		return agentobs.StatusCancelled
	}
	return agentobs.StatusError
}
