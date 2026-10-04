package agent

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/codesandbox"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	runPythonActionName    = "run_python"
	runPythonMaxCodeBytes  = 64 * 1024
	runPythonMaxInputFiles = 8
)

var runPythonOutputNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}\.(?:csv|json|txt|md)$`)

type runPythonAction struct {
	runner codesandbox.Runner
	store  objectstore.Store
	index  researchWorkspaceIndex
}

type runPythonInput struct {
	Code       string   `json:"code"`
	InputPaths []string `json:"input_paths,omitempty"`
}

type runPythonOutput struct {
	Status              string                  `json:"status"`
	Stdout              string                  `json:"stdout"`
	StdoutTruncated     bool                    `json:"stdout_truncated,omitempty"`
	Stderr              string                  `json:"stderr,omitempty"`
	StderrTruncated     bool                    `json:"stderr_truncated,omitempty"`
	Results             []string                `json:"results,omitempty"`
	ImageOutputsIgnored int                     `json:"image_outputs_ignored,omitempty"`
	Error               *runPythonErrorView     `json:"error,omitempty"`
	Files               []researchWorkspaceFile `json:"files"`
	SkippedOutputs      []runPythonSkippedView  `json:"skipped_outputs,omitempty"`
}

type runPythonErrorView struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Traceback string `json:"traceback,omitempty"`
}

type runPythonSkippedView struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// NewRunPythonAction executes model-written Python in an external sandbox. The
// sandbox receives only the code and the named workspace files; text files the
// program writes to output/ return as checkpoint-indexed data/ workspace files.
func NewRunPythonAction(runner codesandbox.Runner, store objectstore.Store, index researchWorkspaceIndex) Action {
	return &runPythonAction{runner: runner, store: store, index: index}
}

// NewResearchRunPythonAction binds run_python to the PostgreSQL workspace index.
func NewResearchRunPythonAction(runner codesandbox.Runner, pool *pgxpool.Pool, store objectstore.Store) Action {
	return NewRunPythonAction(runner, store, postgresResearchWorkspaceIndex{pool: pool})
}

// Re-running after a crash starts a fresh sandbox; outputs are stored under
// action-addressed keys, so a replay converges on the same workspace files.
func (*runPythonAction) CrashReplaySafe() bool { return true }

func (a *runPythonAction) Available(Execution) (bool, string) {
	return a != nil && a.runner != nil && a.store != nil && a.index != nil, "code_sandbox_unavailable"
}

func (*runPythonAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        runPythonActionName,
		Description: "Run Python 3 in a fresh sandbox with no internet (pandas, numpy, scipy, matplotlib installed) for calculation and data analysis. Listed workspace files are copied in at their paths. Files the code writes to output/ with lowercase .csv/.json/.txt/.md names are saved as data/<name>. Nothing else persists between calls. Returns stdout, stderr, the last expression value, and any traceback; computed numbers are not evidence and need cited inputs.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["code"],"properties":{"code":{"type":"string","minLength":1,"maxLength":65536},"input_paths":{"type":"array","maxItems":8,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":96}}}}`),
	}
}

func (*runPythonAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeRunPythonInput(raw)
	return err
}

func (a *runPythonAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeRunPythonInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if ok, reason := a.Available(Execution{}); !ok {
		return ActionResult{Status: ActionDomainError, ErrorCode: reason}, nil
	}
	files := make([]codesandbox.File, 0, len(input.InputPaths))
	if len(input.InputPaths) > 0 {
		snapshot, err := a.index.Snapshot(ctx, request.Attempt.RunID)
		if err != nil {
			return ActionResult{}, err
		}
		for _, path := range input.InputPaths {
			file, ok := snapshot.Files[path]
			if !ok {
				return ActionResult{Status: ActionDomainError, ErrorCode: "research_file_not_found"}, nil
			}
			payload, err := getResearchWorkspaceObject(ctx, a.store, file, researchWorkspaceReportMaxBytes)
			if err != nil {
				if ctx.Err() != nil {
					return ActionResult{}, ctx.Err()
				}
				return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_read_failed"}, nil
			}
			files = append(files, codesandbox.File{Path: path, Content: payload})
		}
	}
	result, err := a.runner.RunPython(ctx, codesandbox.Request{
		Code: input.Code, Files: files,
		Metadata: map[string]string{"nano_run_id": request.Attempt.RunID},
	})
	if err != nil {
		if ctx.Err() != nil {
			return ActionResult{}, ctx.Err()
		}
		switch {
		case errors.Is(err, codesandbox.ErrRateLimited):
			return ActionResult{Status: ActionDomainError, ErrorCode: "code_sandbox_rate_limited"}, nil
		case errors.Is(err, codesandbox.ErrInvalidRequest):
			return ActionResult{Status: ActionDomainError, ErrorCode: "code_sandbox_request_invalid"}, nil
		default:
			return ActionResult{Status: ActionDomainError, ErrorCode: "code_sandbox_unavailable"}, nil
		}
	}
	output := runPythonOutput{
		Status: "ok", Stdout: result.Stdout, StdoutTruncated: result.StdoutTruncated,
		Stderr: result.Stderr, StderrTruncated: result.StderrTruncated,
		Results: result.DisplayTexts, ImageOutputsIgnored: result.DisplayImages,
		Files: []researchWorkspaceFile{},
	}
	if result.Error != nil {
		output.Status = "error"
		output.Error = &runPythonErrorView{Name: result.Error.Name, Value: result.Error.Value, Traceback: result.Error.Traceback}
	}
	if result.TimedOut {
		output.Status = "timeout"
	}
	for _, skipped := range result.SkippedOutputs {
		output.SkippedOutputs = append(output.SkippedOutputs, runPythonSkippedView{Name: skipped.Name, Reason: skipped.Reason})
	}
	for _, produced := range result.Outputs {
		if !runPythonOutputNamePattern.MatchString(produced.Path) {
			output.SkippedOutputs = append(output.SkippedOutputs, runPythonSkippedView{Name: produced.Path, Reason: "unsupported_name_or_type"})
			continue
		}
		if len(produced.Content) == 0 || int64(len(produced.Content)) > researchWorkspaceFileMaxBytes || !utf8.Valid(produced.Content) {
			output.SkippedOutputs = append(output.SkippedOutputs, runPythonSkippedView{Name: produced.Path, Reason: "empty_binary_or_too_large"})
			continue
		}
		file, err := putResearchWorkspaceObject(ctx, a.store, request.Attempt.RunID, request.ActionID, "data/"+produced.Path, produced.Content, false)
		if err != nil {
			if ctx.Err() != nil {
				return ActionResult{}, ctx.Err()
			}
			return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_write_failed"}, nil
		}
		output.Files = append(output.Files, file)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: encoded}, nil
}

func decodeRunPythonInput(raw json.RawMessage) (runPythonInput, error) {
	var input runPythonInput
	if decodeExactJSON(raw, &input) != nil || strings.TrimSpace(input.Code) == "" ||
		len(input.Code) > runPythonMaxCodeBytes || !utf8.ValidString(input.Code) || len(input.InputPaths) > runPythonMaxInputFiles {
		return runPythonInput{}, errors.New("invalid run_python input")
	}
	seen := make(map[string]bool, len(input.InputPaths))
	for _, path := range input.InputPaths {
		if seen[path] || validateResearchWorkspacePath(path, true) != nil {
			return runPythonInput{}, errors.New("invalid run_python input")
		}
		seen[path] = true
	}
	return input, nil
}
