package agentcatalog

import (
	"strings"
	"testing"
)

func TestEmbeddedQwenPlusContextPolicyDerivesPinnedBudgets(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	invocation, ok := catalog.ResolveModelPolicy(MustParseReference("agent.chat-default@1"))
	if !ok {
		t.Fatal("chat invocation policy is missing")
	}
	resolved, err := catalog.ResolveModelContextPolicy(invocation.Reference())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Capability.ResolvedModel != "qwen-plus-2025-07-28" || resolved.Capability.ContextWindowTokens != 1_000_000 ||
		resolved.Capability.MaxInputTokens != 997_952 || resolved.Capability.MaxOutputTokens != 32_768 {
		t.Fatalf("capability=%+v", resolved.Capability)
	}
	if resolved.Budgets.HardInputTokens != 997_952 || resolved.Budgets.SafeInputTokens != 993_856 ||
		resolved.Budgets.CompactionTriggerTokens != 98_304 || resolved.Policy.KeepRecentTokens != 12_288 ||
		resolved.Policy.SummaryMaxOutputTokens != 2_048 || resolved.Policy.OverflowRetryLimit != 1 {
		t.Fatalf("resolved context=%+v", resolved)
	}
}

func TestEmbeddedDeepResearchPoliciesUseThinkingCapabilityOnly(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"agent.deep-research-default@3", "agent.deep-research-default@4", "agent.deep-research-default@5"} {
		policy, ok := catalog.ResolveModelPolicy(MustParseReference(reference))
		if !ok || policy.EnableThinking == nil || !*policy.EnableThinking {
			t.Fatalf("thinking policy %s=%+v ok=%v", reference, policy, ok)
		}
		resolved, err := catalog.ResolveModelContextPolicy(policy.Reference())
		if err != nil || resolved.Capability.InvocationMode != "thinking" || resolved.Capability.ResolvedModel != "qwen-plus-2025-07-28" {
			t.Fatalf("thinking context %s=%+v err=%v", reference, resolved, err)
		}
	}
	for _, reference := range []string{"agent.chat-default@1", "agent.research-default@1", "agent.studio-default@1"} {
		policy, ok := catalog.ResolveModelPolicy(MustParseReference(reference))
		if !ok || policy.EnableThinking != nil {
			t.Fatalf("non-thinking policy %s=%+v ok=%v", reference, policy, ok)
		}
		resolved, err := catalog.ResolveModelContextPolicy(policy.Reference())
		if err != nil || resolved.Capability.InvocationMode != "non_thinking" {
			t.Fatalf("non-thinking context %s=%+v err=%v", reference, resolved, err)
		}
	}
}

