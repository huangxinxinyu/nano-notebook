package agent

// actionErrorCatalog is the model-visible boundary for legacy domain-error
// codes. Its text is deliberately static: raw provider, filesystem, stack, or
// request details must never be copied into Agent Status.
var actionErrorCatalog = map[string]ActionError{
	"division_by_zero": {
		Kind: "domain", Code: "division_by_zero",
		Message: "The calculation cannot divide by zero.", Suggestion: "Use a non-zero divisor or choose another operation.",
	},
	"invalid_decimal": {
		Kind: "domain", Code: "invalid_decimal",
		Message: "A calculation operand is not a supported decimal.", Suggestion: "Use canonical decimal strings without exponents.",
	},
	"invalid_operand_count": {
		Kind: "domain", Code: "invalid_operand_count",
		Message: "The calculation requires exactly two operands.", Suggestion: "Provide exactly two decimal operands.",
	},
	"unsupported_operation": {
		Kind: "domain", Code: "unsupported_operation",
		Message: "The requested calculation operation is unsupported.", Suggestion: "Use add, subtract, multiply, or divide.",
	},
	"calculation_result_too_large": {
		Kind: "domain", Code: "calculation_result_too_large",
		Message: "The calculation result exceeds the supported size.", Suggestion: "Split the calculation into smaller bounded steps.",
	},
	"invalid_time_zone": {
		Kind: "domain", Code: "invalid_time_zone",
		Message: "The requested time zone is invalid.", Suggestion: "Use a valid IANA time-zone name such as Asia/Shanghai.",
	},
	"retrieval_unavailable": {
		Kind: "domain", Code: "retrieval_unavailable", Retryable: true,
		Message: "Selected-source retrieval is currently unavailable.", Suggestion: "Retry once later or continue without claiming source support.",
	},
	"evidence_scope_unavailable": {
		Kind: "domain", Code: "evidence_scope_unavailable",
		Message: "The requested Evidence scope is unavailable in this Research Run.", Suggestion: "Inspect an available Source again and use the exact source_id and entry_id it returns.",
	},
	"web_search_unavailable": {
		Kind: "domain", Code: "web_search_unavailable", Retryable: true,
		Message: "Web search is currently unavailable.", Suggestion: "Retry later or use another available evidence source.",
	},
	"web_search_timeout": {
		Kind: "domain", Code: "web_search_timeout", Retryable: true,
		Message: "Web search did not finish before its deadline.", Suggestion: "Retry with fewer or narrower queries.",
	},
	"web_search_rate_limited": {
		Kind: "domain", Code: "web_search_rate_limited", Retryable: true,
		Message: "Web search is temporarily rate limited.", Suggestion: "Wait before retrying or use another available source.",
	},
	"web_search_invalid_response": {
		Kind: "domain", Code: "web_search_invalid_response", Retryable: true,
		Message: "Web search returned an unusable response.", Suggestion: "Retry once with a simpler query or use another source.",
	},
	"read_url_unavailable": {
		Kind: "domain", Code: "read_url_unavailable", Retryable: true,
		Message: "Page reading is currently unavailable.", Suggestion: "Retry later or use another accessible source.",
	},
	"read_url_failed": {
		Kind: "domain", Code: "read_url_failed", Retryable: true,
		Message: "The requested page could not be read.", Suggestion: "Verify the URL or use another accessible source.",
	},
	"tool_result_expired": {
		Kind: "domain", Code: "tool_result_expired",
		Message: "The recent Tool Result is no longer available.", Suggestion: "Repeat the original read-only Tool Call if the content is still needed.",
	},
	"tool_result_unauthorized": {
		Kind: "authorization", Code: "tool_result_unauthorized",
		Message: "The Tool Result does not belong to the current user, Chat, and Run.", Suggestion: "Use only result references returned in this Research Run.",
	},
	"tool_result_corrupt": {
		Kind: "infrastructure", Code: "tool_result_corrupt",
		Message: "The cached Tool Result failed integrity validation.", Suggestion: "Repeat the original read-only Tool Call if the content is still needed.",
	},
	"tool_result_range_invalid": {
		Kind: "domain", Code: "tool_result_range_invalid",
		Message: "The requested Tool Result byte range is invalid.", Suggestion: "Use offset zero or the exact next_offset returned by the previous page.",
	},
	"tool_result_read_failed": {
		Kind: "infrastructure", Code: "tool_result_read_failed", Retryable: true,
		Message: "The recent Tool Result cache could not be read.", Suggestion: "Retry once or repeat the original read-only Tool Call.",
	},
	"skill_not_allowed": {
		Kind: "domain", Code: "skill_not_allowed",
		Message: "The requested skill is not allowed for this Agent.", Suggestion: "Use one of the skills exposed to the current Agent.",
	},
	"skill_not_found": {
		Kind: "domain", Code: "skill_not_found",
		Message: "The requested skill could not be found.", Suggestion: "Check the available skill names and choose one of them.",
	},
	ErrorActionInterrupted: {
		Kind: "domain", Code: ErrorActionInterrupted, Retryable: true,
		Message: "The previous tool execution was interrupted before its result was accepted.", Suggestion: "Inspect current state before deciding whether a safe retry is appropriate.",
	},
}

func enrichActionDomainError(result ActionResult) ActionResult {
	if result.Status != ActionDomainError || result.Error != nil || result.ErrorCode == "" {
		return result
	}
	detail, ok := actionErrorCatalog[result.ErrorCode]
	if !ok {
		detail = ActionError{
			Kind: "domain", Code: result.ErrorCode,
			Message:    "The tool could not complete the requested operation.",
			Suggestion: "Review the tool input and choose a different safe next action.",
		}
	}
	result.ErrorCode = ""
	result.Error = &detail
	return result
}
