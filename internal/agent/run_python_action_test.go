package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/codesandbox"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

type codeRunnerStub struct {
	request codesandbox.Request
	result  codesandbox.Result
	err     error
	calls   int
}

func (s *codeRunnerStub) RunPython(_ context.Context, request codesandbox.Request) (codesandbox.Result, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

func TestRunPythonCopiesWorkspaceInputsAndSavesTextOutputsAsDataFiles(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	input := mustWorkspaceObject(t, ctx, store, "run_research", "decision:1/action:1", "data/prices.csv", "year,price\n2025,10\n2026,12\n")
	runner := &codeRunnerStub{result: codesandbox.Result{
		Stdout: "growth=20%\n", DisplayTexts: []string{"0.2"}, DisplayImages: 1, OutputsCollected: true,
		Outputs: []codesandbox.File{
			{Path: "growth.csv", Content: []byte("year,growth\n2026,0.2\n")},
			{Path: "chart.png", Content: []byte{0x89, 'P', 'N', 'G'}},
			{Path: "blob.txt", Content: []byte{0xff, 0xfe}},
		},
		SkippedOutputs: []codesandbox.SkippedOutput{{Name: "link.csv", Reason: "not_regular_file"}},
	}}
	action := NewRunPythonAction(runner, store, researchWorkspaceIndexStub{snapshot: researchWorkspaceSnapshot{
		Files: map[string]researchWorkspaceFile{input.Path: input},
	}})
	result, err := action.Execute(ctx, ActionRequest{
		ActionID: "decision:2/action:1", Attempt: Attempt{RunID: "run_research"},
		Input: json.RawMessage(`{"code":"import pandas as pd\nprint('growth=20%')","input_paths":["data/prices.csv"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil || result.Status != ActionSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(runner.request.Files) != 1 || runner.request.Files[0].Path != "data/prices.csv" ||
		string(runner.request.Files[0].Content) != "year,price\n2025,10\n2026,12\n" || runner.request.Metadata["nano_run_id"] != "run_research" {
		t.Fatalf("sandbox request=%+v", runner.request)
	}
	var output runPythonOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != "ok" || output.Stdout != "growth=20%\n" || output.ImageOutputsIgnored != 1 || len(output.Results) != 1 {
		t.Fatalf("output=%+v", output)
	}
	if len(output.Files) != 1 || output.Files[0].Path != "data/growth.csv" {
		t.Fatalf("files=%+v", output.Files)
	}
	reasons := map[string]string{}
	for _, skipped := range output.SkippedOutputs {
		reasons[skipped.Name] = skipped.Reason
	}
	if reasons["chart.png"] != "unsupported_name_or_type" || reasons["blob.txt"] != "empty_binary_or_too_large" || reasons["link.csv"] != "not_regular_file" {
		t.Fatalf("skipped=%+v", output.SkippedOutputs)
	}

	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 2, Actions: []AcceptedAction{{
		ActionID: "decision:2/action:1", Name: runPythonActionName, Result: &result,
	}}}}}
	snapshot := researchWorkspaceSnapshotFromPrefix(prefix)
	saved, ok := snapshot.Files["data/growth.csv"]
	if !ok {
		t.Fatalf("snapshot=%+v", snapshot.Files)
	}
	payload, err := getResearchWorkspaceObject(ctx, store, saved, researchWorkspaceFileMaxBytes)
	if err != nil || string(payload) != "year,growth\n2026,0.2\n" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestRunPythonReportsPythonFailureAsSucceededToolResult(t *testing.T) {
	runner := &codeRunnerStub{result: codesandbox.Result{
		Error: &codesandbox.ExecutionError{Name: "ZeroDivisionError", Value: "division by zero", Traceback: "ZeroDivisionError: division by zero"},
	}}
	action := NewRunPythonAction(runner, objectstore.NewMemoryStore(), researchWorkspaceIndexStub{})
	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "a1", Attempt: Attempt{RunID: "run"}, Input: json.RawMessage(`{"code":"1/0"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output runPythonOutput
	if result.Status != ActionSucceeded || json.Unmarshal(result.Output, &output) != nil || output.Status != "error" || output.Error.Name != "ZeroDivisionError" {
		t.Fatalf("result=%+v output=%+v", result, output)
	}
}

func TestRunPythonMapsProviderFailuresToCatalogedDomainErrors(t *testing.T) {
	for err, code := range map[error]string{
		codesandbox.ErrRateLimited:    "code_sandbox_rate_limited",
		codesandbox.ErrUnavailable:    "code_sandbox_unavailable",
		codesandbox.ErrInvalidRequest: "code_sandbox_request_invalid",
	} {
		action := NewRunPythonAction(&codeRunnerStub{err: err}, objectstore.NewMemoryStore(), researchWorkspaceIndexStub{})
		result, execErr := action.Execute(context.Background(), ActionRequest{
			ActionID: "a1", Attempt: Attempt{RunID: "run"}, Input: json.RawMessage(`{"code":"print(1)"}`),
		})
		if execErr != nil || result.Status != ActionDomainError || result.ErrorCode != code {
			t.Fatalf("err=%v result=%+v", execErr, result)
		}
		if _, ok := actionErrorCatalog[code]; !ok {
			t.Fatalf("code %q is not cataloged", code)
		}
	}
}

func TestRunPythonRejectsMissingInputsWithoutStartingSandbox(t *testing.T) {
	runner := &codeRunnerStub{}
	action := NewRunPythonAction(runner, objectstore.NewMemoryStore(), researchWorkspaceIndexStub{})
	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "a1", Attempt: Attempt{RunID: "run"}, Input: json.RawMessage(`{"code":"1","input_paths":["data/missing.csv"]}`),
	})
	if err != nil || result.ErrorCode != "research_file_not_found" || runner.calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, runner.calls)
	}
	for _, raw := range []string{`{"code":""}`, `{"code":"1","input_paths":["../x.csv"]}`, `{"code":"1","input_paths":["data/a.csv","data/a.csv"]}`, `{"code":"1","extra":true}`} {
		if action.ValidateInput(json.RawMessage(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestRunPythonIsHiddenWithoutConfiguredSandbox(t *testing.T) {
	action := NewRunPythonAction(nil, objectstore.NewMemoryStore(), researchWorkspaceIndexStub{})
	available, reason := action.(ActionAvailability).Available(Execution{})
	if available || reason != "code_sandbox_unavailable" {
		t.Fatalf("available=%v reason=%q", available, reason)
	}
	if _, err := NewActionRegistry(action); err != nil {
		t.Fatalf("definition rejected: %v", err)
	}
}
