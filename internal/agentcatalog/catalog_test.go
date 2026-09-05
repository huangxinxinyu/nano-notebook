package agentcatalog

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedCatalogContainsSprint11ProductionAgents(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	definitions := catalog.Definitions()
	if got, want := len(definitions), 33; got != want {
		t.Fatalf("definitions=%d want=%d", got, want)
	}
	want := map[string]struct {
		executor string
		model    string
		tools    []string
		children []string
	}{
		"chat.leader@1": {
			executor: "chat_leader", model: "agent.chat-default@1",
			tools: []string{"calculate", "current_time", "search_evidence"}, children: []string{"research.source-discovery@1"},
		},
		"chat.leader@2": {
			executor: "chat_leader", model: "agent.chat-default@1",
			tools: []string{"calculate", "current_time", "search_evidence"}, children: []string{"research.source-discovery@1"},
		},
		"chat.leader@3": {
			executor: "chat_leader", model: "agent.chat-default@1",
			tools: []string{"calculate", "current_time", "search_evidence"}, children: []string{"research.source-discovery@1"},
		},
		"chat.leader@4": {
			executor: "chat_leader", model: "agent.chat-default@1",
			tools: []string{"calculate", "current_time", "rewrite_todo_list", "search_evidence", "update_todo_status"}, children: []string{"research.source-discovery@1"},
		},
		"chat.leader@5": {
			executor: "chat_leader", model: "agent.chat-default@2",
			tools: []string{"calculate", "current_time", "rewrite_todo_list", "search_evidence", "update_todo_status"}, children: []string{"research.source-discovery@2"},
		},
		"chat.leader@6": {
			executor: "chat_leader", model: "agent.chat-default@2",
			tools: []string{"calculate", "current_time", "discover_sources", "rewrite_todo_list", "search_evidence", "update_todo_status"},
		},
		"research.source-discovery@1": {
			executor: "research", model: "agent.research-default@1",
			tools: []string{"web_search"},
		},
		"research.source-discovery@2": {
			executor: "research", model: "agent.research-default@2",
			tools: []string{"web_search"},
		},
		"research.planner@1": {
			executor: "research_planner", model: "agent.deep-research-default@1",
			tools: []string{"read_skill"},
		},
		"research.planner@5": {
			executor: "research_planner", model: "agent.deep-research-default@1",
			tools: []string{"read_skill"},
		},
		"research.planner@6": {
			executor: "research_planner", model: "agent.deep-research-default@3",
			tools: []string{"read_skill"},
		},
		"research.planner@7": {
			executor: "research_planner", model: "agent.deep-research-default@6",
			tools: []string{"read_skill"},
		},
		"research.executor@1": {
			executor: "research_root", model: "agent.deep-research-default@1",
			tools: []string{"read_url", "search_evidence", "web_search"},
		},
		"research.executor@6": {
			executor: "research_root", model: "agent.deep-research-default@2",
			tools: []string{"assemble_research_report", "list_research_files", "read_research_file", "read_url", "search_evidence", "web_search", "write_research_file"},
		},
		"research.executor@7": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "list_research_files", "read_research_file", "read_url", "search_evidence", "web_search", "write_research_file"},
		},
		"research.executor@8": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "list_research_files", "read_document_pages", "read_research_file", "read_url", "search_evidence", "web_search", "write_research_file"},
		},
		"research.executor@9": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "list_research_files", "read_research_file", "read_url", "save_url_as_source", "search_evidence", "web_search", "write_research_file"},
		},
		"research.executor@10": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@11": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@12": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@13": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@14": {
			executor: "research_root", model: "agent.deep-research-default@4",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_tool_result", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@15": {
			executor: "research_root", model: "agent.deep-research-default@5",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_tool_result", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@16": {
			executor: "research_root", model: "agent.deep-research-default@5",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_tool_result", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"research.executor@17": {
			executor: "research_root", model: "agent.deep-research-default@5",
			tools: []string{"assemble_research_report", "inspect_source", "list_research_files", "read_research_file", "read_tool_result", "read_url", "rewrite_todo_list", "save_url_as_source", "search_evidence", "update_todo_status", "web_search", "write_research_file"},
		},
		"studio.report@1": {
			executor: "studio_structured_output", model: "agent.studio-default@1",
			tools: []string{"search_evidence"},
		},
		"studio.report@2": {
			executor: "studio_structured_output", model: "agent.studio-default@2",
			tools: []string{"search_evidence"},
		},
		"studio.flashcards@1": {
			executor: "studio_structured_output", model: "agent.studio-default@1",
			tools: []string{"search_evidence"},
		},
		"studio.flashcards@2": {
			executor: "studio_structured_output", model: "agent.studio-default@2",
			tools: []string{"search_evidence"},
		},
		"studio.mind-map@1": {
			executor: "studio_structured_output", model: "agent.studio-default@1",
			tools: []string{"search_evidence"},
		},
		"studio.mind-map@2": {
			executor: "studio_structured_output", model: "agent.studio-default@2",
			tools: []string{"search_evidence"},
		},
		"studio.data-table@1": {
			executor: "studio_structured_output", model: "agent.studio-default@1",
			tools: []string{"search_evidence"},
		},
		"studio.data-table@2": {
			executor: "studio_structured_output", model: "agent.studio-default@2",
			tools: []string{"search_evidence"},
		},
	}
	for _, definition := range definitions {
		key := definition.Reference().String()
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected Definition %s", key)
		}
		if definition.Executor != expected.executor || definition.ModelPolicy.String() != expected.model {
			t.Fatalf("definition=%+v", definition)
		}
		if strings.Join(definition.Tools, ",") != strings.Join(expected.tools, ",") {
			t.Fatalf("tools for %s=%v want=%v", key, definition.Tools, expected.tools)
		}
		children := make([]string, 0, len(definition.Children))
		for _, child := range definition.Children {
			children = append(children, child.String())
		}
		if strings.Join(children, ",") != strings.Join(expected.children, ",") {
			t.Fatalf("children for %s=%v want=%v", key, children, expected.children)
		}
		if len(definition.SHA256) != 64 || definition.SourcePath == "" {
			t.Fatalf("immutable identity missing for %s: %+v", key, definition)
		}
	}
	if got, want := len(catalog.ModelPolicies()), 12; got != want {
		t.Fatalf("model policies=%d want=%d", got, want)
	}
	if got, want := len(catalog.Contracts()), 12; got != want {
		t.Fatalf("contracts=%d want=%d", got, want)
	}
	manifest, ok := catalog.ResolveRelease(MustParseReference("nano.default@1"))
	if !ok || manifest.Roots["chat"].String() != "chat.leader@1" || len(manifest.SHA256) != 64 {
		t.Fatalf("manifest=%+v ok=%v", manifest, ok)
	}
	manifestV2, ok := catalog.ResolveRelease(MustParseReference("nano.default@2"))
	if !ok {
		t.Fatal("nano.default@2 is missing")
	}
	wantRoots := map[string]string{
		"chat":              "chat.leader@1",
		"studio_report":     "studio.report@1",
		"studio_flashcards": "studio.flashcards@1",
		"studio_mind_map":   "studio.mind-map@1",
		"studio_data_table": "studio.data-table@1",
	}
	if len(manifestV2.Roots) != len(wantRoots) {
		t.Fatalf("v2 roots=%v want=%v", manifestV2.Roots, wantRoots)
	}
	for name, wantReference := range wantRoots {
		if got := manifestV2.Roots[name].String(); got != wantReference {
			t.Fatalf("v2 root %s=%q want=%q", name, got, wantReference)
		}
	}
	manifestV5, ok := catalog.ResolveRelease(MustParseReference("nano.default@5"))
	if !ok || manifestV5.Roots["research_planner"].String() != "research.planner@1" || manifestV5.Roots["research"].String() != "research.executor@1" || manifestV5.Roots["chat"].String() != "chat.leader@3" {
		t.Fatalf("v5 manifest=%+v ok=%v", manifestV5, ok)
	}
	manifestV12, ok := catalog.ResolveRelease(MustParseReference("nano.default@12"))
	if !ok || manifestV12.Roots["research_planner"].String() != "research.planner@5" || manifestV12.Roots["research"].String() != "research.executor@6" || manifestV12.Roots["chat"].String() != "chat.leader@3" {
		t.Fatalf("v12 manifest=%+v ok=%v", manifestV12, ok)
	}
	manifestV13, ok := catalog.ResolveRelease(MustParseReference("nano.default@13"))
	if !ok || manifestV13.Roots["research_planner"].String() != "research.planner@6" || manifestV13.Roots["research"].String() != "research.executor@7" ||
		manifestV13.Roots["chat"].String() != "chat.leader@3" || manifestV13.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v13 manifest=%+v ok=%v", manifestV13, ok)
	}
	manifestV14, ok := catalog.ResolveRelease(MustParseReference("nano.default@14"))
	if !ok || manifestV14.Roots["research_planner"].String() != "research.planner@6" || manifestV14.Roots["research"].String() != "research.executor@8" ||
		manifestV14.Roots["chat"].String() != "chat.leader@3" || manifestV14.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v14 manifest=%+v ok=%v", manifestV14, ok)
	}
	manifestV15, ok := catalog.ResolveRelease(MustParseReference("nano.default@15"))
	if !ok || manifestV15.Roots["chat"].String() != "chat.leader@4" || manifestV15.Roots["research_planner"].String() != "research.planner@6" ||
		manifestV15.Roots["research"].String() != "research.executor@8" || manifestV15.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v15 manifest=%+v ok=%v", manifestV15, ok)
	}
	manifestV16, ok := catalog.ResolveRelease(MustParseReference("nano.default@16"))
	if !ok || manifestV16.Roots["chat"].String() != "chat.leader@4" || manifestV16.Roots["research_planner"].String() != "research.planner@6" ||
		manifestV16.Roots["research"].String() != "research.executor@9" || manifestV16.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v16 manifest=%+v ok=%v", manifestV16, ok)
	}
	manifestV17, ok := catalog.ResolveRelease(MustParseReference("nano.default@17"))
	if !ok || manifestV17.Roots["chat"].String() != "chat.leader@4" || manifestV17.Roots["research_planner"].String() != "research.planner@6" ||
		manifestV17.Roots["research"].String() != "research.executor@10" || manifestV17.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v17 manifest=%+v ok=%v", manifestV17, ok)
	}
	manifestV18, ok := catalog.ResolveRelease(MustParseReference("nano.default@18"))
	if !ok || manifestV18.Roots["chat"].String() != "chat.leader@4" || manifestV18.Roots["research_planner"].String() != "research.planner@6" ||
		manifestV18.Roots["research"].String() != "research.executor@11" || manifestV18.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v18 manifest=%+v ok=%v", manifestV18, ok)
	}
	manifestV19, ok := catalog.ResolveRelease(MustParseReference("nano.default@19"))
	if !ok || manifestV19.Roots["research"].String() != "research.executor@12" {
		t.Fatalf("v19 manifest=%+v ok=%v", manifestV19, ok)
	}
	manifestV20, ok := catalog.ResolveRelease(MustParseReference("nano.default@20"))
	if !ok || manifestV20.Roots["research"].String() != "research.executor@13" {
		t.Fatalf("v20 manifest=%+v ok=%v", manifestV20, ok)
	}
	manifestV21, ok := catalog.ResolveRelease(MustParseReference("nano.default@21"))
	if !ok || manifestV21.Roots["chat"].String() != "chat.leader@4" || manifestV21.Roots["research_planner"].String() != "research.planner@6" ||
		manifestV21.Roots["research"].String() != "research.executor@14" || manifestV21.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v21 manifest=%+v ok=%v", manifestV21, ok)
	}
	manifestV22, ok := catalog.ResolveRelease(MustParseReference("nano.default@22"))
	if !ok || manifestV22.Roots["chat"].String() != "chat.leader@4" || manifestV22.Roots["research_planner"].String() != "research.planner@6" ||
		manifestV22.Roots["research"].String() != "research.executor@15" || manifestV22.Roots["studio_report"].String() != "studio.report@1" {
		t.Fatalf("v22 manifest=%+v ok=%v", manifestV22, ok)
	}
	manifestV23, ok := catalog.ResolveRelease(MustParseReference("nano.default@23"))
	if !ok || manifestV23.Roots["chat"].String() != "chat.leader@5" || manifestV23.Roots["research_planner"].String() != "research.planner@7" ||
		manifestV23.Roots["research"].String() != "research.executor@15" || manifestV23.Roots["studio_report"].String() != "studio.report@2" {
		t.Fatalf("v23 manifest=%+v ok=%v", manifestV23, ok)
	}
	manifestV24, ok := catalog.ResolveRelease(MustParseReference("nano.default@24"))
	if !ok || manifestV24.Roots["chat"].String() != "chat.leader@6" || manifestV24.Roots["research_planner"].String() != "research.planner@7" ||
		manifestV24.Roots["research"].String() != "research.executor@15" || manifestV24.Roots["studio_report"].String() != "studio.report@2" {
		t.Fatalf("v24 manifest=%+v ok=%v", manifestV24, ok)
	}
	manifestV25, ok := catalog.ResolveRelease(MustParseReference("nano.default@25"))
	if !ok || manifestV25.Roots["chat"].String() != "chat.leader@6" || manifestV25.Roots["research_planner"].String() != "research.planner@7" ||
		manifestV25.Roots["research"].String() != "research.executor@16" || manifestV25.Roots["studio_report"].String() != "studio.report@2" {
		t.Fatalf("v25 manifest=%+v ok=%v", manifestV25, ok)
	}
	manifestV26, ok := catalog.ResolveRelease(MustParseReference("nano.default@26"))
	if !ok || manifestV26.Roots["research"].String() != "research.executor@17" {
		t.Fatalf("v26 manifest=%+v ok=%v", manifestV26, ok)
	}
	chatV4, ok := catalog.ResolveDefinition(MustParseReference("chat.leader@4"))
	if !ok || chatV4.Limits.LegacyPlanMutations != 12 || chatV4.Limits.ActionDecisions != 4 || chatV4.Limits.ModelCalls != 17 || chatV4.Prompts["chat_composer_bare"].String() != "agent.chat-composer-bare@3" || chatV4.Prompts["chat_composer_grounded"].String() != "agent.chat-composer-grounded@4" {
		t.Fatalf("chat v4=%+v ok=%v", chatV4, ok)
	}
	researchPolicy, ok := catalog.ResolveModelPolicy(MustParseReference("agent.deep-research-default@2"))
	if !ok || researchPolicy.TimeoutMS != 200000 || researchPolicy.MaxOutputTokens != 16384 {
		t.Fatalf("research policy=%+v ok=%v", researchPolicy, ok)
	}
	research, ok := catalog.ResolveDefinition(MustParseReference("research.executor@1"))
	if !ok || research.Limits.ModelCalls < 100 || research.Limits.Actions < 80 || research.Limits.ActionBatch < 4 {
		t.Fatalf("research definition=%+v ok=%v", research, ok)
	}
	researchV6, ok := catalog.ResolveDefinition(MustParseReference("research.executor@6"))
	if !ok || researchV6.Prompts["executor"].String() != "agent.deep-research-executor@4" || researchV6.Prompts["reporter"].String() != "agent.deep-research-reporter@3" {
		t.Fatalf("research v6 prompts=%+v ok=%v", researchV6.Prompts, ok)
	}
	researchV11, ok := catalog.ResolveDefinition(MustParseReference("research.executor@11"))
	if !ok || researchV11.Prompts["archival_compactor"].String() != "agent.deep-research-archival-compactor@2" || researchV11.Prompts["task_memory_compactor"].String() != "agent.deep-research-task-memory-compactor@2" {
		t.Fatalf("research v11 prompts=%+v ok=%v", researchV11.Prompts, ok)
	}
	researchV12, ok := catalog.ResolveDefinition(MustParseReference("research.executor@12"))
	if !ok || researchV12.Prompts["archival_compactor"].String() != "agent.deep-research-archival-compactor@3" || researchV12.Prompts["task_memory_compactor"].String() != "agent.deep-research-task-memory-compactor@3" {
		t.Fatalf("research v12 prompts=%+v ok=%v", researchV12.Prompts, ok)
	}
	researchV13, ok := catalog.ResolveDefinition(MustParseReference("research.executor@13"))
	if !ok || researchV13.Prompts["archival_compactor"].String() != "agent.deep-research-archival-compactor@3" || researchV13.Prompts["task_memory_compactor"].String() != "agent.deep-research-task-memory-compactor@4" {
		t.Fatalf("research v13 prompts=%+v ok=%v", researchV13.Prompts, ok)
	}
	researchV14, ok := catalog.ResolveDefinition(MustParseReference("research.executor@14"))
	if !ok || researchV14.Prompts["archival_compactor"].String() != "agent.deep-research-archival-compactor@3" ||
		researchV14.Prompts["task_memory_compactor"].String() != "agent.deep-research-task-memory-compactor@4" {
		t.Fatalf("research v14 prompts=%+v ok=%v", researchV14.Prompts, ok)
	}
	researchV15, ok := catalog.ResolveDefinition(MustParseReference("research.executor@15"))
	if !ok || researchV15.ModelPolicy.String() != "agent.deep-research-default@5" {
		t.Fatalf("research v15=%+v ok=%v", researchV15, ok)
	}
	researchV16, ok := catalog.ResolveDefinition(MustParseReference("research.executor@16"))
	if !ok || researchV16.ModelPolicy.String() != "agent.deep-research-default@5" || researchV16.Limits.ActionBatch != 8 {
		t.Fatalf("research v16=%+v ok=%v", researchV16, ok)
	}
	researchV17, ok := catalog.ResolveDefinition(MustParseReference("research.executor@17"))
	if !ok || len(researchV17.Skills) != 1 || researchV17.Skills[0].String() != "skill.research-workflow@2" {
		t.Fatalf("research v17=%+v ok=%v", researchV17, ok)
	}
	planner, ok := catalog.ResolveDefinition(MustParseReference("research.planner@1"))
	if !ok || len(planner.Skills) != 1 || planner.Skills[0].String() != "skill.grill-me@1" {
		t.Fatalf("planner definition=%+v ok=%v", planner, ok)
	}
}

