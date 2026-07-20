package sourceprocessing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
)

type TraceSink interface {
	Offer(context.Context, agentbatch.Envelope) error
}

type sourceProcessingTrace struct {
	descriptor  collector.TraceDescriptor
	sink        TraceSink
	records     []agentobs.Record
	currentName string
	currentSpan agentobs.SpanID
	failureCode string
	format      string
	adapterID   string
	configID    string
	coverage    *normalize.Coverage
	blockCount  int
}

func newSourceProcessingTrace(sink TraceSink, lease sourcejobs.Lease, config Config) *sourceProcessingTrace {
	if sink == nil {
		return nil
	}
	traceID := agentobs.TraceID(uuid.NewString())
	workloadID := fmt.Sprintf("%s/attempt-%d", strings.TrimSpace(lease.ID), lease.AttemptNo)
	rootID := stableSourceSpanID(traceID, "root")
	adapterID := strings.TrimSpace(config.ExtractorAdapterID)
	if adapterID == "" {
		adapterID = "unspecified"
	}
	trace := &sourceProcessingTrace{
		descriptor: collector.TraceDescriptor{
			TraceID: traceID, WorkloadKind: collector.WorkloadSourceProcessing, WorkloadID: workloadID,
			NotebookID: lease.NotebookID, RootSpanID: rootID, AgentName: "nano-source-processor",
			SchemaVersion: 1, SemanticConventionVersion: 1,
		},
		sink: sink, adapterID: adapterID, configID: config.ExtractionConfigID,
	}
	trace.records = append(trace.records, trace.record(agentobs.RecordSpanStarted, rootID, "", "source.processing", "root/start", "", []agentobs.Attribute{
		agentobs.Int64("source.processing.attempt_no", int64(lease.AttemptNo)),
		agentobs.String("source.extractor.adapter_id", adapterID),
		agentobs.String("source.extraction_config_id", config.ExtractionConfigID),
	}))
	trace.moveStage("source.validating")
	return trace
}

func (t *sourceProcessingTrace) setFormat(format string) {
	if t != nil {
		t.format = strings.TrimSpace(format)
	}
}

func (t *sourceProcessingTrace) setCoverage(artifact normalize.Artifact) {
	if t == nil {
		return
	}
	coverage := artifact.Coverage
	t.coverage = &coverage
	t.blockCount = len(artifact.Blocks)
}

func (t *sourceProcessingTrace) markFailure(code string) {
	if t != nil {
		t.failureCode = strings.TrimSpace(code)
	}
}

func (t *sourceProcessingTrace) moveStage(name string) {
	if t == nil {
		return
	}
	if t.currentSpan != "" {
		t.records = append(t.records, t.record(agentobs.RecordSpanEnded, t.currentSpan, "", t.currentName, t.currentName+"/end", agentobs.StatusOK, nil))
	}
	t.currentName = name
	t.currentSpan = stableSourceSpanID(t.descriptor.TraceID, name)
	t.records = append(t.records, t.record(agentobs.RecordSpanStarted, t.currentSpan, t.descriptor.RootSpanID, name, name+"/start", "", nil))
}

func (t *sourceProcessingTrace) finish(ctx context.Context, processErr error) {
	if t == nil {
		return
	}
	status := agentobs.StatusOK
	failureCode := t.failureCode
	if failureCode == "" && processErr != nil {
		failureCode = safeSourceProcessingFailure(processErr)
	}
	if failureCode != "" {
		status = agentobs.StatusError
	}
	if errors.Is(processErr, context.Canceled) || errors.Is(processErr, context.DeadlineExceeded) {
		status = agentobs.StatusCancelled
	}
	terminalAttributes := t.terminalAttributes(failureCode)
	if t.currentSpan != "" {
		t.records = append(t.records, t.record(agentobs.RecordSpanEnded, t.currentSpan, "", t.currentName, t.currentName+"/end", status, terminalAttributes))
	}
	t.records = append(t.records, t.record(agentobs.RecordSpanEnded, t.descriptor.RootSpanID, "", "source.processing", "root/end", status, terminalAttributes))
	if ctx == nil {
		ctx = context.Background()
	}
	deliveryCtx := context.WithoutCancel(ctx)
	for _, record := range t.records {
		if record.Validate() != nil {
			continue
		}
		_ = t.sink.Offer(deliveryCtx, agentbatch.Envelope{Trace: t.descriptor, Record: record})
	}
}

func (t *sourceProcessingTrace) terminalAttributes(failureCode string) []agentobs.Attribute {
	attributes := []agentobs.Attribute{
		agentobs.String("source.extractor.adapter_id", t.adapterID),
		agentobs.String("source.extraction_config_id", t.configID),
	}
	if t.format != "" {
		attributes = append(attributes, agentobs.String("source.format", t.format))
	}
	if t.coverage != nil {
		attributes = append(attributes,
			agentobs.String("source.coverage.status", t.coverage.Status),
			agentobs.Int64("source.coverage.total_runes", int64(t.coverage.TotalRunes)),
			agentobs.Int64("source.coverage.gap_count", int64(len(t.coverage.Gaps))),
			agentobs.Int64("source.block_count", int64(t.blockCount)),
		)
	}
	if failureCode != "" {
		attributes = append(attributes, agentobs.String("source.failure.code", failureCode))
	}
	return attributes
}

func (t *sourceProcessingTrace) record(kind agentobs.RecordKind, spanID, parentID agentobs.SpanID, name, identitySuffix string, status agentobs.Status, attributes []agentobs.Attribute) agentobs.Record {
	return agentobs.Record{
		SchemaVersion: 1, SemanticConventionVersion: 1,
		IdentityKey: fmt.Sprintf("source/%s/%s/%s", t.descriptor.WorkloadID, t.descriptor.TraceID, identitySuffix),
		Kind:        kind, TraceID: t.descriptor.TraceID, SpanID: spanID, ParentSpanID: parentID,
		Name: name, OccurredAt: time.Now().UTC(), Status: status, PayloadVersion: 1,
		Attributes: append([]agentobs.Attribute(nil), attributes...),
	}
}

func stableSourceSpanID(traceID agentobs.TraceID, identity string) agentobs.SpanID {
	digest := sha256.Sum256([]byte(string(traceID) + "\x00" + identity))
	return agentobs.SpanID(hex.EncodeToString(digest[:16]))
}

func safeSourceProcessingFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case errors.Is(err, sourcejobs.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, sourcejobs.ErrTransitionConflict):
		return "transition_conflict"
	default:
		return "dependency_unavailable"
	}
}
