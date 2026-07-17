package exampleagent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/exampleagent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
)

func TestIndependentConsumerRunsModelActionModelJourney(t *testing.T) {
	exporter := memory.New()
	runtime, err := agentobs.NewRuntime(agentobs.RuntimeConfig{Destinations: []agentobs.Destination{{
		Name: "memory", Class: agentobs.DeliveryRequired, Exporter: exporter,
	}}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
		Recorder: runtime,
		IDGenerator: &fixtureIDs{
			traceIDs: []agentobs.TraceID{"fixture-trace"},
			spanIDs:  []agentobs.SpanID{"fixture-root", "model-1", "action-1", "model-2"},
		},
		SemanticConventionVersion: semconv.Version,
	})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	model := &scriptedModel{results: []exampleagent.ModelResult{
		{Kind: exampleagent.ResultAction, ActionName: "lookup", Metadata: exampleagent.ModelMetadata{Provider: "fixture", Model: "model-a", InputTokens: 3, OutputTokens: 2}},
		{Kind: exampleagent.ResultOutput, Output: "final answer", Metadata: exampleagent.ModelMetadata{Provider: "fixture", Model: "model-a", InputTokens: 5, OutputTokens: 2}},
	}}
	action := &recordingAction{result: "private action result"}
	agent, err := exampleagent.New(tracer, model, action)
	if err != nil {
		t.Fatalf("New Agent: %v", err)
	}

	output, err := agent.Run(context.Background(), "private user input")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if output != "final answer" || model.calls != 2 || action.calls != 1 {
		t.Fatalf("journey output/model/action = %q/%d/%d", output, model.calls, action.calls)
	}

	records := exporter.Records()
	wantNames := []string{
		semconv.AgentExecution,
		semconv.ModelCall, semconv.ModelCall,
		semconv.AgentAction, semconv.AgentAction,
		semconv.ModelCall, semconv.ModelCall,
		semconv.AgentExecution,
	}
	if len(records) != len(wantNames) {
		t.Fatalf("record count = %d, want %d: %#v", len(records), len(wantNames), records)
	}
	for index, record := range records {
		if record.Name != wantNames[index] {
			t.Fatalf("record %d name = %q, want %q", index, record.Name, wantNames[index])
		}
		if record.Kind == agentobs.RecordSpanStarted && record.SpanID != "fixture-root" && record.ParentSpanID != "fixture-root" {
			t.Fatalf("child %s parent = %s, want fixture-root", record.SpanID, record.ParentSpanID)
		}
		payload, err := record.CanonicalPayload()
		if err != nil {
			t.Fatalf("record %d payload: %v", index, err)
		}
		encoded := string(payload)
		for _, forbidden := range []string{"private user input", "private action result", "final answer"} {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("record %d retained raw content %q: %s", index, forbidden, encoded)
			}
		}
	}
}

type scriptedModel struct {
	results []exampleagent.ModelResult
	calls   int
}

func (m *scriptedModel) Decide(context.Context, string) (exampleagent.ModelResult, error) {
	result := m.results[m.calls]
	m.calls++
	return result, nil
}

type recordingAction struct {
	result string
	calls  int
}

func (a *recordingAction) Execute(context.Context, string) (string, error) {
	a.calls++
	return a.result, nil
}

type fixtureIDs struct {
	traceIDs []agentobs.TraceID
	spanIDs  []agentobs.SpanID
}

func (f *fixtureIDs) NewTraceID() agentobs.TraceID {
	id := f.traceIDs[0]
	f.traceIDs = f.traceIDs[1:]
	return id
}

func (f *fixtureIDs) NewSpanID() agentobs.SpanID {
	id := f.spanIDs[0]
	f.spanIDs = f.spanIDs[1:]
	return id
}
