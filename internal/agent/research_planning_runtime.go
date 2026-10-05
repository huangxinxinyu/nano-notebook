package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/skillcatalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ResearchPlanningRuntime struct {
	base    *PostgresRuntime
	pool    *pgxpool.Pool
	prompts promptcatalog.Catalog
	skills  skillcatalog.Catalog
}

func (*ResearchPlanningRuntime) InvalidModelResponseRetryLimit() int { return 2 }

func NewResearchPlanningRuntime(pool *pgxpool.Pool, prompts promptcatalog.Catalog, skills skillcatalog.Catalog) (*ResearchPlanningRuntime, error) {
	if pool == nil {
		return nil, errors.New("Research Planning Runtime requires PostgreSQL")
	}
	if _, ok := prompts.Resolve("agent.deep-research-planner", 1); !ok {
		return nil, errors.New("Research planner Prompt is missing")
	}
	return &ResearchPlanningRuntime{base: NewPostgresRuntime(pool, "", nil), pool: pool, prompts: prompts, skills: skills}, nil
}

func (r *ResearchPlanningRuntime) Load(ctx context.Context, attempt Attempt) (Execution, error) {
	return r.base.Load(ctx, attempt)
}

func (r *ResearchPlanningRuntime) LoadCheckpointPrefix(ctx context.Context, attempt Attempt) (CheckpointPrefix, error) {
	return r.base.LoadCheckpointPrefix(ctx, attempt)
}

func (r *ResearchPlanningRuntime) CheckAuthority(ctx context.Context, attempt Attempt) error {
	return r.base.CheckAuthority(ctx, attempt)
}

func (r *ResearchPlanningRuntime) AppendCheckpoint(ctx context.Context, attempt Attempt, checkpoint PendingCheckpoint) (Checkpoint, error) {
	return r.base.AppendCheckpoint(ctx, attempt, checkpoint)
}

func (r *ResearchPlanningRuntime) BuildDecisionRequest(ctx context.Context, execution Execution, prefix CheckpointPrefix, definitions []models.ActionDefinition) (models.ModelRequest, error) {
	if prefix.Final != nil {
		return models.ModelRequest{}, errors.New("Research Plan Final Draft does not require another decision")
	}
	var requestText, promptReference string
	var skillAllowlist []byte
	err := r.pool.QueryRow(ctx, `
		select message.content,definition.prompt_bindings->>'planner',definition.skill_allowlist
		from research_sessions session
		join chat_messages message on message.id=session.input_message_id
		join agent_runs run on run.id=session.planning_run_id
		join agent_definition_versions definition
		  on definition.definition_identity=run.definition_identity and definition.definition_version=run.definition_version
		where session.planning_run_id=$1 and session.status='planning'
	`, execution.RunID).Scan(&requestText, &promptReference, &skillAllowlist)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.ModelRequest{}, ErrLeaseLost
	}
	if err != nil {
		return models.ModelRequest{}, err
	}
	promptIdentity, promptVersion, err := parseVersionedIdentity(promptReference)
	if err != nil {
		return models.ModelRequest{}, err
	}
	prompt, ok := r.prompts.Resolve(promptIdentity, promptVersion)
	if !ok {
		return models.ModelRequest{}, fmt.Errorf("Research planner Prompt %s is unavailable", promptReference)
	}
	var skillReferences []string
	if err := json.Unmarshal(skillAllowlist, &skillReferences); err != nil {
		return models.ModelRequest{}, err
	}
	sort.Strings(skillReferences)
	system := strings.TrimSpace(prompt.Content) + "\n\nAvailable Skill summaries (full instructions require read_skill):"
	for _, reference := range skillReferences {
		identity, version, parseErr := parseVersionedIdentity(reference)
		if parseErr != nil {
			return models.ModelRequest{}, parseErr
		}
		skill, exists := r.skills.Resolve(identity, version)
		if !exists {
			return models.ModelRequest{}, fmt.Errorf("Research planner Skill %s is unavailable", reference)
		}
		system += fmt.Sprintf("\n- %s: %s — %s", reference, skill.Name, skill.Description)
	}
	system += "\n\n" + researchPlanningContext(time.Now(), execution.TimeZone)
	turns, err := r.planningLaneTurns(ctx, execution, prefix, requestText)
	if err != nil {
		return models.ModelRequest{}, err
	}
	lane, err := ProjectChatLane(ctx, ChatLane{Turns: turns}, nil)
	if err != nil {
		return models.ModelRequest{}, err
	}
	request := buildProjectedRequest(execution, system, lane, definitions)
	request.InvocationPolicy = execution.ModelInvocation
	return request, nil
}

