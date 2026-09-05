package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/skillcatalog"
)

func TestResearchWorkflowSkillIsMandatoryOnlyForThresholdRuntime(t *testing.T) {
	catalog := skillcatalog.MustLoadEmbedded()
	guidanceV1, err := researchWorkflowSkillPrompt(Execution{AgentConfigID: "research.executor@16"}, catalog)
	if err != nil || !strings.Contains(guidanceV1, "rewrite_todo_list") || !strings.Contains(guidanceV1, "navigation only") || strings.Contains(guidanceV1, "entry_id") {
		t.Fatalf("v1 guidance=%q err=%v", guidanceV1, err)
	}
	guidanceV2, err := researchWorkflowSkillPrompt(Execution{AgentConfigID: "research.executor@17"}, catalog)
	if err != nil || !strings.Contains(guidanceV2, "source_id") || !strings.Contains(guidanceV2, "entry_id") {
		t.Fatalf("v2 guidance=%q err=%v", guidanceV2, err)
	}
	legacy, err := researchWorkflowSkillPrompt(Execution{AgentConfigID: "research.executor@9"}, catalog)
	if err != nil || legacy != "" {
		t.Fatalf("legacy guidance=%q err=%v", legacy, err)
	}
}

func TestBuildResearchRoutingDirectivePreventsRepeatedDiscoveryAndReads(t *testing.T) {
	directive := buildResearchRoutingDirective(41, 5, 1,
		[]string{"https://github.com/openai/codex", "https://github.com/anthropics/claude-code"},
		[]string{"Claude Code architecture | Codex architecture", "DeepSeek harness"},
	)
	for _, required := range []string{
		"47 total unique URLs: 41 discovered-only, 5 successfully read, 1 failed",
		"Do not call `read_url` again",
		"https://github.com/openai/codex",
		"Do not repeat these recent `web_search` query batches",
		"prioritize substantive `read_url` calls",
	} {
		if !strings.Contains(directive, required) {
			t.Fatalf("directive missing %q:\n%s", required, directive)
		}
	}
}

func TestCanonicalizeResearchEvidenceClaimsUsesLedgerAuthority(t *testing.T) {
	report := "# 研究报告\n\n共收录 214 个 URL，其中 **32 个为成功读取的一手材料**（远超目标）。\n\n**证据账本状态**：共 214 个 URL，其中 **32 个为成功读取的一手材料**。"
	got, corrections := canonicalizeResearchEvidenceClaims(report, 214, 204, 8, 2)
	for _, required := range []string{
		"204 个仅发现、8 个成功读取、2 个读取失败",
		"其中 8 个为成功读取的一手材料",
		"证据账本状态（系统核验）",
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("report missing %q:\n%s", required, got)
		}
	}
	if corrections != 3 || strings.Contains(got, "32 个为成功读取") {
		t.Fatalf("corrections=%d report=%s", corrections, got)
	}
	if strings.Contains(got, "系统核验的证据账本（权威）") {
		t.Fatalf("code-owned coverage telemetry was injected into the report body: %s", got)
	}
}