func TestEmbeddedDeepResearchV5UsesExpandedWorkingContext(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.ResolveModelContextPolicy(MustParseReference("agent.deep-research-default@5"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Budgets.CompactionTriggerTokens != 512_000 || resolved.Policy.KeepRecentTokens != 96_000 ||
		resolved.Policy.SummaryMaxOutputTokens != 4_096 || resolved.Policy.PinnedMaxOutputTokens != 16_384 {
		t.Fatalf("expanded Deep Research context=%+v", resolved)
	}
}

func TestEmbeddedDefaultV23UsesExpandedContextForEntireExecutionGraph(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	manifest, ok := catalog.ResolveRelease(MustParseReference("nano.default@23"))
	if !ok {
		t.Fatal("nano.default@23 is missing")
	}
	visited := make(map[Reference]bool)
	var visit func(Reference)
	visit = func(reference Reference) {
		t.Helper()
		if visited[reference] {
			return
		}
		visited[reference] = true
		definition, ok := catalog.ResolveDefinition(reference)
		if !ok {
			t.Fatalf("definition %s is missing", reference)
		}
		resolved, err := catalog.ResolveModelContextPolicy(definition.ModelPolicy)
		if err != nil {
			t.Fatalf("resolve context for %s: %v", reference, err)
		}
		if resolved.Policy.SoftInputLimitTokens != 512_000 || resolved.Policy.KeepRecentTokens != 96_000 ||
			resolved.Policy.EstimationSafetyTokens != 8_192 || resolved.Policy.SummaryMaxOutputTokens != 4_096 ||
			resolved.Policy.OverflowRetryLimit != 2 {
			t.Errorf("context for %s=%+v", reference, resolved.Policy)
		}
		for _, child := range definition.Children {
			visit(child)
		}
	}
	for _, root := range manifest.Roots {
		visit(root)
	}
	want := map[Reference]bool{
		MustParseReference("chat.leader@5"):               true,
		MustParseReference("research.source-discovery@2"): true,
		MustParseReference("research.planner@7"):          true,
		MustParseReference("research.executor@15"):        true,
		MustParseReference("studio.report@2"):             true,
		MustParseReference("studio.flashcards@2"):         true,
		MustParseReference("studio.mind-map@2"):           true,
		MustParseReference("studio.data-table@2"):         true,
	}
	if len(visited) != len(want) {
		t.Fatalf("visited=%v want=%v", visited, want)
	}
	for reference := range want {
		if !visited[reference] {
			t.Errorf("release graph does not reach %s", reference)
		}
	}
}

func TestExpandedContextPoliciesPreserveInvocationBehavior(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"agent.chat-default@1", "agent.chat-default@2"},
		{"agent.research-default@1", "agent.research-default@2"},
		{"agent.deep-research-default@3", "agent.deep-research-default@6"},
		{"agent.studio-default@1", "agent.studio-default@2"},
	} {
		oldPolicy, oldOK := catalog.ResolveModelPolicy(MustParseReference(pair[0]))
		newPolicy, newOK := catalog.ResolveModelPolicy(MustParseReference(pair[1]))
		if !oldOK || !newOK {
			t.Fatalf("policies %v missing: old=%v new=%v", pair, oldOK, newOK)
		}
		if oldPolicy.ProviderModel != newPolicy.ProviderModel || oldPolicy.Temperature != newPolicy.Temperature ||
			oldPolicy.MaxOutputTokens != newPolicy.MaxOutputTokens || oldPolicy.TimeoutMS != newPolicy.TimeoutMS ||
			oldPolicy.ThinkingEnabled() != newPolicy.ThinkingEnabled() {
			t.Errorf("invocation changed for %v: old=%+v new=%+v", pair, oldPolicy, newPolicy)
		}
	}
}

func TestCatalogRejectsContradictoryModelContextPolicy(t *testing.T) {
	files := minimalCatalogFS()
	files["provider-capabilities/provider.model.v1.json"] = mapFile(`{"identity":"provider.model","version":1,"provider_model":"provider/model","resolved_model":"model-1","context_window_tokens":1000,"max_input_tokens":900,"max_output_tokens":200,"tokenizer_identity":"test-tokenizer","tokenizer_version":"1","invocation_mode":"non_thinking"}`)
	files["model-context-policies/context.test.v1.json"] = mapFile(`{"identity":"context.test","version":1,"invocation_model_policy":"model.test@1","provider_capability":"provider.model@1","pinned_max_output_tokens":250,"soft_input_limit_tokens":700,"estimation_safety_tokens":100,"keep_recent_tokens":200,"summary_max_output_tokens":100,"overflow_retry_limit":1}`)
	_, err := LoadFS(files)
	if err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("err=%v", err)
	}
}

func TestCatalogRejectsThinkingModeMismatch(t *testing.T) {
	files := minimalCatalogFS()
	files["model-policies/model.test.v1.json"] = mapFile(`{"identity":"model.test","version":1,"provider_model":"provider/model","temperature":0,"max_output_tokens":128,"timeout_ms":1000,"enable_thinking":true}`)
	_, err := LoadFS(files)
	if err == nil || !strings.Contains(err.Error(), "invocation mode") {
		t.Fatalf("err=%v", err)
	}
}