func TestReferenceRejectsMutableOrNonCanonicalSelectors(t *testing.T) {
	for _, value := range []string{"", "latest", "chat.leader", "chat.leader@latest", "Chat.leader@1", "chat.leader@0", "chat.leader@01", "chat/leader@1", " chat.leader@1"} {
		if _, err := ParseReference(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	reference, err := ParseReference("chat.leader@12")
	if err != nil || reference.Identity != "chat.leader" || reference.Version != 12 || reference.String() != "chat.leader@12" {
		t.Fatalf("reference=%+v err=%v", reference, err)
	}
}

func TestLoadFSRejectsUnknownFieldsTrailingJSONAndIdentityConflicts(t *testing.T) {
	valid := minimalCatalogFS()
	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
		want   string
	}{
		{
			name: "unknown definition field",
			mutate: func(files fstest.MapFS) {
				files["definitions/agent.test.v1.json"] = mapFile(`{"identity":"agent.test","version":1,"executor":"test","model_policy":"model.test@1","prompts":{"main":"prompt.test@1"},"contracts":{"input":"input.test@1","result":"result.test@1"},"tools":["tool"],"children":[],"limits":{"model_calls":1,"actions":1,"action_batch":1,"context_bytes":1024,"result_bytes":1024,"attempts":1},"unknown":true}`)
			},
			want: "unknown field",
		},
		{
			name: "trailing JSON",
			mutate: func(files fstest.MapFS) {
				files["model-policies/model.test.v1.json"] = mapFile(`{"identity":"model.test","version":1,"provider_model":"provider/model","temperature":0,"max_output_tokens":128,"timeout_ms":1000}{}`)
			},
			want: "trailing",
		},
		{
			name: "path identity conflict",
			mutate: func(files fstest.MapFS) {
				files["definitions/agent.test.v2.json"] = files["definitions/agent.test.v1.json"]
			},
			want: "path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := cloneMapFS(valid)
			test.mutate(files)
			if _, err := LoadFS(files); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("err=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestCatalogRejectsUnresolvedAndCyclicConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
		want   string
	}{
		{
			name: "missing model policy",
			mutate: func(files fstest.MapFS) {
				delete(files, "model-policies/model.test.v1.json")
			},
			want: "model policy",
		},
		{
			name: "missing contract",
			mutate: func(files fstest.MapFS) {
				delete(files, "contracts/result.test.v1.schema.json")
			},
			want: "contract",
		},
		{
			name: "self cycle",
			mutate: func(files fstest.MapFS) {
				files["definitions/agent.test.v1.json"] = mapFile(`{"identity":"agent.test","version":1,"executor":"test","model_policy":"model.test@1","prompts":{"main":"prompt.test@1"},"contracts":{"input":"input.test@1","result":"result.test@1"},"tools":["tool"],"children":["agent.test@1"],"limits":{"model_calls":1,"actions":1,"action_batch":1,"context_bytes":1024,"result_bytes":1024,"attempts":1},"delegation":{"description":"call test agent"}}`)
			},
			want: "cycle",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := cloneMapFS(minimalCatalogFS())
			test.mutate(files)
			if _, err := LoadFS(files); err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("err=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateBindingsAllowsOnlyCapabilityNarrowing(t *testing.T) {
	catalog, err := LoadFS(minimalCatalogFS())
	if err != nil {
		t.Fatal(err)
	}
	bindings := Bindings{
		Prompts: map[Reference]bool{MustParseReference("prompt.test@1"): true},
		Tools:   map[string]ToolCapability{"tool": {Scheduling: ToolOrderedSync}},
		Executors: map[string]ExecutorCapability{
			"test": {
				PromptPurposes: map[string]bool{"main": true}, Tools: map[string]bool{"tool": true},
				Contracts: map[Reference]bool{MustParseReference("input.test@1"): true, MustParseReference("result.test@1"): true},
				MaxLimits: Limits{ModelCalls: 2, Actions: 2, ActionBatch: 2, ContextBytes: 2048, ResultBytes: 2048, Attempts: 2},
			},
		},
	}
	if err := catalog.ValidateBindings(bindings); err != nil {
		t.Fatal(err)
	}

	bad := bindings
	bad.Executors = map[string]ExecutorCapability{
		"test": {
			PromptPurposes: map[string]bool{"main": true}, Tools: map[string]bool{"other": true},
			Contracts: map[Reference]bool{MustParseReference("input.test@1"): true, MustParseReference("result.test@1"): true},
			MaxLimits: Limits{ModelCalls: 1, Actions: 1, ActionBatch: 1, ContextBytes: 1024, ResultBytes: 1024, Attempts: 1},
		},
	}
	if err := catalog.ValidateBindings(bad); err == nil || !strings.Contains(strings.ToLower(err.Error()), "tool") {
		t.Fatalf("capability expansion err=%v", err)
	}
}

func TestValidateBindingsAcceptsParallelToolScheduling(t *testing.T) {
	catalog, err := LoadFS(minimalCatalogFS())
	if err != nil {
		t.Fatal(err)
	}
	bindings := Bindings{
		Prompts: map[Reference]bool{MustParseReference("prompt.test@1"): true},
		Tools:   map[string]ToolCapability{"tool": {Scheduling: ToolParallel}},
		Executors: map[string]ExecutorCapability{
			"test": {
				PromptPurposes: map[string]bool{"main": true}, Tools: map[string]bool{"tool": true},
				Contracts: map[Reference]bool{MustParseReference("input.test@1"): true, MustParseReference("result.test@1"): true},
				MaxLimits: Limits{ModelCalls: 1, Actions: 1, ActionBatch: 1, ContextBytes: 1024, ResultBytes: 1024, Attempts: 1},
			},
		},
	}
	if err := catalog.ValidateBindings(bindings); err != nil {
		t.Fatal(err)
	}

	bindings.Tools = map[string]ToolCapability{"tool": {Scheduling: ToolExclusiveDelegation}}
	if err := catalog.ValidateBindings(bindings); err == nil || !strings.Contains(strings.ToLower(err.Error()), "scheduling") {
		t.Fatalf("exclusive_delegation on a regular tool binding err=%v", err)
	}
}

func TestDefinitionSkillBindingsAreImmutableAndCapabilityNarrowed(t *testing.T) {
	files := minimalCatalogFS()
	files["definitions/agent.test.v1.json"] = mapFile(`{"identity":"agent.test","version":1,"executor":"test","model_policy":"model.test@1","prompts":{"main":"prompt.test@1"},"contracts":{"input":"input.test@1","result":"result.test@1"},"skills":["skill.grill-me@1"],"tools":["tool"],"children":[],"limits":{"model_calls":1,"actions":1,"action_batch":1,"context_bytes":1024,"result_bytes":1024,"attempts":1},"delegation":{"description":"call test agent"}}`)
	catalog, err := LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := catalog.ResolveDefinition(MustParseReference("agent.test@1"))
	if !ok || len(definition.Skills) != 1 || definition.Skills[0].String() != "skill.grill-me@1" {
		t.Fatalf("definition=%+v ok=%v", definition, ok)
	}
	definition.Skills[0] = MustParseReference("mutated.skill@1")
	again, _ := catalog.ResolveDefinition(MustParseReference("agent.test@1"))
	if got := again.Skills[0].String(); got != "skill.grill-me@1" {
		t.Fatalf("catalog leaked mutable Skills slice: %s", got)
	}

	bindings := Bindings{
		Prompts: map[Reference]bool{MustParseReference("prompt.test@1"): true},
		Skills:  map[Reference]bool{MustParseReference("skill.grill-me@1"): true},
		Tools:   map[string]ToolCapability{"tool": {Scheduling: ToolOrderedSync}},
		Executors: map[string]ExecutorCapability{
			"test": {
				PromptPurposes: map[string]bool{"main": true}, Skills: map[Reference]bool{MustParseReference("skill.grill-me@1"): true}, Tools: map[string]bool{"tool": true},
				Contracts: map[Reference]bool{MustParseReference("input.test@1"): true, MustParseReference("result.test@1"): true},
				MaxLimits: Limits{ModelCalls: 1, Actions: 1, ActionBatch: 1, ContextBytes: 1024, ResultBytes: 1024, Attempts: 1},
			},
		},
	}
	if err := catalog.ValidateBindings(bindings); err != nil {
		t.Fatal(err)
	}
	delete(bindings.Executors["test"].Skills, MustParseReference("skill.grill-me@1"))
	if err := catalog.ValidateBindings(bindings); err == nil || !strings.Contains(strings.ToLower(err.Error()), "skill") {
		t.Fatalf("executor skill expansion err=%v", err)
	}
}

func TestDefinitionRejectsDuplicateSkills(t *testing.T) {
	files := minimalCatalogFS()
	files["definitions/agent.test.v1.json"] = mapFile(`{"identity":"agent.test","version":1,"executor":"test","model_policy":"model.test@1","prompts":{"main":"prompt.test@1"},"contracts":{"input":"input.test@1","result":"result.test@1"},"skills":["skill.grill-me@1","skill.grill-me@1"],"tools":["tool"],"children":[],"limits":{"model_calls":1,"actions":1,"action_batch":1,"context_bytes":1024,"result_bytes":1024,"attempts":1},"delegation":{"description":"call test agent"}}`)
	if _, err := LoadFS(files); err == nil || !strings.Contains(strings.ToLower(err.Error()), "skill") {
		t.Fatalf("duplicate skill err=%v", err)
	}
}

func TestGeneratedDelegationToolNameIsDeterministicAndBounded(t *testing.T) {
	reference := MustParseReference("research.source-discovery@1")
	name, err := DelegationToolName(reference)
	if err != nil {
		t.Fatal(err)
	}
	if name != "delegate.research.source-discovery.v1" {
		t.Fatalf("name=%q", name)
	}
	if _, err := DelegationToolName(Reference{Identity: strings.Repeat("a", 120), Version: 1}); err == nil {
		t.Fatal("accepted overlong generated MCP Tool name")
	}
}

func minimalCatalogFS() fstest.MapFS {
	return fstest.MapFS{
		"definitions/agent.test.v1.json":               mapFile(`{"identity":"agent.test","version":1,"executor":"test","model_policy":"model.test@1","prompts":{"main":"prompt.test@1"},"contracts":{"input":"input.test@1","result":"result.test@1"},"tools":["tool"],"children":[],"limits":{"model_calls":1,"actions":1,"action_batch":1,"context_bytes":1024,"result_bytes":1024,"attempts":1},"delegation":{"description":"call test agent"}}`),
		"model-policies/model.test.v1.json":            mapFile(`{"identity":"model.test","version":1,"provider_model":"provider/model","temperature":0,"max_output_tokens":128,"timeout_ms":1000}`),
		"provider-capabilities/provider.model.v1.json": mapFile(`{"identity":"provider.model","version":1,"provider_model":"provider/model","resolved_model":"model-1","context_window_tokens":1000,"max_input_tokens":900,"max_output_tokens":200,"tokenizer_identity":"test-tokenizer","tokenizer_version":"1","invocation_mode":"non_thinking"}`),
		"model-context-policies/context.test.v1.json":  mapFile(`{"identity":"context.test","version":1,"invocation_model_policy":"model.test@1","provider_capability":"provider.model@1","pinned_max_output_tokens":128,"soft_input_limit_tokens":700,"estimation_safety_tokens":100,"keep_recent_tokens":200,"summary_max_output_tokens":100,"overflow_retry_limit":1}`),
		"contracts/input.test.v1.schema.json":          mapFile(`{"identity":"input.test","version":1,"schema":{"type":"object","additionalProperties":false}}`),
		"contracts/result.test.v1.schema.json":         mapFile(`{"identity":"result.test","version":1,"schema":{"type":"object","additionalProperties":false}}`),
		"releases/nano.test.v1.json":                   mapFile(`{"identity":"nano.test","version":1,"roots":{"test":"agent.test@1"}}`),
	}
}

func mapFile(value string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(value), Mode: fs.FileMode(0o644)}
}

func cloneMapFS(source fstest.MapFS) fstest.MapFS {
	cloned := make(fstest.MapFS, len(source))
	for path, file := range source {
		copy := *file
		copy.Data = append([]byte(nil), file.Data...)
		cloned[path] = &copy
	}
	return cloned
}
