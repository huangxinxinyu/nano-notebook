package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// request_user_input lets the Research planner ask the Member multiple-choice
// questions. The Action suspends its planning Run, like the Source import
// barrier: no result is checkpointed, the Job waits, and the answered replay
// returns the Member's answers as this Action's result.

const (
	requestUserInputActionName = "request_user_input"
	planningQuestionMax        = 3
	planningOptionMin          = 2
	planningOptionMax          = 4
	planningAnswerTextMax      = 1000
)

var (
	planningQuestionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// ErrPlanningQuestionClosed reports an answer for a question that is no
	// longer open, or whose planning Run can no longer resume.
	ErrPlanningQuestionClosed = errors.New("Research planning question is not open")
	ErrPlanningAnswerInvalid  = errors.New("Research planning answer is invalid")
)

type planningQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type planningQuestion struct {
	ID          string                   `json:"id"`
	Header      string                   `json:"header,omitempty"`
	Question    string                   `json:"question"`
	Options     []planningQuestionOption `json:"options"`
	Recommended int                      `json:"recommended"`
}

type requestUserInputInput struct {
	Questions []planningQuestion `json:"questions"`
}

// PlanningAnswer is one Member answer: the chosen option label, or free text
// when the Member wrote their own answer instead of choosing.
type PlanningAnswer struct {
	ID          string `json:"id"`
	Choice      string `json:"choice,omitempty"`
	Text        string `json:"text,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type requestUserInputOutput struct {
	Answers []PlanningAnswer `json:"answers"`
	Note    string           `json:"note"`
}

type requestUserInputAction struct {
	pool *pgxpool.Pool
}

func NewRequestUserInputAction(pool *pgxpool.Pool) Action {
	return &requestUserInputAction{pool: pool}
}

func (*requestUserInputAction) CrashReplaySafe() bool { return true }

func (a *requestUserInputAction) Available(Execution) (bool, string) {
	return a != nil && a.pool != nil, "planning_questions_unavailable"
}

func (*requestUserInputAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        requestUserInputActionName,
		Description: "Ask the Member 1-3 multiple-choice questions and wait for their answers. Each question offers 2-4 mutually exclusive options and marks one recommended option by index; the Member may instead write their own answer. Use only for decisions that materially change the plan and cannot be found by searching.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["questions"],"properties":{"questions":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"object","additionalProperties":false,"required":["id","question","options","recommended"],"properties":{"id":{"type":"string","pattern":"^[a-z][a-z0-9_]{0,31}$"},"header":{"type":"string","maxLength":24,"description":"Short chip label, such as Scope or Audience."},"question":{"type":"string","minLength":1,"maxLength":500},"options":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"object","additionalProperties":false,"required":["label"],"properties":{"label":{"type":"string","minLength":1,"maxLength":160,"description":"A short choice, ideally under 60 characters; put any explanation in description."},"description":{"type":"string","maxLength":300}}}},"recommended":{"type":"integer","minimum":0,"maximum":3,"description":"Index of the recommended option."}}}}}}`),
	}
}

func (*requestUserInputAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeRequestUserInput(raw)
	return err
}

// decodeRequestUserInput names the exact problem in its error, because the
// controller feeds validation failures back to the model for a retry. A
// header that is merely too long is truncated rather than rejected.
func decodeRequestUserInput(raw json.RawMessage) (requestUserInputInput, error) {
	var input requestUserInputInput
	if err := decodeExactJSON(raw, &input); err != nil {
		return requestUserInputInput{}, fmt.Errorf("invalid request_user_input input: %v", err)
	}
	if len(input.Questions) < 1 || len(input.Questions) > planningQuestionMax {
		return requestUserInputInput{}, fmt.Errorf("invalid request_user_input input: ask 1 to %d questions, got %d", planningQuestionMax, len(input.Questions))
	}
	seen := map[string]bool{}
	for index := range input.Questions {
		question := &input.Questions[index]
		invalid := func(format string, args ...any) (requestUserInputInput, error) {
			return requestUserInputInput{}, fmt.Errorf("invalid request_user_input input: question %d: %s", index+1, fmt.Sprintf(format, args...))
		}
		question.Question = strings.TrimSpace(question.Question)
		question.Header = strings.TrimSpace(question.Header)
		if runes := []rune(question.Header); len(runes) > 24 {
			question.Header = string(runes[:24])
		}
		switch {
		case !planningQuestionIDPattern.MatchString(question.ID):
			return invalid("id %q must match ^[a-z][a-z0-9_]{0,31}$", question.ID)
		case seen[question.ID]:
			return invalid("id %q is repeated", question.ID)
		case question.Question == "" || utf8.RuneCountInString(question.Question) > 500:
			return invalid("question text must be 1 to 500 characters")
		case len(question.Options) < planningOptionMin || len(question.Options) > planningOptionMax:
			return invalid("offer %d to %d options, got %d", planningOptionMin, planningOptionMax, len(question.Options))
		case question.Recommended < 0 || question.Recommended >= len(question.Options):
			return invalid("recommended must be an option index from 0 to %d", len(question.Options)-1)
		}
		seen[question.ID] = true
		labels := map[string]bool{}
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			option.Label = strings.TrimSpace(option.Label)
			option.Description = strings.TrimSpace(option.Description)
			switch {
			case option.Label == "" || utf8.RuneCountInString(option.Label) > 160:
				return invalid("option %d label must be 1 to 160 characters; move explanation into description", optionIndex+1)
			case labels[option.Label]:
				return invalid("option label %q is repeated", option.Label)
			case utf8.RuneCountInString(option.Description) > 300:
				return invalid("option %d description exceeds 300 characters", optionIndex+1)
			}
			labels[option.Label] = true
		}
	}
	return input, nil
}

