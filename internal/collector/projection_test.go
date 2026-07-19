package collector_test

import (
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestBuildTraceProjectionProducesTreeTimelineEventsLinksAndKnownAnalysis(t *testing.T) {
	stored := projectionStoredTrace(t, true, true)
	projection, err := collector.BuildTraceProjection(stored)
	if err != nil {
		t.Fatalf("BuildTraceProjection: %v", err)
	}
	if projection.Summary.TraceID != stored.Trace.TraceID || projection.Summary.Status != agentobs.StatusOK || projection.Summary.Active ||
		projection.Summary.DurationNanoseconds == nil || *projection.Summary.DurationNanoseconds != int64(9*time.Second) ||
		projection.Summary.AttemptCount != 1 || len(projection.Summary.Models) != 2 {
		t.Fatalf("Summary = %#v", projection.Summary)
	}
	if projection.Summary.InputTokens == nil || *projection.Summary.InputTokens != 18 ||
		projection.Summary.OutputTokens == nil || *projection.Summary.OutputTokens != 9 ||
		projection.Summary.TotalTokens == nil || *projection.Summary.TotalTokens != 27 ||
		!projection.Summary.Cost.Known || projection.Summary.Cost.Amount == nil || *projection.Summary.Cost.Amount != 0.0042 || projection.Summary.Cost.Currency != "USD" {
		t.Fatalf("Summary analysis = %#v", projection.Summary)
	}
	if len(projection.Spans) != 4 || projection.Spans[0].SpanID != "root-projection" || projection.Spans[1].ParentSpanID != "root-projection" ||
		projection.Spans[2].Name != semconv.ModelCall || projection.Spans[2].ParentSpanID != "attempt-projection" ||
		projection.Spans[2].EndSequence == nil || projection.Spans[2].DurationNanoseconds == nil || projection.Spans[2].Model == nil ||
		projection.Spans[3].Name != semconv.AgentAction {
		t.Fatalf("Spans = %#v", projection.Spans)
	}
	if len(projection.Events) != 1 || projection.Events[0].Name != "nano.run.admitted" ||
		len(projection.Links) != 1 || projection.Links[0].TargetSpanID != "root-projection" {
		t.Fatalf("Events/Links = %#v/%#v", projection.Events, projection.Links)
	}
}

func TestBuildTraceProjectionKeepsUnfinishedAndUnknownValuesExplicit(t *testing.T) {
	projection, err := collector.BuildTraceProjection(projectionStoredTrace(t, false, false))
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Summary.Active || projection.Summary.Status != "" || projection.Summary.EndedAtUnixNano != nil || projection.Summary.DurationNanoseconds != nil {
		t.Fatalf("unfinished Summary = %#v", projection.Summary)
	}
	model := projection.Spans[2]
	if model.EndSequence == nil || model.Model == nil || model.Model.TotalTokens != nil || model.Model.Cost.Known || model.Model.Cost.Amount != nil ||
		projection.Summary.TotalTokens != nil || projection.Summary.Cost.Known || projection.Summary.Cost.Amount != nil {
		t.Fatalf("unknown analysis = span=%#v summary=%#v", model, projection.Summary)
	}
}

func TestBuildTraceProjectionRejectsInvalidReplayReference(t *testing.T) {
	stored := projectionStoredTrace(t, true, true)
	stored.Records[3].Record.Attributes = append(stored.Records[3].Record.Attributes,
		agentobs.String(replay.ModelRequestAttachmentKey, "not-an-attachment-id"),
	)
	if _, err := collector.BuildTraceProjection(stored); err == nil || !strings.Contains(err.Error(), "Replay Attachment reference is invalid") {
		t.Fatalf("BuildTraceProjection error = %v", err)
	}
}

func TestBuildTraceProjectionUsesRootStartAsTraceStart(t *testing.T) {
	stored := projectionStoredTrace(t, true, true)
	want := stored.Records[0].Record.OccurredAt.UnixNano()
	stored.Records[1].Record.OccurredAt = stored.Records[0].Record.OccurredAt.Add(-time.Second)
	projection, err := collector.BuildTraceProjection(stored)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Summary.StartedAtUnixNano != want {
		t.Fatalf("StartedAtUnixNano = %d, want root start %d", projection.Summary.StartedAtUnixNano, want)
	}
}

func TestBuildTraceProjectionUsesAnEmptyModelListForAModelFreeTrace(t *testing.T) {
	traceID := agentobs.TraceID("trace-without-model")
	rootID := agentobs.SpanID("root-without-model")
	started := collectorRecord(traceID, rootID, "model-free/root/start", agentobs.RecordSpanStarted, "agent.execution")
	ended := collectorRecord(traceID, rootID, "model-free/root/end", agentobs.RecordSpanEnded, "agent.execution")
	ended.Status = agentobs.StatusOK
	projection, err := collector.BuildTraceProjection(collector.StoredTrace{
		Trace: collector.TraceDescriptor{
			TraceID: traceID, RunID: "run-without-model", ChatID: "chat-without-model", NotebookID: "notebook-without-model",
			RootSpanID: rootID, AgentName: "nano-research-agent", SchemaVersion: 1, SemanticConventionVersion: 1,
		},
		CommittedThrough: 2,
		Records:          []collector.SequencedRecord{collectorEnvelope(t, 1, started), collectorEnvelope(t, 2, ended)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Summary.Models == nil || len(projection.Summary.Models) != 0 {
		t.Fatalf("model-free Summary models = %#v, want non-nil empty list", projection.Summary.Models)
	}
}

func TestBuildTraceProjectionPreservesCrossTraceRetryLink(t *testing.T) {
	traceID := agentobs.TraceID("trace-retry-projection")
	rootID := agentobs.SpanID("root-retry-projection")
	started := collectorRecord(traceID, rootID, "retry/root/start", agentobs.RecordSpanStarted, "agent.execution")
	retriedFrom := collectorRecord(traceID, rootID, "retry/root/retried-from", agentobs.RecordLink, semconv.LinkRetriedFrom)
	retriedFrom.TargetTraceID = "trace-prior"
	retriedFrom.TargetSpanID = "root-prior"
	ended := collectorRecord(traceID, rootID, "retry/root/end", agentobs.RecordSpanEnded, "agent.execution")
	ended.Status = agentobs.StatusOK
	stored := collector.StoredTrace{
		Trace: collector.TraceDescriptor{
			TraceID: traceID, RunID: "run-retry", ChatID: "chat-retry", NotebookID: "notebook-retry",
			RootSpanID: rootID, AgentName: "nano-research-agent", SchemaVersion: 1, SemanticConventionVersion: 1,
		},
		CommittedThrough: 3,
		Records: []collector.SequencedRecord{
			collectorEnvelope(t, 1, started), collectorEnvelope(t, 2, retriedFrom), collectorEnvelope(t, 3, ended),
		},
	}

	projection, err := collector.BuildTraceProjection(stored)
	if err != nil {
		t.Fatalf("BuildTraceProjection: %v", err)
	}
	if len(projection.Links) != 1 || projection.Links[0].Name != semconv.LinkRetriedFrom ||
		projection.Links[0].TargetTraceID != "trace-prior" || projection.Links[0].TargetSpanID != "root-prior" {
		t.Fatalf("Retry links = %#v", projection.Links)
	}
}

func projectionStoredTrace(t *testing.T, complete, known bool) collector.StoredTrace {
	t.Helper()
	traceID := agentobs.TraceID("trace-projection")
	rootID := agentobs.SpanID("root-projection")
	attemptID := agentobs.SpanID("attempt-projection")
	modelID := agentobs.SpanID("model-projection")
	actionID := agentobs.SpanID("action-projection")
	base := time.Unix(1_700_000_000, 123).UTC()
	record := func(sequence int, kind agentobs.RecordKind, spanID agentobs.SpanID, name string, offset time.Duration) agentobs.Record {
		item := collectorRecord(traceID, spanID, "projection/record/"+time.Duration(sequence).String(), kind, name)
		item.OccurredAt = base.Add(offset)
		return item
	}
	records := make([]collector.SequencedRecord, 0, 10)
	appendRecord := func(item agentobs.Record) {
		records = append(records, collectorEnvelope(t, len(records)+1, item))
	}
	appendRecord(record(1, agentobs.RecordSpanStarted, rootID, "agent.execution", 0))
	appendRecord(record(2, agentobs.RecordEvent, rootID, "nano.run.admitted", time.Second))
	attemptStart := record(3, agentobs.RecordSpanStarted, attemptID, "nano.job.attempt", 2*time.Second)
	attemptStart.ParentSpanID = rootID
	appendRecord(attemptStart)
	modelStart := record(4, agentobs.RecordSpanStarted, modelID, semconv.ModelCall, 3*time.Second)
	modelStart.ParentSpanID = attemptID
	modelStart.Attributes = []agentobs.Attribute{agentobs.String(semconv.ModelNameKey, "requested-model")}
	appendRecord(modelStart)
	modelEnd := record(5, agentobs.RecordSpanEnded, modelID, semconv.ModelCall, 4*time.Second)
	modelEnd.Status = agentobs.StatusOK
	modelEnd.Attributes = []agentobs.Attribute{
		agentobs.String(semconv.ModelNameKey, "selected-model"),
		agentobs.Bool(semconv.CostKnownKey, known),
	}
	if known {
		modelEnd.Attributes = append(modelEnd.Attributes,
			agentobs.Int64(semconv.TokenInputKey, 18), agentobs.Int64(semconv.TokenOutputKey, 9),
			agentobs.Int64(semconv.TokenTotalKey, 27), agentobs.Float64(semconv.CostAmountKey, 0.0042),
			agentobs.String(semconv.CostCurrencyKey, "USD"), agentobs.String(semconv.CostSourceKey, "gateway"),
		)
	}
	appendRecord(modelEnd)
	actionStart := record(6, agentobs.RecordSpanStarted, actionID, semconv.AgentAction, 5*time.Second)
	actionStart.ParentSpanID = attemptID
	actionStart.Attributes = []agentobs.Attribute{agentobs.String(semconv.ActionNameKey, "calculate")}
	appendRecord(actionStart)
	link := record(7, agentobs.RecordLink, actionID, semconv.LinkRetries, 6*time.Second)
	link.TargetTraceID, link.TargetSpanID = traceID, rootID
	appendRecord(link)
	actionEnd := record(8, agentobs.RecordSpanEnded, actionID, semconv.AgentAction, 7*time.Second)
	actionEnd.Status = agentobs.StatusOK
	appendRecord(actionEnd)
	attemptEnd := record(9, agentobs.RecordSpanEnded, attemptID, "nano.job.attempt", 8*time.Second)
	attemptEnd.Status = agentobs.StatusOK
	appendRecord(attemptEnd)
	if complete {
		rootEnd := record(10, agentobs.RecordSpanEnded, rootID, "agent.execution", 9*time.Second)
		rootEnd.Status = agentobs.StatusOK
		appendRecord(rootEnd)
	}
	return collector.StoredTrace{
		Trace: collector.TraceDescriptor{
			TraceID: traceID, RunID: "run-projection", ChatID: "chat-projection", NotebookID: "notebook-projection",
			RootSpanID: rootID, AgentName: "nano-research-agent", SchemaVersion: 1, SemanticConventionVersion: 1,
		},
		CommittedThrough: len(records), Records: records,
	}
}
