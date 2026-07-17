package semconv_test

import (
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
)

func TestAgentOperationNamesAreStableAndRecordable(t *testing.T) {
	names := []string{
		semconv.AgentExecution,
		semconv.ModelCall,
		semconv.AgentAction,
		semconv.Retrieval,
		semconv.MemoryOperation,
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, "agent.") {
			t.Fatalf("operation name %q is outside agent namespace", name)
		}
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate operation name %q", name)
		}
		seen[name] = struct{}{}
		record := agentobs.Record{
			SchemaVersion:             1,
			SemanticConventionVersion: semconv.Version,
			IdentityKey:               "span/1/start",
			Kind:                      agentobs.RecordSpanStarted,
			TraceID:                   "trace-1",
			SpanID:                    "span-1",
			Name:                      name,
			OccurredAt:                time.Unix(1_700_000_000, 0).UTC(),
			PayloadVersion:            1,
		}
		if err := record.Validate(); err != nil {
			t.Fatalf("operation %q is not recordable: %v", name, err)
		}
	}
}

func TestCommonMetadataKeysProduceValidBoundedAttributes(t *testing.T) {
	attributes := []agentobs.Attribute{
		agentobs.String(semconv.OperationNameKey, "chat"),
		agentobs.String(semconv.OperationStatusKey, "completed"),
		agentobs.String(semconv.ErrorKindKey, "timeout"),
		agentobs.Int64(semconv.DurationMillisecondsKey, 12),
		agentobs.String(semconv.ModelProviderKey, "provider-neutral"),
		agentobs.String(semconv.ModelNameKey, "model-a"),
		agentobs.String(semconv.ModelFinishReasonKey, "stop"),
		agentobs.String(semconv.ModelResultKindKey, "final_draft"),
		agentobs.Int64(semconv.TokenInputKey, 10),
		agentobs.Int64(semconv.TokenOutputKey, 5),
		agentobs.Int64(semconv.TokenTotalKey, 15),
		agentobs.Int64(semconv.TokenCachedKey, 2),
		agentobs.Int64(semconv.TokenReasoningKey, 1),
		agentobs.Bool(semconv.CostKnownKey, true),
		agentobs.Float64(semconv.CostAmountKey, 0.001),
		agentobs.String(semconv.CostCurrencyKey, "USD"),
		agentobs.String(semconv.CostSourceKey, "gateway"),
		agentobs.String(semconv.InstrumentationScopeKey, "fixture.models"),
		agentobs.String(semconv.InstrumentationVersionKey, "v0.1.0"),
	}
	record := agentobs.Record{
		SchemaVersion:             1,
		SemanticConventionVersion: semconv.Version,
		IdentityKey:               "event/1",
		Kind:                      agentobs.RecordEvent,
		TraceID:                   "trace-1",
		SpanID:                    "span-1",
		Name:                      "agent.fixture.metadata",
		OccurredAt:                time.Unix(1_700_000_000, 0).UTC(),
		PayloadVersion:            1,
		Attributes:                attributes,
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("common metadata attributes: %v", err)
	}
}

func TestLinkRelationshipsAreDistinct(t *testing.T) {
	relationships := map[string]struct{}{
		semconv.LinkContinues:   {},
		semconv.LinkRetries:     {},
		semconv.LinkRetriedFrom: {},
	}
	if len(relationships) != 3 {
		t.Fatalf("relationship constants are not distinct: %#v", relationships)
	}
}
