package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

type researchImportBarrierStub struct {
	waiting bool
	err     error
	calls   int
}

func (s *researchImportBarrierStub) WaitIfPending(context.Context, ActionRequest) (bool, error) {
	s.calls++
	return s.waiting, s.err
}

type researchWorkspaceIndexStub struct {
	snapshot researchWorkspaceSnapshot
	err      error
}

func (s researchWorkspaceIndexStub) Snapshot(context.Context, string) (researchWorkspaceSnapshot, error) {
	return s.snapshot, s.err
}

func TestResearchWorkspacePathIsRunScopedMarkdownOrData(t *testing.T) {
	for _, path := range []string{"report_plan.md", "review.md", "notes/codex.md", "sections/recommendation.md", "data/prices.csv", "data/summary.json"} {
		if err := validateResearchWorkspacePath(path, false); err != nil {
			t.Fatalf("valid path %q: %v", path, err)
		}
	}
	for _, path := range []string{"", "report.md", "../secret.md", "/tmp/a.md", "sections/a/b.md", "sections/a.txt", "sections/UPPER.md", "data/chart.png", "data/a/b.csv", "data/Prices.csv"} {
		if err := validateResearchWorkspacePath(path, false); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	if err := validateResearchWorkspacePath("report.md", true); err != nil {
		t.Fatalf("assembled report path: %v", err)
	}
}

func TestWriteResearchFileStoresImmutableActionAddressedObject(t *testing.T) {
	store := objectstore.NewMemoryStore()
	action := newWriteResearchFileAction(store)
	request := ActionRequest{
		ActionID: "decision:7/action:2",
		Input:    json.RawMessage(`{"path":"sections/recommendation.md","content":"## Recommendation\n\nAdopt the checkpoint boundary."}`),
		Attempt:  Attempt{RunID: "run_research"},
	}
	result, err := action.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var output researchWorkspaceFileOutput
	if result.Status != ActionSucceeded || json.Unmarshal(result.Output, &output) != nil {
		t.Fatalf("result=%+v", result)
	}
	if output.Path != "sections/recommendation.md" || output.Bytes == 0 || len(output.SHA256) != 64 || !strings.HasPrefix(output.ObjectKey, "research-workspaces/run_research/") {
		t.Fatalf("output=%+v", output)
	}
	payload, err := store.Get(context.Background(), output.ObjectKey, researchWorkspaceFileMaxBytes)
	if err != nil || string(payload) != "## Recommendation\n\nAdopt the checkpoint boundary." {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	resultAgain, err := action.Execute(context.Background(), request)
	if err != nil || string(resultAgain.Output) != string(result.Output) || store.Len() != 1 {
		t.Fatalf("replay result=%s err=%v objects=%d", resultAgain.Output, err, store.Len())
	}
}

func TestReadListAndAssembleResearchWorkspaceUseLatestCheckpointedFiles(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	first := mustWorkspaceObject(t, ctx, store, "run_research", "decision:1/action:0", "sections/choice.md", "## Choice\n\nOld")
	latest := mustWorkspaceObject(t, ctx, store, "run_research", "decision:3/action:0", "sections/choice.md", "## Choice\n\nUse the durable runtime.")
	risk := mustWorkspaceObject(t, ctx, store, "run_research", "decision:4/action:0", "sections/risk.md", "## Risk\n\nValidate sandbox semantics.")
	snapshot := researchWorkspaceSnapshot{Files: map[string]researchWorkspaceFile{
		first.Path: latest,
		risk.Path:  risk,
	}}
	index := researchWorkspaceIndexStub{snapshot: snapshot}

	read := newReadResearchFileAction(store, index)
	readResult, err := read.Execute(ctx, ActionRequest{Attempt: Attempt{RunID: "run_research"}, Input: json.RawMessage(`{"path":"sections/choice.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var readOutput researchWorkspaceReadOutput
	if json.Unmarshal(readResult.Output, &readOutput) != nil || readOutput.Content != "## Choice\n\nUse the durable runtime." || readOutput.SHA256 != latest.SHA256 {
		t.Fatalf("read=%+v", readOutput)
	}

	list := newListResearchFilesAction(index)
	listResult, err := list.Execute(ctx, ActionRequest{Attempt: Attempt{RunID: "run_research"}, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var listOutput researchWorkspaceListOutput
	if json.Unmarshal(listResult.Output, &listOutput) != nil || len(listOutput.Files) != 2 || listOutput.Files[0].Path != "sections/choice.md" || listOutput.Files[1].Path != "sections/risk.md" {
		t.Fatalf("list=%+v", listOutput)
	}

	assemble := newAssembleResearchReportAction(store, index)
	assembled, err := assemble.Execute(ctx, ActionRequest{
		ActionID: "decision:5/action:0", Attempt: Attempt{RunID: "run_research"},
		Input: json.RawMessage(`{"title":"Platform decision","section_paths":["sections/choice.md","sections/risk.md"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var assembledOutput researchWorkspaceFileOutput
	if json.Unmarshal(assembled.Output, &assembledOutput) != nil || assembledOutput.Path != "report.md" {
		t.Fatalf("assembled=%+v", assembledOutput)
	}
	var assemblyState researchWorkspaceAssemblyOutput
	if json.Unmarshal(assembled.Output, &assemblyState) != nil || assemblyState.ReviewPresent || !strings.Contains(assemblyState.Guidance, "write review.md") {
		t.Fatalf("assembly state=%+v", assemblyState)
	}
	report, err := store.Get(ctx, assembledOutput.ObjectKey, researchWorkspaceReportMaxBytes)
	if err != nil || string(report) != "# Platform decision\n\n## Choice\n\nUse the durable runtime.\n\n## Risk\n\nValidate sandbox semantics.\n" {
		t.Fatalf("report=%q err=%v", report, err)
	}
}

func TestLoadAssembledResearchReportUsesAcceptedAssemblyResult(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	assembled := mustWorkspaceObject(t, ctx, store, "run_research", "decision:9/action:0", "report.md", "# Decision\n\nUse sections.")
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 9, Actions: []AcceptedAction{{
		ActionID: "decision:9/action:0", Index: 0, Name: "assemble_research_report",
		Input:  json.RawMessage(`{"title":"Decision","section_paths":["sections/a.md"]}`),
		Result: &ActionResult{Status: ActionSucceeded, Output: mustJSON(t, researchWorkspaceFileOutput(assembled))},
	}}}}}
	if !hasAssembledResearchReport(prefix) || hasAssembledResearchReport(CheckpointPrefix{}) {
		t.Fatal("assembled report checkpoint detection is incorrect")
	}
	got, ok, err := loadAssembledResearchReport(ctx, store, prefix)
	if err != nil || !ok || got != "# Decision\n\nUse sections." {
		t.Fatalf("report=%q ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := loadAssembledResearchReport(ctx, store, CheckpointPrefix{}); err != nil || ok || got != "" {
		t.Fatalf("fallback report=%q ok=%v err=%v", got, ok, err)
	}
}

func TestAssembleResearchReportYieldsAttemptWhilePDFImportsArePending(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	section := mustWorkspaceObject(t, ctx, store, "run_research", "decision:1/action:0", "sections/choice.md", "## Choice\n\nWait for the source.")
	barrier := &researchImportBarrierStub{waiting: true}
	action := newAssembleResearchReportAction(store, researchWorkspaceIndexStub{snapshot: researchWorkspaceSnapshot{Files: map[string]researchWorkspaceFile{
		section.Path: section,
	}}}, barrier)

	result, err := action.Execute(ctx, ActionRequest{
		ActionID:   "decision:2/action:0",
		Attempt:    Attempt{RunID: "run_research", JobID: "job_research", AttemptNo: 2, LeaseToken: "lease"},
		Definition: agentcatalog.Reference{Identity: "research.executor", Version: 9},
		Input:      json.RawMessage(`{"title":"Decision","section_paths":["sections/choice.md"]}`),
	})
	if !errors.Is(err, ErrLeaseLost) || result.Status != "" || barrier.calls != 1 || store.Len() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d objects=%d", result, err, barrier.calls, store.Len())
	}
}

func TestResearchWorkspaceToolsAreCrashReplaySafe(t *testing.T) {
	store := objectstore.NewMemoryStore()
	index := researchWorkspaceIndexStub{snapshot: researchWorkspaceSnapshot{Files: map[string]researchWorkspaceFile{}}}
	for _, action := range []Action{
		newWriteResearchFileAction(store), newEditResearchFileAction(store, index), newReadResearchFileAction(store, index),
		newListResearchFilesAction(index), newAssembleResearchReportAction(store, index),
	} {
		policy, ok := action.(CrashReplayPolicy)
		if !ok || !policy.CrashReplaySafe() {
			t.Fatalf("%s is not crash replay safe", action.Definition().Name)
		}
	}
}

func mustWorkspaceObject(t *testing.T, ctx context.Context, store objectstore.Store, runID, actionID, path, content string) researchWorkspaceFile {
	t.Helper()
	if path == "report.md" {
		file, err := putResearchWorkspaceObject(ctx, store, runID, actionID, path, []byte(content), true)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	result, err := newWriteResearchFileAction(store).Execute(ctx, ActionRequest{
		ActionID: actionID, Attempt: Attempt{RunID: runID},
		Input: mustJSON(t, map[string]string{"path": path, "content": content}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output researchWorkspaceFileOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	return researchWorkspaceFile(output)
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAssembleInputErrorsNameTheProblem(t *testing.T) {
	for raw, want := range map[string]string{
		`{"title":"T","section_paths":["report_plan.md"]}`:                `"report_plan.md" must look like sections/`,
		`{"title":"T","section_paths":["sections/a.md","sections/a.md"]}`: "is repeated",
		`{"title":"","section_paths":["sections/a.md"]}`:                  "title must be",
		`{"title":"T","section_paths":[]}`:                                "got 0",
		`{"title":"T","section_paths":["sections/a.md"],"order":["x"]}`:   "pass only title and section_paths",
	} {
		_, err := decodeAssembleResearchReportInput(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s err=%v want %q", raw, err, want)
		}
	}
}

func TestEditResearchFileReplacesExactTextAndKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	original := mustWorkspaceObject(t, ctx, store, "run_research", "decision:1/action:0", "sections/choice.md",
		"## Choice\n\nUse the durable runtime.\n\nIt resumes after a crash.\n")
	index := researchWorkspaceIndexStub{snapshot: researchWorkspaceSnapshot{Files: map[string]researchWorkspaceFile{original.Path: original}}}
	action := newEditResearchFileAction(store, index)
	edit := func(actionID string, input map[string]any) ActionResult {
		t.Helper()
		result, err := action.Execute(ctx, ActionRequest{ActionID: actionID, Attempt: Attempt{RunID: "run_research"}, Input: mustJSON(t, input)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	result := edit("decision:2/action:0", map[string]any{"path": "sections/choice.md", "old_string": "durable runtime", "new_string": "checkpointed runtime"})
	var output researchWorkspaceFileOutput
	if result.Status != ActionSucceeded || json.Unmarshal(result.Output, &output) != nil || output.Path != "sections/choice.md" || output.SHA256 == original.SHA256 {
		t.Fatalf("result=%+v", result)
	}
	payload, err := store.Get(ctx, output.ObjectKey, researchWorkspaceFileMaxBytes)
	if err != nil || string(payload) != "## Choice\n\nUse the checkpointed runtime.\n\nIt resumes after a crash.\n" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	if old, err := store.Get(ctx, original.ObjectKey, researchWorkspaceFileMaxBytes); err != nil || !strings.Contains(string(old), "durable runtime") {
		t.Fatalf("the previous version must stay: %q err=%v", old, err)
	}
	if again := edit("decision:2/action:0", map[string]any{"path": "sections/choice.md", "old_string": "durable runtime", "new_string": "checkpointed runtime"}); string(again.Output) != string(result.Output) {
		t.Fatalf("replay differs: %s vs %s", again.Output, result.Output)
	}

	for name, tc := range map[string]struct {
		input map[string]any
		code  string
	}{
		"missing text":    {map[string]any{"path": "sections/choice.md", "old_string": "absent", "new_string": "x"}, "research_edit_not_found"},
		"missing file":    {map[string]any{"path": "sections/other.md", "old_string": "a", "new_string": "b"}, "research_file_not_found"},
		"emptied file":    {map[string]any{"path": "sections/choice.md", "old_string": original.Path, "new_string": ""}, "research_edit_not_found"},
		"ambiguous match": {map[string]any{"path": "sections/choice.md", "old_string": "e", "new_string": "E"}, "research_edit_not_unique"},
	} {
		if got := edit("decision:9/action:0", tc.input); got.Status != ActionDomainError || got.ErrorCode != tc.code {
			t.Errorf("%s: result=%+v want %s", name, got, tc.code)
		}
	}
	if all := edit("decision:10/action:0", map[string]any{"path": "sections/choice.md", "old_string": "e", "new_string": "E", "replace_all": true}); all.Status != ActionSucceeded {
		t.Fatalf("replace_all=%+v", all)
	}
}

func TestEditResearchFileRejectsUnsafeInput(t *testing.T) {
	action := newEditResearchFileAction(objectstore.NewMemoryStore(), researchWorkspaceIndexStub{})
	for _, raw := range []string{
		`{"path":"report.md","old_string":"a","new_string":"b"}`,
		`{"path":"claims.md","old_string":"a","new_string":"b"}`,
		`{"path":"sections/a.md","old_string":"","new_string":"b"}`,
		`{"path":"sections/a.md","old_string":"same","new_string":"same"}`,
		`{"path":"sections/a.md","old_string":"a"}`,
	} {
		if err := action.ValidateInput(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := action.ValidateInput(json.RawMessage(`{"path":"sections/a.md","old_string":"a","new_string":""}`)); err != nil {
		t.Errorf("deleting text must be allowed: %v", err)
	}
}

func TestResearchWorkspaceSnapshotFollowsEditedVersion(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	written := mustWorkspaceObject(t, ctx, store, "run_research", "decision:1/action:0", "sections/choice.md", "A one")
	edited := mustWorkspaceObject(t, ctx, store, "run_research", "decision:2/action:0", "sections/choice.md", "A two")
	output := func(file researchWorkspaceFile) *ActionResult {
		return &ActionResult{Status: ActionSucceeded, Output: mustJSON(t, file)}
	}
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{{Name: "write_research_file", Result: output(written)}}},
		{DecisionNo: 2, Actions: []AcceptedAction{{Name: editResearchFileActionName, Result: output(edited)}}},
	}}
	if got := researchWorkspaceSnapshotFromPrefix(prefix).Files["sections/choice.md"]; got.SHA256 != edited.SHA256 {
		t.Fatalf("snapshot=%+v want the edited version", got)
	}
}

func TestAssembleGuidanceFlagsSectionsEditedAfterReview(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	file := func(actionID, path, content string) researchWorkspaceFile {
		return mustWorkspaceObject(t, ctx, store, "run_research", actionID, path, content)
	}
	result := func(f researchWorkspaceFile) *ActionResult {
		return &ActionResult{Status: ActionSucceeded, Output: mustJSON(t, f)}
	}
	intro1, results := file("d1", "sections/intro.md", "## Intro\n\nv1"), file("d2", "sections/results.md", "## Results\n\nbody")
	review, intro2 := file("d3", "review.md", "fine"), file("d4", "sections/intro.md", "## Intro\n\nv2")
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{{Name: "write_research_file", Result: result(intro1)}, {Name: "write_research_file", Result: result(results)}}},
		{DecisionNo: 2, Actions: []AcceptedAction{{Name: "write_research_file", Result: result(review)}}},
		{DecisionNo: 3, Actions: []AcceptedAction{{Name: editResearchFileActionName, Result: result(intro2)}}},
	}}
	snapshot := researchWorkspaceSnapshotFromPrefix(prefix)
	if got := sectionsEditedAfterReview(snapshot, []string{"sections/intro.md", "sections/results.md"}); len(got) != 1 || got[0] != "sections/intro.md" {
		t.Fatalf("changed=%v", got)
	}
	assembled, err := newAssembleResearchReportAction(store, researchWorkspaceIndexStub{snapshot: snapshot}).Execute(ctx, ActionRequest{
		ActionID: "d5", Attempt: Attempt{RunID: "run_research"},
		Input: json.RawMessage(`{"title":"T","section_paths":["sections/intro.md","sections/results.md"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var state researchWorkspaceAssemblyOutput
	if json.Unmarshal(assembled.Output, &state) != nil || !strings.Contains(state.Guidance, "sections/intro.md changed after review.md") || strings.Contains(state.Guidance, "sections/results.md changed") {
		t.Fatalf("guidance=%q", state.Guidance)
	}
	if got := sectionsEditedAfterReview(researchWorkspaceSnapshot{Files: snapshot.Files}, []string{"sections/intro.md"}); got != nil {
		t.Fatalf("a snapshot without order must not flag sections: %v", got)
	}
}
