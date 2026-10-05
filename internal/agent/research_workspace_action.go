package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	researchWorkspaceFileMaxBytes   int64 = 96 * 1024
	researchWorkspaceReportMaxBytes int64 = maxFinalDraftCheckpointBytes
)

var researchWorkspaceNestedPathPattern = regexp.MustCompile(`^(?:notes|sections)/[a-z0-9][a-z0-9._-]{0,79}\.md$`)

// Data files hold tabular or structured intermediate results, written directly
// or saved from run_python output; they are never report sections.
var researchWorkspaceDataPathPattern = regexp.MustCompile(`^data/[a-z0-9][a-z0-9._-]{0,79}\.(?:csv|json|txt|md)$`)

type researchWorkspaceFileOutput struct {
	Path      string `json:"path"`
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

type researchWorkspaceFile = researchWorkspaceFileOutput

type researchWorkspaceReadOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

type researchWorkspaceListOutput struct {
	Files []researchWorkspaceFile `json:"files"`
}

type researchWorkspaceAssemblyOutput struct {
	researchWorkspaceFileOutput
	ReviewPresent bool                      `json:"review_present"`
	Guidance      string                    `json:"guidance"`
	CitationCheck *researchCitationCheck    `json:"citation_check,omitempty"`
	Coverage      *researchSourceCoverage   `json:"source_coverage,omitempty"`
	Running       []researchRunningSubagent `json:"running_subagents,omitempty"`
	QueuedReads   []researchQueuedRead      `json:"queued_reads,omitempty"`
}

type researchWorkspaceSnapshot struct {
	Files map[string]researchWorkspaceFile
}

type researchWorkspaceIndex interface {
	Snapshot(context.Context, string) (researchWorkspaceSnapshot, error)
}

type postgresResearchWorkspaceIndex struct {
	pool *pgxpool.Pool
}

func (i postgresResearchWorkspaceIndex) Snapshot(ctx context.Context, runID string) (researchWorkspaceSnapshot, error) {
	if i.pool == nil || strings.TrimSpace(runID) == "" {
		return researchWorkspaceSnapshot{}, errors.New("Research workspace checkpoint index is unavailable")
	}
	rows, err := i.pool.Query(ctx, `
		select `+selectCheckpointColumns+`
		from agent_run_checkpoints
		where run_id=$1
		order by sequence_no
	`, runID)
	if err != nil {
		return researchWorkspaceSnapshot{}, err
	}
	defer rows.Close()
	checkpoints := make([]Checkpoint, 0)
	for rows.Next() {
		checkpoint, err := scanCheckpoint(rows)
		if err != nil {
			return researchWorkspaceSnapshot{}, err
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return researchWorkspaceSnapshot{}, err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return researchWorkspaceSnapshot{}, err
	}
	return researchWorkspaceSnapshotFromPrefix(prefix), nil
}

func researchWorkspaceSnapshotFromPrefix(prefix CheckpointPrefix) researchWorkspaceSnapshot {
	snapshot := researchWorkspaceSnapshot{Files: make(map[string]researchWorkspaceFile)}
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Result == nil || action.Result.Status != ActionSucceeded {
				continue
			}
			if action.Name == runPythonActionName {
				var output struct {
					Files []researchWorkspaceFile `json:"files"`
				}
				if json.Unmarshal(action.Result.Output, &output) == nil {
					for _, file := range output.Files {
						if validateResearchWorkspaceFile(file) == nil && researchWorkspaceDataPathPattern.MatchString(file.Path) {
							snapshot.Files[file.Path] = file
						}
					}
				}
				continue
			}
			if action.Name != "write_research_file" && action.Name != "assemble_research_report" {
				continue
			}
			var file researchWorkspaceFile
			if json.Unmarshal(action.Result.Output, &file) != nil || validateResearchWorkspaceFile(file) != nil {
				continue
			}
			snapshot.Files[file.Path] = file
		}
	}
	return snapshot
}

type writeResearchFileAction struct {
	store objectstore.Store
}

type writeResearchFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func newWriteResearchFileAction(store objectstore.Store) Action {
	return &writeResearchFileAction{store: store}
}

func (*writeResearchFileAction) CrashReplaySafe() bool { return true }

func (a *writeResearchFileAction) Available(Execution) (bool, string) {
	return a != nil && a.store != nil, "research_workspace_unavailable"
}

