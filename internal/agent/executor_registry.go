package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/skillcatalog"
)

type DefinitionExecutor interface {
	ExecuteAttempt(context.Context, Attempt) AttemptResolution
}

type ExecutorRegistration struct {
	Identity   string
	Executor   DefinitionExecutor
	Capability agentcatalog.ExecutorCapability
}

type ResolvedExecution struct {
	Definition   agentcatalog.Definition
	ModelPolicy  agentcatalog.ModelPolicy
	ModelContext agentcatalog.ResolvedModelContextPolicy
	Executor     DefinitionExecutor
	Capability   agentcatalog.ExecutorCapability
	Skills       []skillcatalog.SkillVersion
}

type ExecutorRegistry struct {
	catalog       agentcatalog.Catalog
	skills        skillcatalog.Catalog
	registrations map[string]ExecutorRegistration
}

func NewExecutorRegistry(catalog agentcatalog.Catalog, prompts promptcatalog.Catalog, skills skillcatalog.Catalog, tools map[string]agentcatalog.ToolCapability, registrations ...ExecutorRegistration) (*ExecutorRegistry, error) {
	registry := &ExecutorRegistry{catalog: catalog, skills: skills, registrations: make(map[string]ExecutorRegistration, len(registrations))}
	bindings := agentcatalog.Bindings{
		Prompts:   make(map[agentcatalog.Reference]bool),
		Skills:    make(map[agentcatalog.Reference]bool),
		Tools:     cloneToolCapabilities(tools),
		Executors: make(map[string]agentcatalog.ExecutorCapability, len(registrations)),
	}
	for _, prompt := range prompts.Versions() {
		bindings.Prompts[agentcatalog.Reference{Identity: prompt.Identity, Version: prompt.Version}] = true
	}
	for _, skill := range skills.Versions() {
		bindings.Skills[agentcatalog.Reference{Identity: skill.Identity, Version: skill.Version}] = true
	}
	for _, registration := range registrations {
		registration.Identity = strings.TrimSpace(registration.Identity)
		if !validExecutorIdentity(registration.Identity) || registration.Executor == nil {
			return nil, errors.New("invalid Executor registration")
		}
		if _, duplicate := registry.registrations[registration.Identity]; duplicate {
			return nil, fmt.Errorf("duplicate Executor registration %q", registration.Identity)
		}
		registration.Capability = cloneExecutorCapability(registration.Capability)
		registry.registrations[registration.Identity] = registration
		bindings.Executors[registration.Identity] = registration.Capability
	}
	if err := catalog.ValidateBindings(bindings); err != nil {
		return nil, err
	}
	for identity := range registry.registrations {
		used := false
		for _, definition := range catalog.Definitions() {
			if definition.Executor == identity {
				used = true
				break
			}
		}
		if !used {
			return nil, fmt.Errorf("Executor registration %q has no Agent Definition", identity)
		}
	}
	return registry, nil
}

func NewNanoExecutorRegistry(catalog agentcatalog.Catalog, prompts promptcatalog.Catalog, skills skillcatalog.Catalog, chatLeader, research, researchPlanner, researchRoot DefinitionExecutor, studio ...DefinitionExecutor) (*ExecutorRegistry, error) {
	if researchPlanner == nil {
		researchPlanner = unavailableDefinitionExecutor{code: "research_planner_executor_unavailable"}
	}
	if researchRoot == nil {
		researchRoot = unavailableDefinitionExecutor{code: "research_root_executor_unavailable"}
	}
	studioExecutor := DefinitionExecutor(unavailableStudioExecutor{})
	if len(studio) > 0 && studio[0] != nil {
		studioExecutor = studio[0]
	}
	return NewExecutorRegistry(catalog, prompts, skills, NanoToolCapabilities(),
		ExecutorRegistration{Identity: "chat_leader", Executor: chatLeader, Capability: ChatLeaderExecutorCapability()},
		ExecutorRegistration{Identity: "research", Executor: research, Capability: ResearchExecutorCapability()},
		ExecutorRegistration{Identity: "research_planner", Executor: researchPlanner, Capability: ResearchPlannerExecutorCapability()},
		ExecutorRegistration{Identity: "research_root", Executor: researchRoot, Capability: ResearchRootExecutorCapability()},
		ExecutorRegistration{Identity: "studio_structured_output", Executor: studioExecutor, Capability: StudioStructuredOutputExecutorCapability()},
	)
}

type unavailableStudioExecutor struct{}

func (unavailableStudioExecutor) ExecuteAttempt(context.Context, Attempt) AttemptResolution {
	return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: "studio_executor_unavailable"}
}

type unavailableDefinitionExecutor struct{ code string }