func (a *requestUserInputAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeRequestUserInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.pool == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "planning_questions_unavailable"}, nil
	}
	traceCtx := ctx
	var traceScope *TraceScope
	if _, ok := TraceScopeFromContext(ctx); !ok {
		traceScope, err = NewTraceScope(DiscardTraceSink{})
		if err != nil {
			return ActionResult{}, err
		}
		traceCtx = ContextWithTraceScope(ctx, traceScope)
		defer traceScope.Rollback()
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return ActionResult{}, err
	}
	if err := lockCheckpointAuthority(ctx, tx, request.Attempt); err != nil {
		return ActionResult{}, err
	}
	var answers []byte
	err = tx.QueryRow(ctx, `select answers from research_planning_questions where run_id=$1 and action_id=$2`, request.Attempt.RunID, request.ActionID).Scan(&answers)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ActionResult{}, err
	}
	if len(answers) > 0 {
		var output requestUserInputOutput
		if err := json.Unmarshal(answers, &output.Answers); err != nil {
			return ActionResult{}, err
		}
		output.Note = "The Member answered. An answer marked recommended means they accepted your recommended option without choosing; record it as an assumption in the plan."
		payload, err := json.Marshal(output)
		if err != nil {
			return ActionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Status: ActionSucceeded, Output: payload}, nil
	}
	var sessionID string
	if err := tx.QueryRow(ctx, `
		select id from research_sessions
		where planning_run_id=$1 and status in ('planning','awaiting_input')
		for update
	`, request.Attempt.RunID).Scan(&sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ActionResult{Status: ActionDomainError, ErrorCode: "planning_questions_unavailable"}, nil
		}
		return ActionResult{}, err
	}
	questions, err := json.Marshal(input.Questions)
	if err != nil {
		return ActionResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into research_planning_questions(run_id,action_id,session_id,questions)
		values($1,$2,$3,$4::jsonb) on conflict(run_id,action_id) do nothing
	`, request.Attempt.RunID, request.ActionID, sessionID, string(questions)); err != nil {
		return ActionResult{}, err
	}
	if _, err := tx.Exec(ctx, `update research_sessions set status='awaiting_input',updated_at=now() where id=$1`, sessionID); err != nil {
		return ActionResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		update agent_jobs
		set status='waiting',lease_token=null,lease_expires_at=null,updated_at=now()
		where id=$1 and run_id=$2 and status='running' and lease_token=$3::uuid and attempt_no=$4
	`, request.Attempt.JobID, request.Attempt.RunID, request.Attempt.LeaseToken, request.Attempt.AttemptNo)
	if err != nil {
		return ActionResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return ActionResult{}, ErrLeaseLost
	}
	if err := RecordAttemptWaitingInTx(traceCtx, tx, request.Attempt.RunID, request.Attempt.JobID, request.Attempt.AttemptNo); err != nil {
		return ActionResult{}, err
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs',$1)`, request.Attempt.RunID); err != nil {
		return ActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ActionResult{}, err
	}
	if traceScope != nil {
		_ = traceScope.PublishAfterCommit(traceCtx)
	}
	return ActionResult{}, ErrLeaseLost
}

// PendingPlanningQuestions is the open question batch of a session, if any.
type PendingPlanningQuestions struct {
	ActionID  string          `json:"action_id"`
	Questions json.RawMessage `json:"questions"`
}