func TestHydrateExternalizedResearchProposalRestoresScopedBody(t *testing.T) {
	body := []byte(`{"engine":"lightweight","final_url":"https://example.com/paper","markdown":"full primary evidence"}`)
	envelope := testToolResultEnvelope(body)
	projection, err := json.Marshal(ToolResultProjection{
		ContentState: ToolResultExternalized, ResultRef: envelope.ResultRef,
		ResultBytes: len(body), SHA256: envelope.SHA256, ReadTool: ToolResultReadTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := AcceptedProposal{DecisionNo: 1, Actions: []AcceptedAction{{
		ActionID: envelope.ActionID, Name: "read_url", Result: &ActionResult{Status: ActionSucceeded, Output: projection},
	}}}
	reader := ToolResultReader{Store: &recordingToolResultStore{envelopes: []ToolResultEnvelope{envelope}}, MaximumPageBytes: 11, Now: testToolResultNow}

	hydrated, err := hydrateExternalizedResearchProposal(context.Background(), reader, ToolResultScope{
		UserID: envelope.UserID, ChatID: envelope.ChatID, RunID: envelope.RunID,
	}, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(hydrated.Actions[0].Result.Output); got != string(body) {
		t.Fatalf("hydrated body=%q want=%q", got, body)
	}
	if got := string(proposal.Actions[0].Result.Output); got == string(body) {
		t.Fatal("hydration mutated the checkpoint projection")
	}
}

func TestHydrateExternalizedResearchProposalKeepsProjectionAfterExpiry(t *testing.T) {
	projection, err := json.Marshal(ToolResultProjection{
		ContentState: ToolResultExternalized, ResultRef: "tr_expired_result",
		ResultBytes: 100000, SHA256: strings.Repeat("a", 64), ReadTool: ToolResultReadTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := AcceptedProposal{DecisionNo: 1, Actions: []AcceptedAction{{
		ActionID: "decision:1/action:0", Name: "read_url", Result: &ActionResult{Status: ActionSucceeded, Output: projection},
	}}}
	reader := ToolResultReader{Store: &recordingToolResultStore{}, MaximumPageBytes: 16, Now: testToolResultNow}

	hydrated, err := hydrateExternalizedResearchProposal(context.Background(), reader, ToolResultScope{
		UserID: "user_a", ChatID: "chat_a", RunID: "run_a",
	}, proposal)
	if err != nil {
		t.Fatalf("expired ephemeral result must not block compaction: %v", err)
	}
	if got := string(hydrated.Actions[0].Result.Output); got != string(projection) {
		t.Fatalf("expired projection changed: %s", got)
	}
}

func TestResearchActionFailureReasonUsesStructuredDomainErrorCode(t *testing.T) {
	result := ActionResult{Status: ActionDomainError, Error: &ActionError{
		Kind: "domain", Code: "read_url_failed", Message: "safe", Suggestion: "try another source",
	}}
	if got := researchActionFailureReason(result); got != "read_url_failed" {
		t.Fatalf("failure reason=%q", got)
	}
}

func TestCanonicalizeResearchEvidenceClaimsDoesNotAddOperationalBoilerplate(t *testing.T) {
	got, corrections := canonicalizeResearchEvidenceClaims("# Decision\n\nAdopt A for the pilot.", 40, 25, 12, 3)
	if corrections != 0 || got != "# Decision\n\nAdopt A for the pilot." {
		t.Fatalf("corrections=%d report=%q", corrections, got)
	}
}

func TestCombineResearchRollupRetainsPreviousProjection(t *testing.T) {
	got := combineResearchRollup("# Research Rollup\n\nold fact", []string{"# Agent Step 9\n\nnew fact"})
	if !strings.Contains(got, "old fact") || !strings.Contains(got, "new fact") {
		t.Fatalf("rollup lost history:\n%s", got)
	}
}

func TestConsecutiveResearchDuplicateStepsTriggersReportRecovery(t *testing.T) {
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{{Result: &ActionResult{Status: ActionSucceeded, Output: []byte(`{"ok":true}`)}}}},
		{DecisionNo: 2, Actions: []AcceptedAction{{Result: &ActionResult{Status: ActionDomainError, ErrorCode: "research_duplicate_action"}}}},
		{DecisionNo: 3, Actions: []AcceptedAction{{Result: &ActionResult{Status: ActionDomainError, ErrorCode: "research_duplicate_action"}}}},
		{DecisionNo: 4, Actions: []AcceptedAction{{Result: &ActionResult{Status: ActionDomainError, ErrorCode: "research_duplicate_action"}}}},
	}}
	if got := consecutiveResearchDuplicateSteps(prefix); got != 3 {
		t.Fatalf("duplicate steps=%d", got)
	}
}

func TestPlanResearchDuplicateRecoverySuggestsUnreadEvidenceWithoutSchemaDeadlock(t *testing.T) {
	definitions := []models.ActionDefinition{
		{Name: "web_search"},
		{Name: "read_url", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`)},
		{Name: "search_evidence"},
	}
	recovery := planResearchDuplicateRecovery(1, []string{
		"https://github.com/openai/codex",
		"https://docs.anthropic.com/en/docs/claude-code",
	}, definitions)
	if len(recovery.definitions) != len(definitions) {
		t.Fatalf("definitions=%+v", recovery.definitions)
	}
	if strings.Contains(string(recovery.definitions[1].InputSchema), "enum") {
		t.Fatalf("recovery schema created a fatal URL enum: %s", recovery.definitions[1].InputSchema)
	}
	for _, required := range []string{"fresh unread", "https://github.com/openai/codex", "https://docs.anthropic.com/en/docs/claude-code"} {
		if !strings.Contains(recovery.directive, required) {
			t.Fatalf("directive missing %q: %s", required, recovery.directive)
		}
	}
}

func TestPlanResearchDuplicateRecoveryKeepsFreshEvidenceAvailableAfterManyStalledSteps(t *testing.T) {
	definitions := []models.ActionDefinition{{Name: "web_search"}, {Name: "read_url"}}
	recovery := planResearchDuplicateRecovery(3, []string{"https://example.com/still-unread"}, definitions)
	if len(recovery.definitions) != len(definitions) {
		t.Fatalf("recovery=%+v", recovery)
	}
	if !strings.Contains(recovery.directive, "https://example.com/still-unread") {
		t.Fatalf("recovery directive=%q", recovery.directive)
	}
}

func TestResearchDuplicateRecoveryOmitsExactRecentSteps(t *testing.T) {
	if includeExactResearchSuffix([]models.ActionDefinition{{Name: "read_url"}}, 1) {
		t.Fatal("duplicate recovery retained the exact suffix that caused the model to copy attempted URLs")
	}
	if !includeExactResearchSuffix([]models.ActionDefinition{{Name: "read_url"}}, 0) {
		t.Fatal("ordinary tool-capable research lost its exact suffix")
	}
	if includeExactResearchSuffix(nil, 0) {
		t.Fatal("reporting request exposed raw tool steps")
	}
}

func TestSelectUnattemptedResearchURLsExcludesEveryPriorReadProposal(t *testing.T) {
	candidates := []string{
		"https://example.com/already-read",
		"https://example.com/fresh-one",
		"https://example.com/already-failed",
		"https://example.com/fresh-two",
	}
	attempted := map[string]bool{
		"https://example.com/already-read":   true,
		"https://example.com/already-failed": true,
	}
	got := selectUnattemptedResearchURLs(candidates, attempted, 2)
	if strings.Join(got, ",") != "https://example.com/fresh-one,https://example.com/fresh-two" {
		t.Fatalf("selected=%v", got)
	}
}

func TestResearchFinalOnlyPromptClosesToolUse(t *testing.T) {
	for _, required := range []string{"Tool use is closed", "Final", "complete report"} {
		if !strings.Contains(researchFinalOnlyPrompt(), required) {
			t.Fatalf("final-only prompt missing %q", required)
		}
	}
}

func TestRewriteResearchReportLinksDowngradesUnreadCitationWithoutDroppingReport(t *testing.T) {
	report := "Read [Codex](https://github.com/openai/codex) and [unread lead](https://example.com/lead)."
	got, removed := rewriteResearchReportLinks(report, map[string]bool{"https://github.com/openai/codex": true})
	if got != "Read [Codex](https://github.com/openai/codex) and unread lead." || len(removed) != 1 || removed[0] != "https://example.com/lead" {
		t.Fatalf("report=%q removed=%v", got, removed)
	}
}

func TestSearchedResearchSourceEvidenceRequiresSuccessfulSearchManifest(t *testing.T) {
	succeeded := &ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{
		"result_version":2,"complete_empty":false,"degraded":false,"degradations":[],
		"evidence":[{"chunk_id":"chunk_pdf","source_id":"src_pdf","evidence_revision_id":"evr_pdf"}]
	}`)}
	failed := &ActionResult{Status: ActionDomainError, ErrorCode: "retrieval_unavailable"}
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{{Actions: []AcceptedAction{
		{Name: "search_evidence", Result: succeeded},
		{Name: "search_evidence", Result: failed},
		{Name: "read_url", Result: succeeded},
	}}}}
	references := searchedResearchSourceEvidence(prefix)
	if len(references) != 1 {
		t.Fatalf("references=%+v", references)
	}
	if _, ok := references[researchSourceEvidenceReference{SourceID: "src_pdf", RevisionID: "evr_pdf"}]; !ok {
		t.Fatalf("searched PDF authority missing: %+v", references)
	}
}

func TestResearchExecutionControlPromptCarriesPinnedBatchContract(t *testing.T) {
	got := researchExecutionControlPrompt(6)
	for _, required := range []string{"at most 6 tool calls", "one to three queries", "exactly one URL"} {
		if !strings.Contains(got, required) {
			t.Fatalf("control prompt missing %q: %s", required, got)
		}
	}
}

func TestResearchRecoveryAllowsChoiceBeyondOneActionBatch(t *testing.T) {
	if got, want := researchRecoveryCandidateLimit(6), 24; got != want {
		t.Fatalf("candidate limit=%d want=%d", got, want)
	}
	runtime := &ResearchRuntime{}
	if got, want := runtime.InvalidModelResponseRetryLimit(), 5; got != want {
		t.Fatalf("invalid response retries=%d want=%d", got, want)
	}
}

func TestResearchFinalProjectionHidesDiscoveredOnlyLedgerURLs(t *testing.T) {
	if !includeResearchLedgerURL("read", false) || !includeResearchLedgerURL("failed", false) {
		t.Fatal("Final projection dropped a read or failed evidence boundary")
	}
	if includeResearchLedgerURL("discovered", false) {
		t.Fatal("Final projection exposed a discovered-only URL")
	}
	if !includeResearchLedgerURL("discovered", true) {
		t.Fatal("tool-capable projection hid discovery candidates")
	}
}

func TestResearchPDFImportRequiredResultIsDiscoveryMetadataNotReadEvidence(t *testing.T) {
	output := readURLOutput{
		Outcome:      "pdf_requires_source_import",
		RequestedURL: "https://example.com/paper",
		FinalURL:     "https://cdn.example.com/paper.pdf",
		MediaType:    "application/pdf",
	}
	if !isResearchPDFImportRequired(output) {
		t.Fatal("source-first PDF result was eligible for read-evidence materialization")
	}
	if isResearchPDFImportRequired(readURLOutput{FinalURL: output.FinalURL, Markdown: "verified HTML body"}) {
		t.Fatal("ordinary read_url output was classified as a PDF import requirement")
	}
}

func TestResearchPDFImportRequiredCapsuleDoesNotClaimRetainedEvidence(t *testing.T) {
	payload := json.RawMessage(`{"outcome":"pdf_requires_source_import","requested_url":"https://example.com/paper","final_url":"https://cdn.example.com/paper.pdf","media_type":"application/pdf"}`)
	capsule := buildResearchStepCapsule(AcceptedProposal{DecisionNo: 1, Actions: []AcceptedAction{{
		ActionID: "action_pdf", Name: "read_url", Input: json.RawMessage(`{"url":"https://example.com/paper"}`),
		Result: &ActionResult{Status: ActionSucceeded, Output: payload},
	}}})
	if strings.Contains(capsule, "Retained evidence") || strings.Contains(capsule, "words:") {
		t.Fatalf("PDF import requirement was represented as read evidence:\n%s", capsule)
	}
	for _, required := range []string{"pdf_requires_source_import", "https://cdn.example.com/paper.pdf"} {
		if !strings.Contains(capsule, required) {
			t.Fatalf("capsule missing %q:\n%s", required, capsule)
		}
	}
}

func TestResearchCompletionSignalRequiresAssembledArtifact(t *testing.T) {
	for _, value := range []string{"Final", "done.", "已完成！"} {
		if !isResearchCompletionSignal(value) {
			t.Fatalf("completion signal %q was accepted as a report", value)
		}
	}
	for _, value := range []string{"# Decision\n\nAdopt checkpointed sections.", "完成情况：建议采用分段写作。"} {
		if isResearchCompletionSignal(value) {
			t.Fatalf("complete report %q was rejected as a signal", value)
		}
	}
}

func TestSourceFirstResearchRejectsEveryFinalUntilReportAssemblyCheckpoint(t *testing.T) {
	runtime := &ResearchRuntime{}
	decision := models.ModelDecision{Final: &models.FinalDraft{Text: "# Decision\n\nUse the durable Source pipeline."}}
	if _, err := runtime.PrepareDecisionResponse(context.Background(), Execution{AgentConfigID: "research.executor@9"}, CheckpointPrefix{}, decision); err == nil {
		t.Fatal("v9 accepted a full Final that bypassed the Source import barrier")
	}
	if got, err := runtime.PrepareDecisionResponse(context.Background(), Execution{AgentConfigID: "research.executor@8"}, CheckpointPrefix{}, decision); err != nil || got.Final == nil {
		t.Fatalf("legacy full Final changed: got=%+v err=%v", got, err)
	}
}

func TestSourceFirstResearchRequiresAssemblyAfterLatestImport(t *testing.T) {
	succeeded := &ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{"ok":true}`)}
	assembled := &ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{"path":"report.md","object_key":"research-workspaces/run/a/report.md","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bytes":10}`)}
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{{Name: "assemble_research_report", Result: assembled}}},
		{DecisionNo: 2, Actions: []AcceptedAction{{Name: "save_url_as_source", Result: succeeded}}},
	}}
	if hasAssembledResearchReportAfterImports(prefix) {
		t.Fatal("assembly before the latest import was accepted")
	}
	prefix.Proposals = append(prefix.Proposals, AcceptedProposal{DecisionNo: 3, Actions: []AcceptedAction{{Name: "assemble_research_report", Result: assembled}}})
	if !hasAssembledResearchReportAfterImports(prefix) {
		t.Fatal("assembly after the latest import was rejected")
	}
}

func TestResearchSourceImportProjectionContainsOnlyLifecycleState(t *testing.T) {
	projection := formatResearchSourceImportProjection([]researchSourceImportState{
		{SourceID: "src_pending", SourceState: "normalizing", JobStatus: "running"},
		{SourceID: "src_ready", SourceState: "ready", JobStatus: "succeeded", Searchable: true},
		{SourceID: "src_failed", SourceState: "failed", JobStatus: "failed", ErrorCode: "extraction_invalid"},
		{SourceID: "src_review", SourceState: "qualifying", JobStatus: "succeeded"},
	})
	for _, required := range []string{
		"Pending Source Imports:",
		"src_pending: processing, not searchable",
		"src_ready: ready, searchable",
		"src_failed: failed, extraction_invalid, not searchable",
		"src_review: review_required, not searchable",
	} {
		if !strings.Contains(projection, required) {
			t.Fatalf("projection missing %q:\n%s", required, projection)
		}
	}
	for _, forbidden := range []string{"worker log", "%PDF", "https://"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("projection leaked %q:\n%s", forbidden, projection)
		}
	}
}