func (*writeResearchFileAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "write_research_file",
		Description: "Persist one bounded Markdown planning, note, review, or report-section file, or a data/<name>.csv|json|txt|md data file, in this Research Run's durable MinIO workspace. Rewriting a logical path creates a new immutable version.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","content"],"properties":{"path":{"type":"string","minLength":1,"maxLength":96},"content":{"type":"string","minLength":1,"maxLength":98304}}}`),
	}
}

func (*writeResearchFileAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeWriteResearchFileInput(raw)
	return err
}

func (a *writeResearchFileAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeWriteResearchFileInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.store == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_unavailable"}, nil
	}
	file, err := putResearchWorkspaceObject(ctx, a.store, request.Attempt.RunID, request.ActionID, input.Path, []byte(input.Content), false)
	if err != nil {
		if ctx.Err() != nil {
			return ActionResult{}, ctx.Err()
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_write_failed"}, nil
	}
	output, err := json.Marshal(file)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

func decodeWriteResearchFileInput(raw json.RawMessage) (writeResearchFileInput, error) {
	var input writeResearchFileInput
	if decodeExactJSON(raw, &input) != nil || validateResearchWorkspacePath(input.Path, false) != nil ||
		strings.TrimSpace(input.Content) == "" || len([]byte(input.Content)) > int(researchWorkspaceFileMaxBytes) || !utf8.ValidString(input.Content) {
		return writeResearchFileInput{}, errors.New("invalid write_research_file input")
	}
	return input, nil
}

type readResearchFileAction struct {
	store  objectstore.Store
	index  researchWorkspaceIndex
	claims researchClaimsReader
}

type readResearchFileInput struct {
	Path string `json:"path"`
}

func newReadResearchFileAction(store objectstore.Store, index researchWorkspaceIndex, claims ...researchClaimsReader) Action {
	action := &readResearchFileAction{store: store, index: index}
	if len(claims) > 0 {
		action.claims = claims[0]
	}
	return action
}

func (*readResearchFileAction) CrashReplaySafe() bool { return true }

func (a *readResearchFileAction) Available(Execution) (bool, string) {
	return a != nil && a.store != nil && a.index != nil, "research_workspace_unavailable"
}

func (*readResearchFileAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "read_research_file",
		Description: "Read the latest checkpoint-accepted version of one logical Markdown or data file from this Research Run's MinIO workspace. claims.md always lists every recorded claim card.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":96}}}`),
	}
}

func (*readResearchFileAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeReadResearchFileInput(raw)
	return err
}

func (a *readResearchFileAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeReadResearchFileInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.store == nil || a.index == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_unavailable"}, nil
	}
	if input.Path == researchClaimsPath && a.claims != nil {
		content, err := a.claims.ClaimsMarkdown(ctx, request.Attempt.RunID)
		if err != nil {
			return ActionResult{}, err
		}
		digest := sha256.Sum256([]byte(content))
		output, err := json.Marshal(researchWorkspaceReadOutput{Path: researchClaimsPath, Content: content, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content))})
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Status: ActionSucceeded, Output: output}, nil
	}
	snapshot, err := a.index.Snapshot(ctx, request.Attempt.RunID)
	if err != nil {
		return ActionResult{}, err
	}
	file, ok := snapshot.Files[input.Path]
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
	output, err := json.Marshal(researchWorkspaceReadOutput{Path: file.Path, Content: string(payload), SHA256: file.SHA256, Bytes: file.Bytes})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

func decodeReadResearchFileInput(raw json.RawMessage) (readResearchFileInput, error) {
	var input readResearchFileInput
	if decodeExactJSON(raw, &input) != nil || (input.Path != researchClaimsPath && validateResearchWorkspacePath(input.Path, true) != nil) {
		return readResearchFileInput{}, errors.New("invalid read_research_file input")
	}
	return input, nil
}

type listResearchFilesAction struct {
	index researchWorkspaceIndex
}

func newListResearchFilesAction(index researchWorkspaceIndex) Action {
	return &listResearchFilesAction{index: index}
}

func (*listResearchFilesAction) CrashReplaySafe() bool { return true }

func (a *listResearchFilesAction) Available(Execution) (bool, string) {
	return a != nil && a.index != nil, "research_workspace_unavailable"
}

