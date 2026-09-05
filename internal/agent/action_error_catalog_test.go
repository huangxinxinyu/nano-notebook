package agent

import "testing"

func TestEnrichActionDomainErrorUsesSafeCodeOwnedDetail(t *testing.T) {
	result := enrichActionDomainError(ActionResult{Status: ActionDomainError, ErrorCode: "division_by_zero"})
	if result.ErrorCode != "" || result.Error == nil {
		t.Fatalf("enriched result = %+v", result)
	}
	if result.Error.Kind != "domain" || result.Error.Code != "division_by_zero" ||
		result.Error.Message != "The calculation cannot divide by zero." ||
		result.Error.Suggestion == "" || result.Error.Retryable {
		t.Fatalf("safe detail = %+v", result.Error)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnrichActionDomainErrorHasSafeFallbackForRegisteredLegacyCode(t *testing.T) {
	result := enrichActionDomainError(ActionResult{Status: ActionDomainError, ErrorCode: "custom_domain_failure"})
	if result.Error == nil || result.Error.Code != "custom_domain_failure" || result.Error.Message == "" || result.Error.Suggestion == "" {
		t.Fatalf("safe fallback = %+v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnrichActionDomainErrorPreservesExplicitlySafeDetail(t *testing.T) {
	detail := &ActionError{Kind: "domain", Code: "todo_revision_conflict", Message: "safe", Retryable: true}
	result := enrichActionDomainError(ActionResult{Status: ActionDomainError, Error: detail})
	if result.Error != detail {
		t.Fatalf("explicit detail was replaced: %+v", result)
	}
}

func TestEvidenceScopeUnavailableHasSafeActionableCatalogEntry(t *testing.T) {
	result := enrichActionDomainError(ActionResult{Status: ActionDomainError, ErrorCode: "evidence_scope_unavailable"})
	if result.Error == nil || result.Error.Code != "evidence_scope_unavailable" || result.Error.Message == "" || result.Error.Suggestion == "" {
		t.Fatalf("scoped Evidence error = %#v", result)
	}
	if result.Error.Retryable {
		t.Fatalf("scope locator error must not be retryable: %#v", result.Error)
	}
}
