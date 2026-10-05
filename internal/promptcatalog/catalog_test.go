package promptcatalog

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogContainsEveryProductionPrompt(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"agent.leader-router":                         "select_leader_route.v1",
		"agent.research-planner":                      "submit_research_queries.v1",
		"agent.deep-research-planner":                 "research_plan_text.v1",
		"agent.deep-research-executor":                "research_execution_text.v1",
		"agent.deep-research-step-compactor":          "research_step_capsule_text.v1",
		"agent.deep-research-rollup":                  "research_rollup_text.v1",
		"agent.deep-research-archival-compactor":      "nano.research-capsules.v1",
		"agent.deep-research-task-memory-compactor":   "nano.research-task-memory.v1",
		"agent.deep-research-reporter":                "research_report_text.v1",
		"agent.chat-composer-bare":                    "final_draft_text.v1",
		"agent.chat-composer-grounded":                "grounded_final_draft_text.v1",
		"agent.query-contextualizer":                  "search_evidence.v1",
		"agent.studio-report":                         "studio_report_result.v1",
		"agent.studio-flashcards":                     "studio_flashcards_result.v1",
		"agent.studio-mind-map":                       "studio_mind_map_result.v1",
		"agent.studio-data-table":                     "studio_data_table_result.v1",
		"source-processing.image-evidence-normalizer": "image_evidence_regions.v1",
	}
	const extraVersions = 32 // chat composer upgrades plus final deep Research planner/executor/reporter/compactor upgrades, alongside their @1s
	if got := len(catalog.Versions()); got != len(want)+extraVersions {
		t.Fatalf("versions=%d want=%d", got, len(want)+extraVersions)
	}
	for identity, contract := range want {
		prompt, ok := catalog.Resolve(identity, 1)
		if !ok {
			t.Fatalf("missing %s@1", identity)
		}
		if prompt.Contract != contract || prompt.SHA256 == "" || strings.TrimSpace(prompt.Content) == "" {
			t.Fatalf("prompt=%+v", prompt)
		}
		if !strings.HasSuffix(prompt.SourcePath, ".md") {
			t.Fatalf("source path=%q", prompt.SourcePath)
		}
	}
	for _, version := range []int{2, 3, 4} {
		grounded, ok := catalog.Resolve("agent.chat-composer-grounded", version)
		if !ok {
			t.Fatalf("missing agent.chat-composer-grounded@%d", version)
		}
		if grounded.Contract != "grounded_final_draft_text.v1" || grounded.SHA256 == "" || strings.TrimSpace(grounded.Content) == "" {
			t.Fatalf("prompt=%+v", grounded)
		}
	}
	bare, ok := catalog.Resolve("agent.chat-composer-bare", 2)
	if !ok {
		t.Fatal("missing agent.chat-composer-bare@2")
	}
	if bare.Contract != "final_draft_text.v1" || bare.SHA256 == "" || strings.TrimSpace(bare.Content) == "" {
		t.Fatalf("prompt=%+v", bare)
	}
	bareV3, ok := catalog.Resolve("agent.chat-composer-bare", 3)
	if !ok || !strings.Contains(bareV3.Content, "rewrite_todo_list") || !strings.Contains(bareV3.Content, "TODO state is working memory") {
		t.Fatalf("bare v3 prompt=%+v ok=%v", bareV3, ok)
	}
	groundedV4, ok := catalog.Resolve("agent.chat-composer-grounded", 4)
	if !ok || !strings.Contains(groundedV4.Content, "update_todo_status") || !strings.Contains(groundedV4.Content, "Tool-call counts") {
		t.Fatalf("grounded v4 prompt=%+v ok=%v", groundedV4, ok)
	}
	bareV4, ok := catalog.Resolve("agent.chat-composer-bare", 4)
	if !ok || !strings.Contains(bareV4.Content, "discover_sources") || !strings.Contains(bareV4.Content, "factual") ||
		!strings.Contains(bareV4.Content, "one to three") {
		t.Fatalf("bare v4 prompt=%+v ok=%v", bareV4, ok)
	}
	groundedV5, ok := catalog.Resolve("agent.chat-composer-grounded", 5)
	if !ok || !strings.Contains(groundedV5.Content, "discover_sources") ||
		!strings.Contains(groundedV5.Content, "same Action batch") || strings.Contains(groundedV5.Content, "delegate.research.source-discovery") {
		t.Fatalf("grounded v5 prompt=%+v ok=%v", groundedV5, ok)
	}
	plannerV5, ok := catalog.Resolve("agent.deep-research-planner", 5)
	if !ok || plannerV5.Contract != "research_plan_text.v1" || !strings.Contains(plannerV5.Content, "This phase has no Web evidence") || !strings.Contains(plannerV5.Content, "Do not prescribe generic report boilerplate") {
		t.Fatalf("planner v5 prompt=%+v ok=%v", plannerV5, ok)
	}
	executorV4, ok := catalog.Resolve("agent.deep-research-executor", 4)
	if !ok || executorV4.Contract != "research_execution_text.v1" || !strings.Contains(executorV4.Content, "review_present=false") || !strings.Contains(executorV4.Content, "never use numbered placeholders") {
		t.Fatalf("executor v4 prompt=%+v ok=%v", executorV4, ok)
	}
	executorV5, ok := catalog.Resolve("agent.deep-research-executor", 5)
	if !ok || executorV5.Contract != "research_execution_text.v1" ||
		!strings.Contains(executorV5.Content, "save_url_as_source") ||
		!strings.Contains(executorV5.Content, "PDF facts are unavailable") ||
		!strings.Contains(executorV5.Content, "search_evidence") ||
		!strings.Contains(executorV5.Content, "assemble_research_report") {
		t.Fatalf("executor v5 prompt=%+v ok=%v", executorV5, ok)
	}
	reporterV3, ok := catalog.Resolve("agent.deep-research-reporter", 3)
	if !ok || reporterV3.Contract != "research_report_text.v1" || !strings.Contains(reporterV3.Content, "not verified in this run") || !strings.Contains(reporterV3.Content, "Organize the report around the Member's decision") {
		t.Fatalf("reporter v3 prompt=%+v ok=%v", reporterV3, ok)
	}
	for _, forbidden := range []string{"executive summary, method and evidence scope", "source appendix"} {
		if strings.Contains(strings.ToLower(reporterV3.Content), forbidden) {
			t.Fatalf("reporter v3 exposes internal acceptance scaffolding %q: %s", forbidden, reporterV3.Content)
		}
	}
}