func (*listResearchFilesAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "list_research_files",
		Description: "List the latest checkpoint-accepted logical files and hashes in this Research Run's MinIO workspace without returning their bodies.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
}

func (*listResearchFilesAction) ValidateInput(raw json.RawMessage) error {
	var input struct{}
	if decodeExactJSON(raw, &input) != nil {
		return errors.New("invalid list_research_files input")
	}
	return nil
}

func (a *listResearchFilesAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := a.ValidateInput(request.Input); err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.index == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_unavailable"}, nil
	}
	snapshot, err := a.index.Snapshot(ctx, request.Attempt.RunID)
	if err != nil {
		return ActionResult{}, err
	}
	paths := make([]string, 0, len(snapshot.Files))
	for path := range snapshot.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]researchWorkspaceFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, snapshot.Files[path])
	}
	output, err := json.Marshal(researchWorkspaceListOutput{Files: files})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

type assembleResearchReportAction struct {
	store   objectstore.Store
	index   researchWorkspaceIndex
	barrier researchSourceImportBarrier
	claims  researchClaimsSource
	sources researchReadSourceLister
	agents  researchSubagentLister
}

type assembleResearchReportInput struct {
	Title        string   `json:"title"`
	SectionPaths []string `json:"section_paths"`
}

func newAssembleResearchReportAction(store objectstore.Store, index researchWorkspaceIndex, barriers ...researchSourceImportBarrier) Action {
	var barrier researchSourceImportBarrier
	if len(barriers) > 0 {
		barrier = barriers[0]
	}
	return &assembleResearchReportAction{store: store, index: index, barrier: barrier}
}

func (*assembleResearchReportAction) CrashReplaySafe() bool { return true }

func (a *assembleResearchReportAction) Available(Execution) (bool, string) {
	return a != nil && a.store != nil && a.index != nil, "research_workspace_unavailable"
}

