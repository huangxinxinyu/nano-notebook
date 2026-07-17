package agentobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var ErrRuntimeShutdown = errors.New("Agent Observability Runtime is shut down")

var (
	ErrIdentityConflict = errors.New("record identity conflicts with an existing fact")
	ErrLifecycle        = errors.New("record violates Trace lifecycle")
	ErrUnresolvedLink   = errors.New("Link target does not resolve")
	ErrLimitExceeded    = errors.New("Trace limit exceeded")
)

type Exporter interface {
	Export(context.Context, Record) error
	ForceFlush(context.Context) error
	Shutdown(context.Context) error
}

type Recorder interface {
	Record(context.Context, Record) error
}

type DeliveryClass string

const (
	DeliveryRequired   DeliveryClass = "required"
	DeliveryBestEffort DeliveryClass = "best_effort"
)

type Destination struct {
	Name     string
	Class    DeliveryClass
	Exporter Exporter
}

type Diagnostic struct {
	Destination string
	Operation   string
	Err         error
}

type RuntimeConfig struct {
	Destinations []Destination
	OnDiagnostic func(context.Context, Diagnostic)
	RecordLimits *Limits
}

type Runtime struct {
	destinations []Destination
	onDiagnostic func(context.Context, Diagnostic)
	recordLimits Limits
	mu           sync.RWMutex
	shutdown     bool
	shutdownErr  error
}

func NewRuntime(config RuntimeConfig) (*Runtime, error) {
	if len(config.Destinations) == 0 {
		return nil, errors.New("Runtime requires at least one destination")
	}
	seen := make(map[string]struct{}, len(config.Destinations))
	destinations := make([]Destination, len(config.Destinations))
	for index, destination := range config.Destinations {
		destination.Name = strings.TrimSpace(destination.Name)
		if destination.Name == "" || destination.Exporter == nil {
			return nil, errors.New("Runtime destination is incomplete")
		}
		if destination.Class != DeliveryRequired && destination.Class != DeliveryBestEffort {
			return nil, errors.New("Runtime delivery class is invalid")
		}
		if _, duplicate := seen[destination.Name]; duplicate {
			return nil, errors.New("Runtime destination name is duplicated")
		}
		seen[destination.Name] = struct{}{}
		destinations[index] = destination
	}
	onDiagnostic := config.OnDiagnostic
	if onDiagnostic == nil {
		onDiagnostic = func(context.Context, Diagnostic) {}
	}
	recordLimits := DefaultLimits()
	if config.RecordLimits != nil {
		recordLimits = *config.RecordLimits
		if err := recordLimits.Validate(); err != nil {
			return nil, err
		}
	}
	return &Runtime{destinations: destinations, onDiagnostic: onDiagnostic, recordLimits: recordLimits}, nil
}

func (r *Runtime) Record(ctx context.Context, record Record) error {
	if r == nil {
		return errors.New("nil Runtime")
	}
	if err := record.ValidateWithLimits(r.recordLimits); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	if r.shutdown {
		r.mu.RUnlock()
		return ErrRuntimeShutdown
	}
	requiredErr, diagnostics := r.dispatch("export", func(destination Destination) error {
		return destination.Exporter.Export(ctx, cloneRecord(record))
	})
	r.mu.RUnlock()
	r.report(ctx, diagnostics)
	return requiredErr
}

func (r *Runtime) ForceFlush(ctx context.Context) error {
	if r == nil {
		return errors.New("nil Runtime")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	if r.shutdown {
		r.mu.RUnlock()
		return ErrRuntimeShutdown
	}
	requiredErr, diagnostics := r.dispatch("flush", func(destination Destination) error {
		return destination.Exporter.ForceFlush(ctx)
	})
	r.mu.RUnlock()
	r.report(ctx, diagnostics)
	return requiredErr
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return errors.New("nil Runtime")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.shutdown {
		err := r.shutdownErr
		r.mu.Unlock()
		return err
	}
	r.shutdown = true
	requiredErr, diagnostics := r.dispatch("shutdown", func(destination Destination) error {
		return destination.Exporter.Shutdown(ctx)
	})
	r.shutdownErr = requiredErr
	r.mu.Unlock()
	r.report(ctx, diagnostics)
	return requiredErr
}

func (r *Runtime) dispatch(operation string, call func(Destination) error) (error, []Diagnostic) {
	var requiredErr error
	var diagnostics []Diagnostic
	for _, destination := range r.destinations {
		err := call(destination)
		if err == nil {
			continue
		}
		wrapped := fmt.Errorf("%s %s: %w", operation, destination.Name, err)
		if destination.Class == DeliveryRequired {
			requiredErr = errors.Join(requiredErr, wrapped)
			continue
		}
		diagnostics = append(diagnostics, Diagnostic{Destination: destination.Name, Operation: operation, Err: wrapped})
	}
	return requiredErr, diagnostics
}

func (r *Runtime) report(ctx context.Context, diagnostics []Diagnostic) {
	for _, diagnostic := range diagnostics {
		r.onDiagnostic(ctx, diagnostic)
	}
}

func cloneRecord(record Record) Record {
	record.Attributes = append([]Attribute(nil), record.Attributes...)
	return record
}
