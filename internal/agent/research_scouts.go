package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
)

// From executor v29 a root Research Run starts with open discovery that does
// not depend on the model choosing to search widely: before its first
// decision the runtime dispatches scout subagents, one per plan research
// question (questions share scouts beyond researchScoutMax).
// Scouts only search and return candidate sources; the root reads them.

const (
	researchScoutVersion    = 29
	researchScoutTaskPrefix = "Scout open discovery for the parent researcher."
	researchScoutActionID   = "scout:"
	// researchScoutMax leaves at least one of the four concurrent slots to
	// readers, so long documents the root finds early are read right away.
	researchScoutMax = 3
)

// RunStarter lets a runtime start work owned by a fresh Attempt before its
// first decision. It must be idempotent: every Attempt of the Run calls it.
type RunStarter interface {
	StartRun(ctx context.Context, attempt Attempt, execution Execution) error
}

func isResearchScoutExecution(execution Execution) bool {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	return err == nil && execution.ParentRunID == "" && reference.Identity == "research.executor" && reference.Version >= researchScoutVersion
}

func (r *ResearchRuntime) StartRun(ctx context.Context, attempt Attempt, execution Execution) error {
	if !isResearchScoutExecution(execution) {
		return nil
	}
	traceCtx, scope, err := r.base.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	defer scope.Rollback()
	tx, err := r.base.workerTx(traceCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(traceCtx)
	if err := lockCheckpointAuthority(traceCtx, tx, attempt); err != nil {
		return err
	}
	var started bool
	if err := tx.QueryRow(traceCtx, `select exists(select 1 from agent_subagents where parent_run_id=$1 and action_id like 'scout:%')`, attempt.RunID).Scan(&started); err != nil {
		return err
	}
	if started {
		return nil
	}
	questions, err := loadResearchPlanQuestions(traceCtx, tx, attempt.RunID)
	if err != nil {
		return err
	}
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	if err != nil {
		return err
	}
	request := ActionRequest{Attempt: attempt, UserID: execution.UserID, ChatID: execution.ChatID, Definition: reference}
	for index, group := range researchScoutGroups(questions, researchScoutMax) {
		input := researchScoutSpawnInput(group, reference.Version)
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if _, err := createRuntimeSubagentInTx(traceCtx, tx, request, fmt.Sprintf("%s%d", researchScoutActionID, index+1), input, len(raw)); err != nil {
			return err
		}
	}
	if err := tx.Commit(traceCtx); err != nil {
		return err
	}
	publishCommittedTrace(traceCtx, scope)
	return nil
}

type researchScoutQuestion struct {
	Number int
	Text   string
}

// researchScoutGroups assigns plan questions to at most limit scouts, round
// robin, so each scout keeps a coherent share of the plan.
func researchScoutGroups(questions []string, limit int) [][]researchScoutQuestion {
	var numbered []researchScoutQuestion
	for index, question := range questions {
		if text := strings.TrimSpace(question); text != "" {
			numbered = append(numbered, researchScoutQuestion{Number: index + 1, Text: text})
		}
	}
	if len(numbered) == 0 || limit <= 0 {
		return nil
	}
	count := min(len(numbered), limit)
	groups := make([][]researchScoutQuestion, count)
	for index, question := range numbered {
		groups[index%count] = append(groups[index%count], question)
	}
	return groups
}

// From executor v32 a scout batches its searches and skips the TODO tools,
// which cost one slow model decision per update.
func researchScoutSpawnInput(questions []researchScoutQuestion, definitionVersion int) spawnAgentInput {
	labels := make([]string, len(questions))
	var lines strings.Builder
	for index, question := range questions {
		labels[index] = fmt.Sprintf("Q%d", question.Number)
		fmt.Fprintf(&lines, "- Q%d: %s\n", question.Number, question.Text)
	}
	message := researchScoutTaskPrefix + " Find candidate sources for these research questions of the accepted plan:\n" + lines.String() + "\n" +
		"Run several rounds of web_search with varied queries: restate each question in plain terms without method names, use synonyms and adjacent terms, and add angles such as survey, benchmark, evaluation, replication, limitations, critique, and the most recent year. " +
		"Do not limit yourself to sources the plan names; they came from a shallow scout. Search snippets are leads, not evidence: do not read full documents, record claim cards, or state findings. " +
		"Return Final with, for each question, 5 to 10 candidate sources, most valuable first: URL, title, kind (paper, documentation, benchmark, evaluation, critique, or other), and one line on why it matters for the question. Flag independent evaluations and critiques, and say which questions found little."
	if definitionVersion >= researchRecommendedLeadsVersion {
		message += " Propose up to three web_search calls together in each decision and do not use the TODO tools; your Final's candidates become the parent's recommended reading list, so rank them carefully."
	}
	taskName := "Scout: " + strings.Join(labels, ", ")
	if runes := []rune(taskName); len(runes) > 80 {
		taskName = string(runes[:80])
	}
	return spawnAgentInput{Message: message, TaskName: taskName}
}

func loadResearchPlanQuestions(ctx context.Context, tx DBTX, runID string) ([]string, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `
		select coalesce(plan.plan_json->'research_questions','[]'::jsonb)
		from research_sessions session
		join research_plan_versions plan on plan.session_id=session.id and plan.version=session.accepted_plan_version
		where session.execution_run_id=nano_research_root_run($1)
	`, runID).Scan(&raw); err != nil {
		return nil, err
	}
	var questions []string
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, nil
	}
	return questions, nil
}

// researchScoutsPrompt tells the root which scouts the runtime started. It
// lists only stable facts so the system prompt stays cacheable.
func (r *ResearchRuntime) researchScoutsPrompt(ctx context.Context, execution Execution) (string, error) {
	if !isResearchScoutExecution(execution) {
		return "", nil
	}
	rows, err := r.pool.Query(ctx, `select child_run_id,task_name from agent_subagents
		where parent_run_id=$1 and action_id like 'scout:%' order by action_id`, execution.RunID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var builder strings.Builder
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return "", err
		}
		fmt.Fprintf(&builder, "\n- %s: %s", name, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if builder.Len() == 0 {
		return "", nil
	}
	return "Scouts: the runtime started one open-discovery scout per research question before your first decision. Each returns candidate sources for its questions; it does not read them. Collect them with wait_agent, read the strongest candidates (long documents go to readers), and keep searching where a scout found little:" + builder.String(), nil
}