func TestResearchCompactorV2PromptsExposeExactOutputFields(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	archival, ok := catalog.Resolve("agent.deep-research-archival-compactor", 2)
	if !ok {
		t.Fatal("missing archival compactor v2")
	}
	for _, field := range []string{"schema_version", "decision_no", "start_checkpoint_seq", "end_checkpoint_seq", "objective_advanced", "conclusions", "decisions", "constraints", "durable_refs", "contradictions", "verification", "follow_up"} {
		if !strings.Contains(archival.Content, `"`+field+`"`) {
			t.Fatalf("archival v2 missing exact field %q", field)
		}
	}
	memory, ok := catalog.Resolve("agent.deep-research-task-memory-compactor", 2)
	if !ok {
		t.Fatal("missing task memory compactor v2")
	}
	for _, field := range []string{"schema_version", "first_decision_no", "last_decision_no", "start_checkpoint_seq", "end_checkpoint_seq", "goal", "phase", "conclusions", "decisions", "constraints", "durable_refs", "contradictions", "failed_paths", "verification", "report_state", "follow_up"} {
		if !strings.Contains(memory.Content, `"`+field+`"`) {
			t.Fatalf("task memory v2 missing exact field %q", field)
		}
	}
}

func TestResearchCompactorV3PromptsExposeNonEmptyRequiredText(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	archival, ok := catalog.Resolve("agent.deep-research-archival-compactor", 3)
	if !ok || !strings.Contains(archival.Content, `"objective_advanced"`) || !strings.Contains(archival.Content, "must be a non-empty") {
		t.Fatalf("archival v3 prompt=%+v ok=%v", archival, ok)
	}
	memory, ok := catalog.Resolve("agent.deep-research-task-memory-compactor", 3)
	if !ok || !strings.Contains(memory.Content, `"goal"`) || !strings.Contains(memory.Content, `"phase"`) || !strings.Contains(memory.Content, "must both be non-empty") {
		t.Fatalf("task memory v3 prompt=%+v ok=%v", memory, ok)
	}
}

