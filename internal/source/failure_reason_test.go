package source_test

import (
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/source"
)

func TestSafeFailureReasonCollapsesInternalProcessingErrors(t *testing.T) {
	tests := map[string]string{
		"processing_budget_exceeded": "limits_exceeded",
		"source_object_missing":      "source_unavailable",
		"source_integrity_mismatch":  "source_unavailable",
		"extraction_invalid":         "content_unreadable",
		"projection_invalid":         "indexing_failed",
		"retrieval_unavailable":      "retrieval_unavailable",
		"retry_exhausted":            "processing_interrupted",
		"provider-secret-error":      "processing_failed",
		"":                           "processing_failed",
	}
	for internal, want := range tests {
		if got := source.SafeFailureReason(internal); got != want {
			t.Fatalf("SafeFailureReason(%q) = %q, want %q", internal, got, want)
		}
	}
}