// planningLaneTurns projects the whole planning conversation: the original
// request, then each plan revision turn with the plan version it revises.
// Every earlier planning Run contributes its full checkpointed transcript.
func (r *ResearchPlanningRuntime) planningLaneTurns(ctx context.Context, execution Execution, prefix CheckpointPrefix, requestText string) ([]ChatLaneTurn, error) {
	rows, err := r.pool.Query(ctx, `
		select turn.turn_no,turn.run_id,turn.input_message_id,message.content,coalesce(plan.version,0),coalesce(plan.plan_json::text,'')
		from research_planning_turns turn
		join research_sessions session on session.id=turn.session_id
		join chat_messages message on message.id=turn.input_message_id
		left join research_plan_versions plan on plan.session_id=turn.session_id and plan.version=turn.base_plan_version
		where session.planning_run_id=$1
		order by turn.turn_no
	`, execution.RunID)
	if err != nil {
		return nil, err
	}
	type storedTurn struct {
		turnNo           int
		runID, messageID string
		content          string
		planVersion      int
		planJSON         string
	}
	stored := make([]storedTurn, 0)
	for rows.Next() {
		var turn storedTurn
		if err := rows.Scan(&turn.turnNo, &turn.runID, &turn.messageID, &turn.content, &turn.planVersion, &turn.planJSON); err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, turn)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	current := ChatLaneRun{RunID: execution.RunID, Prefix: &prefix}
	if len(stored) == 0 {
		return []ChatLaneTurn{{MessageID: execution.InputMessageID, Content: requestText, Runs: []ChatLaneRun{current}}}, nil
	}
	turns := make([]ChatLaneTurn, 0, len(stored))
	for _, turn := range stored {
		run := current
		if turn.runID != execution.RunID {
			checkpoints, err := loadRunCheckpoints(ctx, r.pool, turn.runID)
			if err != nil {
				return nil, err
			}
			run = ChatLaneRun{RunID: turn.runID, Checkpoints: checkpoints}
		}
		turns = append(turns, ChatLaneTurn{MessageID: turn.messageID, Content: planningTurnContent(turn.turnNo, turn.content, turn.planVersion, turn.planJSON), Runs: []ChatLaneRun{run}})
	}
	return turns, nil
}

func planningTurnContent(turnNo int, message string, planVersion int, planJSON string) string {
	if turnNo == 0 {
		return message
	}
	if planJSON == "" {
		return "The Member asks to revise the proposed Research Plan:\n" + message
	}
	return fmt.Sprintf("The Member asks to revise Research Plan version %d. The current plan, including any edits the Member made directly, is:\n%s\n\nRevision request:\n%s\n\nRevise the plan. Ask with request_user_input only if the request leaves a consequential decision open; otherwise return the complete revised plan.", planVersion, planJSON, message)
}

func researchPlanningContext(now time.Time, timeZone string) string {
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		location = time.UTC
		timeZone = "UTC"
	}
	return fmt.Sprintf("Operational planning context: the current date is %s in %s. This is supplied to prevent stale or fabricated dates; it does not create a date cutoff unless the Member requested one.", now.In(location).Format("2006-01-02"), timeZone)
}

func (*ResearchPlanningRuntime) PrepareDecisionResponse(_ context.Context, _ Execution, _ CheckpointPrefix, decision models.ModelDecision) (models.ModelDecision, error) {
	if decision.Final == nil {
		return decision, nil
	}
	plan, err := ValidateResearchPlanJSON(decision.Final.Text)
	if err != nil {
		return models.ModelDecision{}, err
	}
	prepared := decision
	final := *decision.Final
	final.Text = string(plan)
	prepared.Final = &final
	return prepared, nil
}