func TestResearchTaskMemoryV4DerivesExactRangeInsteadOfCopyingExample(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	memory, ok := catalog.Resolve("agent.deep-research-task-memory-compactor", 4)
	if !ok || !strings.Contains(memory.Content, "first input Step") || !strings.Contains(memory.Content, "last input Step") || !strings.Contains(memory.Content, "Do not copy") {
		t.Fatalf("task memory v4 prompt=%+v ok=%v", memory, ok)
	}
}

func TestCanonicalSHA256NormalizesLineEndingsAndTerminalNewline(t *testing.T) {
	left := PromptVersion{Identity: "agent.test", Version: 7, Contract: "contract.v1", Content: "alpha\r\nbeta"}
	right := PromptVersion{Identity: "agent.test", Version: 7, Contract: "contract.v1", Content: "alpha\nbeta\n"}
	leftHash, err := CanonicalSHA256(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := CanonicalSHA256(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("left=%s right=%s", leftHash, rightHash)
	}
}

func TestCatalogRejectsSameIdentityAndVersionWithDifferentCanonicalDefinition(t *testing.T) {
	_, err := New([]PromptVersion{
		{Identity: "agent.test", Version: 1, Contract: "contract.v1", Content: "alpha", SourcePath: "one.md"},
		{Identity: "agent.test", Version: 1, Contract: "contract.v1", Content: "beta", SourcePath: "two.md"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("err=%v", err)
	}
}

func TestCatalogRejectsInvalidMetadataAndMutableVersionSelectors(t *testing.T) {
	tests := []PromptVersion{
		{Identity: "agent.test", Version: 0, Contract: "contract.v1", Content: "content"},
		{Identity: "latest", Version: 1, Contract: "contract.v1", Content: "content"},
		{Identity: "agent.test", Version: 1, Contract: "latest", Content: "content"},
		{Identity: "agent.test", Version: 1, Contract: "contract.v1", Content: " "},
	}
	for _, definition := range tests {
		if _, err := New([]PromptVersion{definition}); err == nil {
			t.Fatalf("accepted %+v", definition)
		}
	}
}

func TestMarkdownParserRejectsDuplicateMetadata(t *testing.T) {
	_, err := parseMarkdown("duplicate.md", "---\nidentity: agent.test\nidentity: agent.other\nversion: 1\ncontract: contract.v1\n---\ncontent\n")
	if err == nil {
		t.Fatal("accepted duplicate identity metadata")
	}
}

func TestArchivedPromptsRemainResolvableWithoutAppearingInActiveCatalog(t *testing.T) {
	catalog := MustLoadEmbedded()
	for _, reference := range []struct {
		identity string
		version  int
	}{
		{"agent.research-planner", 1},
		{"agent.deep-research-step-compactor", 1},
		{"agent.deep-research-executor", 5},
	} {
		prompt, ok := catalog.Resolve(reference.identity, reference.version)
		if !ok || !prompt.Archived || !strings.HasPrefix(prompt.SourcePath, "prompts/") {
			t.Fatalf("archived prompt lost stable identity/path: %+v ok=%v", prompt, ok)
		}
	}
	for _, prompt := range catalog.ActiveVersions() {
		if prompt.Archived || prompt.Identity == "agent.research-planner" {
			t.Fatalf("historical prompt exposed as current: %+v", prompt)
		}
	}
	current, ok := catalog.Resolve("agent.deep-research-executor", 8)
	if !ok || current.Archived {
		t.Fatalf("claim card executor is not current: %+v", current)
	}
}
