package collector_test

import (
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestBuildTraceAnalyticsProjectionPromotesVersionedDimensionsWithoutPayloadText(t *testing.T) {
	duration := int64(42)
	cached := int64(7)
	reasoning := int64(3)
	projection := collector.TraceProjection{
		Summary: collector.TraceSummary{TraceID: "trace-analytics", NotebookID: "notebook-a", AgentName: "agent-a", Status: agentobs.StatusError},
		Spans: []collector.SpanProjection{
			{
				TraceID: "trace-analytics", SpanID: "root", Name: semconv.AgentExecution, Status: agentobs.StatusError,
				StartAttributes: []agentobs.Attribute{
					agentobs.String("nano.agent.definition", "chat.leader@1"),
					agentobs.String("nano.run.prompt_version", "prompt-set@4"),
					agentobs.String("nano.configuration_set.id", "configuration@2"),
				},
				EndAttributes: []agentobs.Attribute{
					agentobs.String("nano.run.status", "failed"), agentobs.String("nano.error.code", "action_budget_exhausted"),
				},
			},
			{
				TraceID: "trace-analytics", SpanID: "model", Name: semconv.ModelCall, Status: agentobs.StatusOK, DurationNanoseconds: &duration,
				Model: &collector.ModelAnalysisProjection{
					RequestedModel: "qwen-plus", SelectedModel: "qwen-max", Provider: "aliyun",
					CachedTokens: &cached, ReasoningTokens: &reasoning,
				},
			},
			{
				TraceID: "trace-analytics", SpanID: "tool", Name: semconv.AgentAction, Status: agentobs.StatusError, DurationNanoseconds: &duration,
				StartAttributes: []agentobs.Attribute{agentobs.String(semconv.ActionNameKey, "delegate.research.source-discovery.v1")},
				EndAttributes: []agentobs.Attribute{
					agentobs.String(semconv.ActionNameKey, "delegate.research.source-discovery.v1"),
					agentobs.String(semconv.OperationStatusKey, "domain_error"), agentobs.String(semconv.ErrorKindKey, "provider_unavailable"),
				},
			},
			{
				TraceID: "trace-analytics", SpanID: "rag", Name: semconv.AgentAction, Status: agentobs.StatusOK,
				StartAttributes: []agentobs.Attribute{agentobs.String(semconv.ActionNameKey, "search_evidence")},
				EndAttributes: []agentobs.Attribute{
					agentobs.String("nano.rag.retrieval.degradations", `["reranker_unavailable"]`),
				},
			},
			{
				TraceID: "trace-analytics", SpanID: "grounding", Name: "nano.grounding", Status: agentobs.StatusOK,
				EndAttributes: []agentobs.Attribute{agentobs.String("nano.rag.grounding.outcome", "source_cited")},
			},
		},
		Events: []collector.EventProjection{{
			TraceID: "trace-analytics", Name: "nano.delegation.terminal",
			Attributes: []agentobs.Attribute{agentobs.String("nano.delegation.state", "failed")},
		}},
	}

	analytics := collector.BuildTraceAnalyticsProjection(projection)
	if !reflect.DeepEqual(analytics.Providers, []string{"aliyun"}) || analytics.CachedTokens == nil || *analytics.CachedTokens != 7 ||
		analytics.ReasoningTokens == nil || *analytics.ReasoningTokens != 3 || analytics.ErrorCode != "action_budget_exhausted" ||
		analytics.StopReason != "action_budget_exhausted" || analytics.AgentDefinition != "chat.leader@1" ||
		analytics.PromptVersion != "prompt-set@4" || analytics.ConfigurationVersion != "configuration@2" {
		t.Fatalf("trace analytics dimensions=%#v", analytics)
	}
	if !reflect.DeepEqual(analytics.DelegationTargets, []string{"delegate.research.source-discovery.v1"}) ||
		!reflect.DeepEqual(analytics.DelegationOutcomes, []string{"failed"}) ||
		!reflect.DeepEqual(analytics.RAGStages, []string{"grounding", "retrieval"}) ||
		!reflect.DeepEqual(analytics.RAGDegradations, []string{"reranker_unavailable"}) ||
		!reflect.DeepEqual(analytics.CitationOutcomes, []string{"source_cited"}) {
		t.Fatalf("behavior dimensions=%#v", analytics)
	}
	if len(analytics.Spans) != 4 || analytics.Spans[1].ToolName != "delegate.research.source-discovery.v1" ||
		analytics.Spans[1].ErrorCode != "provider_unavailable" || analytics.Spans[1].Outcome != "domain_error" ||
		analytics.Spans[0].Provider != "aliyun" || analytics.Spans[0].CachedTokens == nil {
		t.Fatalf("span analytics=%#v", analytics.Spans)
	}
}

func TestBuildTraceAnalyticsProjectionLeavesUnknownValuesUnknown(t *testing.T) {
	analytics := collector.BuildTraceAnalyticsProjection(collector.TraceProjection{
		Summary: collector.TraceSummary{TraceID: "trace-unknown", NotebookID: "notebook-a"},
		Spans:   []collector.SpanProjection{{TraceID: "trace-unknown", SpanID: "action", Name: semconv.AgentAction}},
	})
	if analytics.CachedTokens != nil || analytics.ReasoningTokens != nil || analytics.ErrorCode != "" || analytics.StopReason != "" ||
		len(analytics.Spans) != 1 || analytics.Spans[0].Outcome != "" || analytics.Spans[0].Retryable != nil {
		t.Fatalf("unknown dimensions were coerced: %#v", analytics)
	}
}

func TestBuildTraceAnalyticsProjectionCarriesPrefixCacheDiagnostics(t *testing.T) {
	input, cached := int64(60000), int64(41000)
	projection := collector.TraceProjection{
		Summary: collector.TraceSummary{TraceID: "trace-prefix", NotebookID: "notebook-a", AgentName: "agent-a", Status: agentobs.StatusOK},
		Spans: []collector.SpanProjection{{
			TraceID: "trace-prefix", SpanID: "model-7", Name: semconv.ModelCall, Status: agentobs.StatusOK, StartedAtUnixNano: 1700,
			StartAttributes: []agentobs.Attribute{
				agentobs.Int64(semconv.DecisionOrdinalKey, 7),
				agentobs.String("nano.context.system_sha256", "system-hash"),
				agentobs.String("nano.context.tools_sha256", "tools-hash"),
			},
			Model: &collector.ModelAnalysisProjection{InputTokens: &input, CachedTokens: &cached},
		}},
	}
	span := collector.BuildTraceAnalyticsProjection(projection).Spans[0]
	if span.InputTokens == nil || *span.InputTokens != 60000 || span.CachedTokens == nil || *span.CachedTokens != 41000 ||
		span.DecisionOrdinal == nil || *span.DecisionOrdinal != 7 || span.StartedAtUnixNano != 1700 ||
		span.SystemSHA256 != "system-hash" || span.ToolsSHA256 != "tools-hash" {
		t.Fatalf("span=%+v", span)
	}
}