func (r *ResearchPlanningRuntime) PublishFinal(ctx context.Context, attempt Attempt, draft models.FinalDraft) error {
	plan, err := ValidateResearchPlanJSON(draft.Text)
	if err != nil {
		return err
	}
	traceCtx, traceScope, err := r.base.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	defer traceScope.Rollback()
	tx, err := r.base.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return err
	}
	checkpoints, err := loadRunCheckpoints(ctx, tx, attempt.RunID)
	if err != nil {
		return err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return err
	}
	storedHash, storedErr := finalDraftSHA256(valueOrEmptyFinal(prefix.Final))
	expectedHash, expectedErr := finalDraftSHA256(draft)
	if prefix.Final == nil || storedErr != nil || expectedErr != nil || storedHash != expectedHash {
		return invalidCheckpoint("Research Plan publication does not match accepted Final Draft")
	}
	var sessionID string
	if err := tx.QueryRow(ctx, `
		select id from research_sessions
		where planning_run_id=$1 and status='planning'
		for update
	`, attempt.RunID).Scan(&sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	if err := storeConfiguredFinalResult(ctx, tx, attempt.RunID, draft.Text); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into research_plan_versions(session_id,version,plan_json,producer_run_id,created_by)
		values($1,(select coalesce(max(version),0)+1 from research_plan_versions where session_id=$1),$2::jsonb,$3,'model')
	`, sessionID, string(plan), attempt.RunID); err != nil {
		return err
	}
	if tag, err := tx.Exec(ctx, `
		update research_sessions set status='awaiting_confirmation',updated_at=now()
		where id=$1 and status='planning'
	`, sessionID); err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return ErrLeaseLost
	}
	if tag, err := tx.Exec(ctx, `
		update agent_runs set status='completed',finished_at=now(),updated_at=now()
		where id=$1 and status='running' and output_message_id is null
	`, attempt.RunID); err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return ErrLeaseLost
	}
	if tag, err := tx.Exec(ctx, `
		update agent_jobs set status='succeeded',lease_token=null,lease_expires_at=null,finished_at=now(),updated_at=now()
		where id=$1 and run_id=$2 and status='running' and lease_token=$3::uuid
	`, attempt.JobID, attempt.RunID, attempt.LeaseToken); err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return ErrLeaseLost
	}
	if err := RecordRunTerminalInTx(traceCtx, tx, attempt.RunID, RunTerminalTrace{
		RunStatus: "completed", SpanStatus: agentobs.StatusOK, AttemptNo: attempt.AttemptNo,
	}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs',$1)`, attempt.RunID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	publishCommittedTrace(traceCtx, traceScope)
	return nil
}

func (r *ResearchPlanningRuntime) Fail(ctx context.Context, attempt Attempt, errorCode string) error {
	if err := r.base.Fail(ctx, attempt, errorCode); err != nil {
		return err
	}
	tx, err := r.base.workerTx(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(context.WithoutCancel(ctx), `
		update research_sessions set status='failed',error_code=$2,updated_at=now()
		where planning_run_id=$1 and status='planning'
	`, attempt.RunID, errorCode); err != nil {
		return err
	}
	return tx.Commit(context.WithoutCancel(ctx))
}

func parseVersionedIdentity(reference string) (string, int, error) {
	parsed, err := agentcatalog.ParseReference(reference)
	if err != nil {
		return "", 0, err
	}
	return parsed.Identity, parsed.Version, nil
}

func ValidateResearchPlanJSON(value string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(value)))
	decoder.DisallowUnknownFields()
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("Research Plan must be one JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("Research Plan contains trailing content")
	}
	wantStrings := []string{"title", "objective"}
	wantLists := []string{"research_questions", "investigation_tracks", "source_strategy", "analysis_method", "deliverable_outline", "completion_criteria", "clarifying_questions"}
	if len(object) != len(wantStrings)+len(wantLists)+1 {
		return nil, errors.New("Research Plan has unknown or missing fields")
	}
	for _, key := range wantStrings {
		var text string
		if json.Unmarshal(object[key], &text) != nil || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("Research Plan %s is invalid", key)
		}
	}
	var scope string
	if err := json.Unmarshal(object["scope"], &scope); err != nil {
		var scopeItems []string
		if listErr := json.Unmarshal(object["scope"], &scopeItems); listErr != nil || len(scopeItems) == 0 {
			return nil, errors.New("Research Plan scope is invalid")
		}
		for _, item := range scopeItems {
			if strings.TrimSpace(item) == "" {
				return nil, errors.New("Research Plan scope contains an empty item")
			}
		}
		scope = strings.Join(scopeItems, "\n")
		object["scope"], _ = json.Marshal(scope)
	}
	if strings.TrimSpace(scope) == "" {
		return nil, errors.New("Research Plan scope is invalid")
	}
	for _, key := range wantLists {
		var values []string
		if json.Unmarshal(object[key], &values) != nil || (key != "clarifying_questions" && len(values) == 0) {
			return nil, fmt.Errorf("Research Plan %s is invalid", key)
		}
		for _, item := range values {
			if strings.TrimSpace(item) == "" {
				return nil, fmt.Errorf("Research Plan %s contains an empty item", key)
			}
		}
	}
	canonical, err := json.Marshal(object)
	return canonical, err
}
