package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

func TestResearchRootSupportsRuntimeSubagentTools(t *testing.T) {
	capability := ResearchRootExecutorCapability()
	for _, name := range []string{"spawn_agent", "wait_agent", "list_agents"} {
		if !capability.Tools[name] {
			t.Errorf("Research executor cannot use runtime tool %q", name)
		}
		if _, ok := NanoToolCapabilities()[name]; !ok {
			t.Errorf("runtime tool %q has no scheduling policy", name)
		}
	}
}

type subagentTestAuthority struct{ mcpToolAuthority }

func (*subagentTestAuthority) IsSubagent(context.Context, Attempt) (bool, error) { return true, nil }

func TestSubagentInheritsToolsButCannotCallAgentManagement(t *testing.T) {
	catalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := catalog.ResolveDefinition(agentcatalog.MustParseReference("research.executor@18"))
	if !ok {
		t.Fatal("missing research definition")
	}
	registrations := make([]MCPToolRegistration, 0, len(definition.Tools))
	for _, name := range definition.Tools {
		registrations = append(registrations, MCPToolRegistration{Action: testMCPAction(name), Scheduling: agentcatalog.ToolParallel})
	}
	registry, err := NewMCPToolRegistry(registrations...)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewMCPToolHost(catalog, registry, &subagentTestAuthority{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := host.OpenAttempt(context.Background(), AttemptToolScope{
		Definition: definition.Reference(), Attempt: Attempt{RunID: "run-child", JobID: "job-child", AttemptNo: 1, LeaseToken: "lease-child"}, RemainingActions: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == "spawn_agent" || tool.Name == "wait_agent" || tool.Name == "list_agents" {
			t.Errorf("child was granted management tool %s", tool.Name)
		}
	}
	if len(tools) != len(definition.Tools)-3 {
		t.Errorf("inherited %d tools, want %d", len(tools), len(definition.Tools)-3)
	}
	if _, err := session.CallTool(context.Background(), "spawn_agent", json.RawMessage(`{"message":"create a descendant"}`), "decision:1/action:0"); !isToolErrorKind(err, ToolErrorAuthorization) {
		t.Errorf("child can bypass advertised scope: %v", err)
	}
}

func TestRuntimeSubagentReleaseDoesNotRequirePredefinedChildren(t *testing.T) {
	catalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := catalog.ResolveDefinition(agentcatalog.MustParseReference("research.executor@18"))
	if !ok {
		t.Fatal("runtime subagent Research definition is missing")
	}
	if len(definition.Children) != 0 {
		t.Fatal("runtime subagents must not require a catalog child definition")
	}
	for _, name := range []string{"spawn_agent", "wait_agent", "list_agents"} {
		found := false
		for _, tool := range definition.Tools {
			found = found || tool == name
		}
		if !found {
			t.Errorf("runtime subagent Research definition omits %q", name)
		}
	}
}

func TestCurrentReleaseDoesNotBindArchivedPrompts(t *testing.T) {
	catalog := agentcatalog.MustLoadEmbedded()
	prompts := promptcatalog.MustLoadEmbedded()
	release, _ := catalog.ResolveRelease(agentcatalog.MustParseReference("nano.default@36"))
	for _, root := range release.Roots {
		definition, _ := catalog.ResolveDefinition(root)
		for _, reference := range definition.Prompts {
			prompt, ok := prompts.Resolve(reference.Identity, reference.Version)
			if !ok || prompt.Archived {
				t.Fatalf("current root %s binds historical prompt %s", root, reference)
			}
		}
	}
}