func (e unavailableDefinitionExecutor) ExecuteAttempt(context.Context, Attempt) AttemptResolution {
	return AttemptResolution{Disposition: AttemptTerminal, ErrorCode: e.code}
}

func (r *ExecutorRegistry) Resolve(reference agentcatalog.Reference) (ResolvedExecution, error) {
	if r == nil {
		return ResolvedExecution{}, errors.New("Executor Registry is nil")
	}
	definition, ok := r.catalog.ResolveDefinition(reference)
	if !ok {
		return ResolvedExecution{}, fmt.Errorf("unknown Agent Definition %s", reference)
	}
	registration, ok := r.registrations[definition.Executor]
	if !ok {
		return ResolvedExecution{}, fmt.Errorf("Agent Definition %s has no Executor", reference)
	}
	policy, ok := r.catalog.ResolveModelPolicy(definition.ModelPolicy)
	if !ok {
		return ResolvedExecution{}, fmt.Errorf("Agent Definition %s has no Model Policy", reference)
	}
	modelContext, err := r.catalog.ResolveModelContextPolicy(policy.Reference())
	if err != nil {
		return ResolvedExecution{}, err
	}
	resolvedSkills := make([]skillcatalog.SkillVersion, 0, len(definition.Skills))
	for _, reference := range definition.Skills {
		skill, ok := r.skills.Resolve(reference.Identity, reference.Version)
		if !ok {
			return ResolvedExecution{}, fmt.Errorf("Agent Definition %s has no Skill %s", reference, reference)
		}
		resolvedSkills = append(resolvedSkills, skill)
	}
	return ResolvedExecution{
		Definition: definition, ModelPolicy: policy, ModelContext: modelContext, Executor: registration.Executor,
		Capability: cloneExecutorCapability(registration.Capability), Skills: resolvedSkills,
	}, nil
}

// NanoToolCapabilities declares each tool's execution scheduling. calculate,
// current_time, search_evidence, and the independently idempotent
// discover_sources tool can run concurrently. web_search stays
// ordered_sync: it calls an external, rate-limited provider, and batching
// concurrent calls to it needs its own rate-limit accounting first.
func NanoToolCapabilities() map[string]agentcatalog.ToolCapability {
	return map[string]agentcatalog.ToolCapability{
		"assemble_research_report": {Scheduling: agentcatalog.ToolOrderedSync},
		"calculate":                {Scheduling: agentcatalog.ToolParallel},
		"current_time":             {Scheduling: agentcatalog.ToolParallel},
		"discover_sources":         {Scheduling: agentcatalog.ToolParallel},
		"inspect_source":           {Scheduling: agentcatalog.ToolParallel},
		"list_research_files":      {Scheduling: agentcatalog.ToolParallel},
		"read_research_file":       {Scheduling: agentcatalog.ToolParallel},
		"read_tool_result":         {Scheduling: agentcatalog.ToolParallel},
		"read_document_pages":      {Scheduling: agentcatalog.ToolParallel},
		"read_skill":               {Scheduling: agentcatalog.ToolParallel},
		"read_url":                 {Scheduling: agentcatalog.ToolParallel},
		"save_url_as_source":       {Scheduling: agentcatalog.ToolOrderedSync},
		"rewrite_todo_list":        {Scheduling: agentcatalog.ToolOrderedSync},
		"search_evidence":          {Scheduling: agentcatalog.ToolParallel},
		"update_todo_status":       {Scheduling: agentcatalog.ToolOrderedSync},
		"web_search":               {Scheduling: agentcatalog.ToolOrderedSync},
		"write_research_file":      {Scheduling: agentcatalog.ToolOrderedSync},
	}
}

func ChatLeaderExecutorCapability() agentcatalog.ExecutorCapability {
	return agentcatalog.ExecutorCapability{
		PromptPurposes: map[string]bool{
			"leader_router": true, "chat_composer_bare": true,
			"chat_composer_grounded": true, "query_contextualizer": true,
		},
		Contracts: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("chat.turn@1"):   true,
			agentcatalog.MustParseReference("chat.answer@1"): true,
		},
		Tools: map[string]bool{
			"calculate": true, "current_time": true, "discover_sources": true, "rewrite_todo_list": true,
			"search_evidence": true, "update_todo_status": true,
		},
		ChildExecutors: map[string]bool{"research": true},
		MaxLimits: agentcatalog.Limits{
			ModelCalls: 17, ActionDecisions: 4, Actions: 8, ActionBatch: 4, ContextBytes: 65536, ResultBytes: 65536, Attempts: 3,
		},
		MaxChildren: 1, MemberVisible: true, CanPublish: true,
	}
}

