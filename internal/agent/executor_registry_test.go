package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/skillcatalog"
)

type noopDefinitionExecutor struct{}

func (noopDefinitionExecutor) ExecuteAttempt(context.Context, Attempt) AttemptResolution {
	return AttemptResolution{Disposition: AttemptCompleted}
}

// TestNanoToolCapabilitiesSchedulesOnlySideEffectFreeToolsInParallel locks in
// the production scheduling decision: calculate, current_time, reads, and
// search_evidence are read-only and side-effect-free, so a batch made up
// only of these is eligible for Controller's concurrent execution path.
// web_search stays ordered_sync because it calls an external, rate-limited
// provider that concurrent calls would need their own accounting for.
func TestNanoToolCapabilitiesSchedulesOnlySideEffectFreeToolsInParallel(t *testing.T) {
	capabilities := NanoToolCapabilities()
	want := map[string]agentcatalog.ToolScheduling{
		"assemble_research_report": agentcatalog.ToolOrderedSync,
		"spawn_agent":              agentcatalog.ToolOrderedSync,
		"wait_agent":               agentcatalog.ToolOrderedSync,
		"list_agents":              agentcatalog.ToolParallel,
		"calculate":                agentcatalog.ToolParallel,
		"current_time":             agentcatalog.ToolParallel,
		"discover_sources":         agentcatalog.ToolParallel,
		"inspect_source":           agentcatalog.ToolParallel,
		"list_research_files":      agentcatalog.ToolParallel,
		"read_document_pages":      agentcatalog.ToolParallel,
		"read_research_file":       agentcatalog.ToolParallel,
		"read_tool_result":         agentcatalog.ToolParallel,
		"read_skill":               agentcatalog.ToolParallel,
		"read_url":                 agentcatalog.ToolParallel,
		"read_source":              agentcatalog.ToolParallel,
		"search_text":              agentcatalog.ToolParallel,
		"record_claim":             agentcatalog.ToolParallel,
		"request_user_input":       agentcatalog.ToolOrderedSync,
		"save_url_as_source":       agentcatalog.ToolOrderedSync,
		"rewrite_todo_list":        agentcatalog.ToolOrderedSync,
		"run_python":               agentcatalog.ToolOrderedSync,
		"search_evidence":          agentcatalog.ToolParallel,
		"update_todo_status":       agentcatalog.ToolOrderedSync,
		"web_search":               agentcatalog.ToolOrderedSync,
		"write_research_file":      agentcatalog.ToolOrderedSync,
	}
	if len(capabilities) != len(want) {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	for name, scheduling := range want {
		if got := capabilities[name].Scheduling; got != scheduling {
			t.Fatalf("%s scheduling=%q want=%q", name, got, scheduling)
		}
	}
}

func TestResearchRootAllowsSixteenActionsPerParallelBatch(t *testing.T) {
	if got := ResearchRootExecutorCapability().MaxLimits.ActionBatch; got != 16 {
		t.Fatalf("Research Root action batch=%d want=16", got)
	}
}

func TestExecutorRegistryResolvesExactDefinitionAndPolicyWithoutRole(t *testing.T) {
	registry := newTestExecutorRegistry(t)
	binding, err := registry.Resolve(agentcatalog.MustParseReference("chat.leader@1"))
	if err != nil {
		t.Fatal(err)
	}
	if binding.Definition.Reference().String() != "chat.leader@1" || binding.Definition.Executor != "chat_leader" {
		t.Fatalf("definition=%+v", binding.Definition)
	}
	if binding.ModelPolicy.Reference().String() != "agent.chat-default@1" || binding.Executor == nil {
		t.Fatalf("binding=%+v", binding)
	}
	if !binding.Capability.MemberVisible || !binding.Capability.CanPublish || binding.Capability.MaxChildren != 1 {
		t.Fatalf("code-owned capability=%+v", binding.Capability)
	}
	if _, err := registry.Resolve(agentcatalog.MustParseReference("chat.unknown@1")); err == nil || !strings.Contains(err.Error(), "unknown Agent Definition") {
		t.Fatalf("unknown definition err=%v", err)
	}
}

func TestExecutorRegistryResolvesEveryStudioDefinitionThroughOneBoundedExecutor(t *testing.T) {
	registry := newTestExecutorRegistry(t)
	for _, reference := range []string{"studio.report@1", "studio.flashcards@1", "studio.mind-map@1", "studio.data-table@1"} {
		binding, err := registry.Resolve(agentcatalog.MustParseReference(reference))
		if err != nil {
			t.Fatalf("resolve %s: %v", reference, err)
		}
		if binding.Definition.Executor != "studio_structured_output" || binding.ModelPolicy.Reference().String() != "agent.studio-default@1" {
			t.Fatalf("binding %s=%+v", reference, binding)
		}
		capability := binding.Capability
		if capability.MaxLimits.ModelCalls != 2 || capability.MaxLimits.Actions != 1 || capability.MaxLimits.ActionBatch != 1 || capability.MaxChildren != 0 {
			t.Fatalf("Studio capability=%+v", capability)
		}
		if len(capability.Tools) != 1 || !capability.Tools["search_evidence"] || len(capability.ChildExecutors) != 0 {
			t.Fatalf("Studio authority=%+v", capability)
		}
	}
}

func TestExecutorRegistryRejectsDuplicateMissingAndCapabilityExpansion(t *testing.T) {
	catalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := promptcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	skills := skillcatalog.MustLoadEmbedded()
	tools := productionToolCapabilities()
	registrations := productionExecutorRegistrations()
	missingTool := append([]ExecutorRegistration(nil), registrations...)
	missingTool[0] = ExecutorRegistration{Identity: "chat_leader", Executor: noopDefinitionExecutor{}, Capability: leaderExecutorCapability(map[string]bool{"current_time": true, "search_evidence": true})}
	for _, test := range []struct {
		name          string
		registrations []ExecutorRegistration
		want          string
	}{
		{"duplicate", append(registrations, registrations[0]), "duplicate Executor"},
		{"missing", registrations[:1], "unknown executor"},
		{"missing tool ceiling", missingTool, "tool"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewExecutorRegistry(catalog, prompts, skills, tools, test.registrations...); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want=%q", err, test.want)
			}
		})
	}
}

