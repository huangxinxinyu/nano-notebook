package agent

import "testing"

func TestGroundingTraceAttributesDescribeResearchAndSourceMarkerNormalization(t *testing.T) {
	attributes := groundingTraceAttributes(groundingPreparation{
		outcome:              "source_cited",
		research:             researchState{performed: true, complete: true, degraded: false},
		eligibleSourceCount:  2,
		validReferenceCount:  1,
		discardedMarkerCount: 3,
	})
	want := map[string]any{
		TraceKeyGroundingOutcome:           "source_cited",
		TraceKeyGroundingResearchPerformed: true,
		TraceKeyGroundingResearchComplete:  true,
		TraceKeyGroundingResearchDegraded:  false,
		TraceKeyEligibleSourceCount:        int64(2),
		TraceKeyValidSourceReferenceCount:  int64(1),
		TraceKeyDiscardedSourceMarkerCount: int64(3),
	}
	if len(attributes) != len(want) {
		t.Fatalf("attributes=%#v", attributes)
	}
	for _, attribute := range attributes {
		expected, exists := want[attribute.Key]
		if !exists {
			t.Fatalf("unexpected attribute=%#v", attribute)
		}
		switch value := expected.(type) {
		case string:
			if attribute.Value.String != value {
				t.Fatalf("%s=%#v", attribute.Key, attribute.Value)
			}
		case bool:
			if attribute.Value.Bool != value {
				t.Fatalf("%s=%#v", attribute.Key, attribute.Value)
			}
		case int64:
			if attribute.Value.Int64 != value {
				t.Fatalf("%s=%#v", attribute.Key, attribute.Value)
			}
		}
	}
}
