package instrumentation

import (
	"context"
	"errors"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type Terminal[T any] func(result T, err error) agentobs.SpanEnd

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
		return zero, err
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