func TestModelSelectionResolvesDifferentValidatedContextLimits(t *testing.T) {
	files := minimalCatalogFS()
	files["model-policies/model.large.v1.json"] = mapFile(`{"identity":"model.large","version":1,"provider_model":"provider/large","temperature":0,"max_output_tokens":256,"timeout_ms":1000,"enable_thinking":true}`)
	files["provider-capabilities/provider.large.v1.json"] = mapFile(`{"identity":"provider.large","version":1,"provider_model":"provider/large","resolved_model":"large-2026","context_window_tokens":4000,"max_input_tokens":3500,"max_output_tokens":500,"tokenizer_identity":"large-tokenizer","tokenizer_version":"2","invocation_mode":"thinking"}`)
	files["model-context-policies/context.large.v1.json"] = mapFile(`{"identity":"context.large","version":1,"invocation_model_policy":"model.large@1","provider_capability":"provider.large@1","pinned_max_output_tokens":256,"soft_input_limit_tokens":3000,"estimation_safety_tokens":200,"keep_recent_tokens":500,"summary_max_output_tokens":200,"overflow_retry_limit":2}`)
	catalog, err := LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	small, err := catalog.ResolveModelContextPolicy(MustParseReference("model.test@1"))
	if err != nil {
		t.Fatal(err)
	}
	large, err := catalog.ResolveModelContextPolicy(MustParseReference("model.large@1"))
	if err != nil {
		t.Fatal(err)
	}
	if small.Budgets.CompactionTriggerTokens != 700 || large.Budgets.HardInputTokens != 3500 ||
		large.Budgets.SafeInputTokens != 3300 || large.Budgets.CompactionTriggerTokens != 3000 ||
		large.Policy.KeepRecentTokens != 500 || large.Capability.InvocationMode != "thinking" {
		t.Fatalf("small=%+v large=%+v", small, large)
	}
}

func TestEmbeddedDeepResearchV7AllowsSlowThinkingCalls(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := catalog.ResolveModelPolicy(MustParseReference("agent.deep-research-default@7"))
	if !ok || policy.TimeoutMS != 360_000 || policy.EnableThinking == nil || !*policy.EnableThinking {
		t.Fatalf("policy=%+v ok=%v", policy, ok)
	}
	resolved, err := catalog.ResolveModelContextPolicy(policy.Reference())
	if err != nil || resolved.Budgets.CompactionTriggerTokens != 512_000 || resolved.Capability.InvocationMode != "thinking" {
		t.Fatalf("context=%+v err=%v", resolved, err)
	}
}

func TestEmbeddedDeepSeekResearchPolicyResolvesItsOwnCapability(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := catalog.ResolveModelPolicy(MustParseReference("agent.deep-research-deepseek@1"))
	if !ok || policy.ProviderModel != "aliyun/deepseek-v4-pro" || policy.TimeoutMS != 360_000 || policy.EnableThinking == nil || !*policy.EnableThinking {
		t.Fatalf("policy=%+v ok=%v", policy, ok)
	}
	resolved, err := catalog.ResolveModelContextPolicy(policy.Reference())
	if err != nil || resolved.Capability.ResolvedModel != "deepseek-v4-pro" || resolved.Capability.ContextWindowTokens != 1_000_000 ||
		resolved.Capability.InvocationMode != "thinking" || resolved.Budgets.CompactionTriggerTokens != 512_000 {
		t.Fatalf("context=%+v err=%v", resolved, err)
	}
	release, ok := catalog.ResolveRelease(MustParseReference("nano.default@49"))
	if !ok || release.Roots["research"].String() != "research.executor@36" || release.Roots["research_planner"].String() != "research.planner@15" {
		t.Fatalf("release=%+v ok=%v", release, ok)
	}
	definition, ok := catalog.ResolveDefinition(MustParseReference("research.executor@36"))
	if !ok || definition.Limits.Actions != 1000 || definition.Limits.ModelCalls != 600 || definition.Limits.ActionBatch != 16 || definition.Limits.InputTokens != 6_000_000 {
		t.Fatalf("DeepSeek tree budget=%+v ok=%v", definition.Limits, ok)
	}
}