func LoadPendingPlanningQuestions(ctx context.Context, tx DBTX, sessionID string) (*PendingPlanningQuestions, error) {
	var pending PendingPlanningQuestions
	err := tx.QueryRow(ctx, `
		select action_id,questions from research_planning_questions
		where session_id=$1 and answered_at is null order by created_at desc limit 1
	`, sessionID).Scan(&pending.ActionID, &pending.Questions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pending, nil
}

// AnswerPlanningQuestionsInTx records the Member's answers and requeues the
// waiting planning Run, whose replayed request_user_input returns them. With
// useRecommended, every unanswered question takes its recommended option.
func AnswerPlanningQuestionsInTx(ctx context.Context, tx pgx.Tx, sessionID, actionID string, answers []PlanningAnswer, useRecommended bool) error {
	var runID string
	var raw []byte
	err := tx.QueryRow(ctx, `
		select question.run_id,question.questions
		from research_planning_questions question
		join research_sessions session on session.id=question.session_id
		where question.session_id=$1 and question.action_id=$2 and question.answered_at is null
		  and session.status='awaiting_input' and session.planning_run_id=question.run_id
		for update of question, session
	`, sessionID, actionID).Scan(&runID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPlanningQuestionClosed
	}
	if err != nil {
		return err
	}
	var questions []planningQuestion
	if err := json.Unmarshal(raw, &questions); err != nil {
		return err
	}
	normalized, err := normalizePlanningAnswers(questions, answers, useRecommended)
	if err != nil {
		return err
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs set status='queued',updated_at=now()
		where id=$1 and status='running' and coalesce(
			deadline_at,(select absolute_deadline from agent_trees where id=agent_runs.tree_id)
		)>now() and exists(select 1 from agent_jobs where run_id=$1 and status='waiting')
	`, runID)
	if err != nil {
		return err
	}
	if runTag.RowsAffected() != 1 {
		return ErrPlanningQuestionClosed
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update research_planning_questions set answers=$3::jsonb,answered_at=now() where run_id=$1 and action_id=$2`, runID, actionID, string(payload)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update research_sessions set status='planning',updated_at=now() where id=$1 and status='awaiting_input'`, sessionID); err != nil {
		return err
	}
	jobTag, err := tx.Exec(ctx, `update agent_jobs set status='queued',available_at=now(),updated_at=now() where run_id=$1 and status='waiting'`, runID)
	if err != nil {
		return err
	}
	if jobTag.RowsAffected() != 1 {
		return errors.New("Research planning waiter lost its Agent Job")
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_jobs',id) from agent_jobs where run_id=$1 and status='queued'`, runID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `select pg_notify('nano_agent_runs',$1)`, runID)
	return err
}

func normalizePlanningAnswers(questions []planningQuestion, answers []PlanningAnswer, useRecommended bool) ([]PlanningAnswer, error) {
	byID := make(map[string]PlanningAnswer, len(answers))
	for _, answer := range answers {
		if _, duplicate := byID[answer.ID]; duplicate {
			return nil, ErrPlanningAnswerInvalid
		}
		byID[answer.ID] = answer
	}
	normalized := make([]PlanningAnswer, 0, len(questions))
	for _, question := range questions {
		answer, ok := byID[question.ID]
		delete(byID, question.ID)
		answer.ID = question.ID
		answer.Choice = strings.TrimSpace(answer.Choice)
		answer.Text = strings.TrimSpace(answer.Text)
		answer.Recommended = false
		if !ok || (answer.Choice == "" && answer.Text == "") {
			if !useRecommended {
				return nil, ErrPlanningAnswerInvalid
			}
			answer.Choice, answer.Text, answer.Recommended = question.Options[question.Recommended].Label, "", true
		}
		if answer.Choice != "" && answer.Text != "" {
			return nil, ErrPlanningAnswerInvalid
		}
		if answer.Choice != "" && !planningQuestionHasOption(question, answer.Choice) {
			return nil, ErrPlanningAnswerInvalid
		}
		if utf8.RuneCountInString(answer.Text) > planningAnswerTextMax {
			return nil, ErrPlanningAnswerInvalid
		}
		normalized = append(normalized, answer)
	}
	if len(byID) > 0 {
		return nil, ErrPlanningAnswerInvalid
	}
	return normalized, nil
}

func planningQuestionHasOption(question planningQuestion, label string) bool {
	for _, option := range question.Options {
		if option.Label == label {
			return true
		}
	}
	return false
}