func TestExecutionHostDispatchesConfiguredPinWithoutRoleOrExecutorVersion(t *testing.T) {
	configured := newTestExecutorRegistry(t)
	legacy, err := NewRoleRegistry(
		RoleRegistration{Role: RoleLeader, ExecutorVersion: "leader-v1", Executor: noopRoleExecutor{}},
		RoleRegistration{Role: RoleResearch, ExecutorVersion: "research-v1", Executor: noopRoleExecutor{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	host := &AgentExecutionHost{legacyRegistry: legacy, configuredRegistry: configured}
	catalog, _ := agentcatalog.LoadEmbedded()
	definition, _ := catalog.ResolveDefinition(agentcatalog.MustParseReference("chat.leader@1"))
	policy, _ := catalog.ResolveModelPolicy(definition.ModelPolicy)
	executor, err := host.resolvePinnedExecution(pinnedExecution{
		runtimeKind: "configured", definition: definition.Reference(), definitionSHA256: definition.SHA256,
		executorIdentity: definition.Executor, modelPolicy: policy.Reference(), modelPolicySHA256: policy.SHA256,
		providerModel: policy.ProviderModel,
	})
	if err != nil || executor == nil {
		t.Fatalf("executor=%v err=%v", executor, err)
	}
	if _, err := host.resolvePinnedExecution(pinnedExecution{
		runtimeKind: "configured", definition: definition.Reference(), definitionSHA256: strings.Repeat("0", 64),
		executorIdentity: definition.Executor, modelPolicy: policy.Reference(), modelPolicySHA256: policy.SHA256,
		providerModel: policy.ProviderModel,
	}); err == nil || !strings.Contains(err.Error(), "pin mismatch") {
		t.Fatalf("mutated pin err=%v", err)
	}
}

func newTestExecutorRegistry(t *testing.T) *ExecutorRegistry {
	t.Helper()
	catalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := promptcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	skills := skillcatalog.MustLoadEmbedded()
	registry, err := NewExecutorRegistry(catalog, prompts, skills, productionToolCapabilities(), productionExecutorRegistrations()...)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func productionToolCapabilities() map[string]agentcatalog.ToolCapability {
	return map[string]agentcatalog.ToolCapability{
		"assemble_research_report": {Scheduling: agentcatalog.ToolOrderedSync},
		"spawn_agent":              {Scheduling: agentcatalog.ToolOrderedSync},
		"wait_agent":               {Scheduling: agentcatalog.ToolOrderedSync},
		"list_agents":              {Scheduling: agentcatalog.ToolParallel},
		"calculate":                {Scheduling: agentcatalog.ToolOrderedSync},
		"current_time":             {Scheduling: agentcatalog.ToolOrderedSync},
		"discover_sources":         {Scheduling: agentcatalog.ToolOrderedSync},
		"inspect_source":           {Scheduling: agentcatalog.ToolOrderedSync},
		"list_research_files":      {Scheduling: agentcatalog.ToolOrderedSync},
		"read_document_pages":      {Scheduling: agentcatalog.ToolOrderedSync},
		"read_research_file":       {Scheduling: agentcatalog.ToolOrderedSync},
		"read_tool_result":         {Scheduling: agentcatalog.ToolOrderedSync},
		"read_skill":               {Scheduling: agentcatalog.ToolOrderedSync},
		"read_url":                 {Scheduling: agentcatalog.ToolOrderedSync},
		"read_source":              {Scheduling: agentcatalog.ToolParallel},
		"search_text":              {Scheduling: agentcatalog.ToolParallel},
		"save_url_as_source":       {Scheduling: agentcatalog.ToolOrderedSync},
		"rewrite_todo_list":        {Scheduling: agentcatalog.ToolOrderedSync},
		"search_evidence":          {Scheduling: agentcatalog.ToolOrderedSync},
		"update_todo_status":       {Scheduling: agentcatalog.ToolOrderedSync},
		"web_search":               {Scheduling: agentcatalog.ToolOrderedSync},
		"write_research_file":      {Scheduling: agentcatalog.ToolOrderedSync},
		"run_python":               {Scheduling: agentcatalog.ToolOrderedSync},
		"record_claim":             {Scheduling: agentcatalog.ToolParallel},
		"request_user_input":       {Scheduling: agentcatalog.ToolOrderedSync},
	}
}

func productionExecutorRegistrations() []ExecutorRegistration {
	return []ExecutorRegistration{
		{Identity: "chat_leader", Executor: noopDefinitionExecutor{}, Capability: leaderExecutorCapability(map[string]bool{
			"calculate": true, "current_time": true, "discover_sources": true, "read_source": true, "rewrite_todo_list": true,
			"search_evidence": true, "search_text": true, "update_todo_status": true,
		})},
		{Identity: "research", Executor: noopDefinitionExecutor{}, Capability: agentcatalog.ExecutorCapability{
			PromptPurposes: map[string]bool{"planner": true},
			Contracts: map[agentcatalog.Reference]bool{
				agentcatalog.MustParseReference("research.discovery-task@1"):   true,
				agentcatalog.MustParseReference("research.discovery-result@1"): true,
			},
			Tools: map[string]bool{"web_search": true},
			MaxLimits: agentcatalog.Limits{
				ModelCalls: 1, Actions: 1, ActionBatch: 1, ContextBytes: 65536, ResultBytes: 262144, Attempts: 3,
			},
		}},
		{Identity: "research_planner", Executor: noopDefinitionExecutor{}, Capability: ResearchPlannerExecutorCapability()},
		{Identity: "research_root", Executor: noopDefinitionExecutor{}, Capability: ResearchRootExecutorCapability()},
		{Identity: "studio_structured_output", Executor: noopDefinitionExecutor{}, Capability: StudioStructuredOutputExecutorCapability()},
	}
}

func leaderExecutorCapability(tools map[string]bool) agentcatalog.ExecutorCapability {
	return agentcatalog.ExecutorCapability{
		PromptPurposes: map[string]bool{
			"leader_router": true, "chat_composer_bare": true,
			"chat_composer_grounded": true, "query_contextualizer": true,
		},
		Contracts: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("chat.turn@1"):   true,
			agentcatalog.MustParseReference("chat.answer@1"): true,
		},
		Tools: tools, ChildExecutors: map[string]bool{"research": true},
		MaxLimits: agentcatalog.Limits{
			ModelCalls: 17, ActionDecisions: 4, Actions: 8, ActionBatch: 4, ContextBytes: 65536, ResultBytes: 65536, Attempts: 3,
		},
		MaxChildren: 1, MemberVisible: true, CanPublish: true,
	}
}