func (*assembleResearchReportAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "assemble_research_report",
		Description: "Deterministically concatenate selected current section files into the immutable final report artifact after planning, drafting, review, and revision are complete.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["title","section_paths"],"properties":{"title":{"type":"string","minLength":1,"maxLength":500},"section_paths":{"type":"array","minItems":1,"maxItems":24,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":96,"pattern":"^sections/[a-z0-9][a-z0-9._-]{0,79}\\.md$"}}}}`),
	}
}

func (*assembleResearchReportAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeAssembleResearchReportInput(raw)
	return err
}

func (a *assembleResearchReportAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeAssembleResearchReportInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.store == nil || a.index == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_unavailable"}, nil
	}
	if a.barrier != nil {
		waiting, err := a.barrier.WaitIfPending(ctx, request)
		if err != nil {
			return ActionResult{}, err
		}
		if waiting {
			return ActionResult{}, ErrLeaseLost
		}
	}
	snapshot, err := a.index.Snapshot(ctx, request.Attempt.RunID)
	if err != nil {
		return ActionResult{}, err
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s", strings.TrimSpace(input.Title))
	for _, path := range input.SectionPaths {
		file, ok := snapshot.Files[path]
		if !ok {
			return ActionResult{Status: ActionDomainError, ErrorCode: "research_file_not_found"}, nil
		}
		payload, err := getResearchWorkspaceObject(ctx, a.store, file, researchWorkspaceFileMaxBytes)
		if err != nil {
			if ctx.Err() != nil {
				return ActionResult{}, ctx.Err()
			}
			return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_read_failed"}, nil
		}
		content := strings.TrimSpace(string(payload))
		if content != "" {
			builder.WriteString("\n\n")
			builder.WriteString(content)
		}
	}
	builder.WriteByte('\n')
	if builder.Len() > int(researchWorkspaceReportMaxBytes) {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_report_too_large"}, nil
	}
	file, err := putResearchWorkspaceObject(ctx, a.store, request.Attempt.RunID, request.ActionID, "report.md", []byte(builder.String()), true)
	if err != nil {
		if ctx.Err() != nil {
			return ActionResult{}, ctx.Err()
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_write_failed"}, nil
	}
	reviewPresent := false
	if _, ok := snapshot.Files["review.md"]; ok {
		reviewPresent = true
	}
	guidance := "Assembly succeeded. Before Final, read the assembled sections as a whole, write review.md, revise weak sections, and assemble again."
	if reviewPresent {
		guidance = "Assembly succeeded with a checkpoint-accepted review.md. Return Final only if the reviewed sections satisfy the accepted plan."
	}
	var citationCheck *researchCitationCheck
	var coverage *researchSourceCoverage
	if a.claims != nil {
		claims, err := a.claims.ResearchClaims(ctx, request.Attempt.RunID)
		if err != nil {
			return ActionResult{}, err
		}
		check := checkResearchCitationNumbers(builder.String(), claims)
		citationCheck = &check
		if advice := researchCitationCheckGuidance(check); advice != "" {
			guidance += " " + advice
		}
		if a.sources != nil {
			sources, err := a.sources.ResearchReadSources(ctx, request.Attempt.RunID)
			if err != nil {
				return ActionResult{}, err
			}
			report := builder.String()
			measured := checkResearchSourceCoverage(report, claims, sources)
			if measured.ReadSources < researchBreadthTarget {
				if measured.UnreadLeads, err = a.sources.ResearchUnreadLeads(ctx, request.Attempt.RunID, sourceCoverageMaxLeads); err != nil {
					return ActionResult{}, err
				}
			}
			coverage = &measured
			if advice := researchSourceCoverageGuidance(measured); advice != "" {
				guidance += " " + advice
			}
		}
	}
	var running []researchRunningSubagent
	if a.agents != nil {
		if running, err = a.agents.RunningResearchSubagents(ctx, request.Attempt.RunID); err != nil {
			return ActionResult{}, err
		}
		if advice := researchRunningSubagentsGuidance(running); advice != "" {
			guidance += " " + advice
		}
	}
	var queued []researchQueuedRead
	if lister, ok := a.agents.(researchQueuedReadLister); ok && request.Definition.Identity == "research.executor" && request.Definition.Version >= researchQueuedReaderVersion {
		if queued, err = lister.QueuedResearchReads(ctx, request.Attempt.RunID); err != nil {
			return ActionResult{}, err
		}
		if advice := researchQueuedReadsGuidance(queued); advice != "" {
			guidance += " " + advice
		}
	}
	output, err := json.Marshal(researchWorkspaceAssemblyOutput{
		researchWorkspaceFileOutput: file, ReviewPresent: reviewPresent, Guidance: guidance, CitationCheck: citationCheck, Coverage: coverage, Running: running, QueuedReads: queued,
	})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

// decodeAssembleResearchReportInput names the exact problem, because the
// controller feeds validation failures back to the model for a retry.
func decodeAssembleResearchReportInput(raw json.RawMessage) (assembleResearchReportInput, error) {
	var input assembleResearchReportInput
	if err := decodeExactJSON(raw, &input); err != nil {
		return assembleResearchReportInput{}, fmt.Errorf("invalid assemble_research_report input: %v; pass only title and section_paths", err)
	}
	switch {
	case strings.TrimSpace(input.Title) == "" || len([]rune(input.Title)) > 500:
		return assembleResearchReportInput{}, errors.New("invalid assemble_research_report input: title must be 1 to 500 characters")
	case len(input.SectionPaths) < 1 || len(input.SectionPaths) > 24:
		return assembleResearchReportInput{}, fmt.Errorf("invalid assemble_research_report input: section_paths must list 1 to 24 files, got %d", len(input.SectionPaths))
	}
	seen := make(map[string]bool, len(input.SectionPaths))
	for _, path := range input.SectionPaths {
		switch {
		case seen[path]:
			return assembleResearchReportInput{}, fmt.Errorf("invalid assemble_research_report input: section path %q is repeated", path)
		case !strings.HasPrefix(path, "sections/") || validateResearchWorkspacePath(path, false) != nil:
			return assembleResearchReportInput{}, fmt.Errorf("invalid assemble_research_report input: section path %q must look like sections/<lowercase-name>.md and be a file you wrote; report_plan.md, review.md, and data files are not sections", path)
		}
		seen[path] = true
	}
	return input, nil
}

func validateResearchWorkspacePath(path string, allowReport bool) error {
	if path == "report_plan.md" || path == "review.md" || (allowReport && path == "report.md") ||
		researchWorkspaceNestedPathPattern.MatchString(path) || researchWorkspaceDataPathPattern.MatchString(path) {
		return nil
	}
	return errors.New("invalid Research workspace path")
}

func putResearchWorkspaceObject(ctx context.Context, store objectstore.Store, runID, actionID, path string, payload []byte, allowReport bool) (researchWorkspaceFile, error) {
	if store == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(actionID) == "" || validateResearchWorkspacePath(path, allowReport) != nil || len(payload) == 0 || !utf8.Valid(payload) {
		return researchWorkspaceFile{}, errors.New("invalid Research workspace object")
	}
	digest := sha256.Sum256(payload)
	identity := sha256.Sum256([]byte(runID + "\x00" + actionID))
	objectKey := fmt.Sprintf("research-workspaces/%s/%s/%s%s", runID, hex.EncodeToString(identity[:]), hex.EncodeToString(digest[:]), path[strings.LastIndexByte(path, '.'):])
	if err := store.Put(ctx, objectKey, payload); err != nil {
		return researchWorkspaceFile{}, err
	}
	return researchWorkspaceFile{Path: path, ObjectKey: objectKey, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(payload))}, nil
}

func getResearchWorkspaceObject(ctx context.Context, store objectstore.Store, file researchWorkspaceFile, maxBytes int64) ([]byte, error) {
	if store == nil || validateResearchWorkspaceFile(file) != nil || maxBytes < 1 || file.Bytes > maxBytes {
		return nil, errors.New("invalid Research workspace object reference")
	}
	payload, err := store.Get(ctx, file.ObjectKey, maxBytes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	if int64(len(payload)) != file.Bytes || hex.EncodeToString(digest[:]) != file.SHA256 {
		return nil, errors.New("Research workspace object integrity mismatch")
	}
	return payload, nil
}

func validateResearchWorkspaceFile(file researchWorkspaceFile) error {
	if validateResearchWorkspacePath(file.Path, true) != nil || !strings.HasPrefix(file.ObjectKey, "research-workspaces/") ||
		len(file.SHA256) != 64 || file.Bytes < 1 || file.Bytes > researchWorkspaceReportMaxBytes {
		return errors.New("invalid Research workspace file reference")
	}
	if _, err := hex.DecodeString(file.SHA256); err != nil {
		return errors.New("invalid Research workspace file hash")
	}
	return nil
}

func loadAssembledResearchReport(ctx context.Context, store objectstore.Store, prefix CheckpointPrefix) (string, bool, error) {
	if store == nil {
		return "", false, nil
	}
	file, ok := researchWorkspaceSnapshotFromPrefix(prefix).Files["report.md"]
	if !ok {
		return "", false, nil
	}
	payload, err := getResearchWorkspaceObject(ctx, store, file, researchWorkspaceReportMaxBytes)
	if err != nil {
		return "", false, err
	}
	return string(payload), true, nil
}

func hasAssembledResearchReport(prefix CheckpointPrefix) bool {
	_, ok := researchWorkspaceSnapshotFromPrefix(prefix).Files["report.md"]
	return ok
}

func hasAssembledResearchReportAfterImports(prefix CheckpointPrefix) bool {
	assembledAfterLatestImport := false
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name == "save_url_as_source" && action.Result != nil && action.Result.Status == ActionSucceeded {
				assembledAfterLatestImport = false
			}
			if action.Name == "assemble_research_report" && action.Result != nil && action.Result.Status == ActionSucceeded {
				assembledAfterLatestImport = true
			}
		}
	}
	return assembledAfterLatestImport && hasAssembledResearchReport(prefix)
}

func NewResearchWorkspaceActions(pool *pgxpool.Pool, store objectstore.Store) ([]Action, error) {
	if pool == nil || store == nil {
		return nil, errors.New("Research workspace requires PostgreSQL and object storage")
	}
	index := postgresResearchWorkspaceIndex{pool: pool}
	return []Action{
		newWriteResearchFileAction(store),
		newReadResearchFileAction(store, index, postgresResearchClaimBackend{pool: pool}),
		newListResearchFilesAction(index),
		&assembleResearchReportAction{store: store, index: index, barrier: postgresResearchSourceImportBarrier{pool: pool}, claims: postgresResearchClaimBackend{pool: pool}, sources: postgresResearchClaimBackend{pool: pool}, agents: postgresResearchClaimBackend{pool: pool}},
	}, nil
}