func ResearchExecutorCapability() agentcatalog.ExecutorCapability {
	return agentcatalog.ExecutorCapability{
		PromptPurposes: map[string]bool{"planner": true},
		Contracts: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("research.discovery-task@1"):   true,
			agentcatalog.MustParseReference("research.discovery-result@1"): true,
		},
		Tools: map[string]bool{"web_search": true},
		MaxLimits: agentcatalog.Limits{
			ModelCalls: 1, Actions: 1, ActionBatch: 1, ContextBytes: 65536, ResultBytes: 262144, Attempts: 3,
		},
	}
}

func ResearchPlannerExecutorCapability() agentcatalog.ExecutorCapability {
	return agentcatalog.ExecutorCapability{
		PromptPurposes: map[string]bool{"planner": true},
		Contracts: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("research.plan-request@1"): true,
			agentcatalog.MustParseReference("research.plan-result@1"):  true,
		},
		Skills: map[agentcatalog.Reference]bool{agentcatalog.MustParseReference("skill.grill-me@1"): true},
		Tools:  map[string]bool{"read_skill": true},
		MaxLimits: agentcatalog.Limits{
			ModelCalls: 4, Actions: 2, ActionBatch: 1, ContextBytes: 262144, ResultBytes: 131072, Attempts: 3,
		},
		MemberVisible: true, CanPublish: true,
	}
}

func ResearchRootExecutorCapability() agentcatalog.ExecutorCapability {
	return agentcatalog.ExecutorCapability{
		PromptPurposes: map[string]bool{
			"executor": true, "step_compactor": true, "rollup": true, "archival_compactor": true,
			"task_memory_compactor": true, "reporter": true,
		},
		Contracts: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("research.plan-result@1"):   true,
			agentcatalog.MustParseReference("research.report-result@1"): true,
		},
		Skills: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("skill.research-workflow@1"): true,
			agentcatalog.MustParseReference("skill.research-workflow@2"): true,
		},
		Tools: map[string]bool{
			"assemble_research_report": true, "list_research_files": true, "read_research_file": true,
			"inspect_source": true, "read_document_pages": true, "read_tool_result": true, "read_url": true, "save_url_as_source": true,
			"rewrite_todo_list": true, "search_evidence": true, "update_todo_status": true,
			"web_search": true, "write_research_file": true,
		},
		MaxLimits: agentcatalog.Limits{
			ModelCalls: 120, Actions: 100, ActionBatch: 8, ContextBytes: 8388608, ResultBytes: 33554432, Attempts: 5,
		},
		MemberVisible: true, CanPublish: true,
	}
}

func StudioStructuredOutputExecutorCapability() agentcatalog.ExecutorCapability {
	return agentcatalog.ExecutorCapability{
		PromptPurposes: map[string]bool{
			"report": true, "flashcards": true, "mind_map": true, "data_table": true,
		},
		Contracts: map[agentcatalog.Reference]bool{
			agentcatalog.MustParseReference("studio.output-request@1"):    true,
			agentcatalog.MustParseReference("studio.report-result@1"):     true,
			agentcatalog.MustParseReference("studio.flashcards-result@1"): true,
			agentcatalog.MustParseReference("studio.mind-map-result@1"):   true,
			agentcatalog.MustParseReference("studio.data-table-result@1"): true,
		},
		Tools: map[string]bool{"search_evidence": true},
		MaxLimits: agentcatalog.Limits{
			ModelCalls: 2, Actions: 1, ActionBatch: 1, ContextBytes: 65536, ResultBytes: 65536, Attempts: 3,
		},
		MemberVisible: true, CanPublish: true,
	}
}

func validExecutorIdentity(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsLower(character) || unicode.IsDigit(character) || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func cloneToolCapabilities(source map[string]agentcatalog.ToolCapability) map[string]agentcatalog.ToolCapability {
	cloned := make(map[string]agentcatalog.ToolCapability, len(source))
	for name, capability := range source {
		cloned[name] = capability
	}
	return cloned
}

func cloneExecutorCapability(source agentcatalog.ExecutorCapability) agentcatalog.ExecutorCapability {
	source.PromptPurposes = cloneBoolMap(source.PromptPurposes)
	source.Contracts = cloneBoolMap(source.Contracts)
	source.Skills = cloneBoolMap(source.Skills)
	source.Tools = cloneBoolMap(source.Tools)
	source.ChildExecutors = cloneBoolMap(source.ChildExecutors)
	return source
}

func cloneBoolMap[K comparable](source map[K]bool) map[K]bool {
	if source == nil {
		return nil
	}
	cloned := make(map[K]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
