package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// complete_research closes a Research Run with a per-question declaration that
// the runtime checks against the claim cards and the assembled report. From
// executor v42 a root Final needs an accepted declaration made after the last
// assembly, unless the Run has no Action left to make one. A question declared
// not_found without a single usable card is sent back for one more round of
// discovery; after that round, or once the budget is spent, not_found stands.
// The accepted declaration and the Run's card yield per round are stored with
// the published report version.

const (
	completeResearchActionName = "complete_research"
	researchCompletionVersion  = 42
	researchCompletionMaxNote  = 500
	researchCompletionMaxCards = 30
	// researchCompletionMaxListed bounds how many names one problem lists.
	researchCompletionMaxListed = 8
)

const (
	researchQuestionAnswered = "answered"
	researchQuestionPartial  = "partial"
	researchQuestionNotFound = "not_found"
)

const (
	researchStopAnswered   = "answered"
	researchStopPartial    = "partially_answered"
	researchStopBudget     = "budget"
	researchStopUndeclared = "undeclared"
)

// researchCompletionRetryActions count as the extra round a rejected not_found
// question asks for: anything that looks for or reads new evidence.
var researchCompletionRetryActions = map[string]bool{
	"web_search": true, "read_url": true, "read_document_pages": true, "read_source": true,
	"search_evidence": true, "search_text": true, "save_url_as_source": true, "spawn_agent": true,
}

// researchSaturationReadActions are the root's own reads in a round.
var researchSaturationReadActions = map[string]bool{
	"read_url": true, "read_document_pages": true, "read_source": true, "search_evidence": true, "search_text": true,
}

type completeResearchQuestion struct {
	Question int      `json:"question"`
	Status   string   `json:"status"`
	Cards    []string `json:"cards,omitempty"`
	Note     string   `json:"note,omitempty"`
}

type completeResearchInput struct {
	Questions []completeResearchQuestion `json:"questions"`
}

type completeResearchOutput struct {
	Accepted       bool                       `json:"accepted"`
	StopReason     string                     `json:"stop_reason,omitempty"`
	Questions      []completeResearchQuestion `json:"questions,omitempty"`
	Problems       []string                   `json:"problems,omitempty"`
	RetryQuestions []int                      `json:"retry_questions,omitempty"`
	Guidance       string                     `json:"guidance"`
}

type researchCompletionBackend interface {
	ClaimTree(ctx context.Context, runID string) (researchClaimTree, error)
	ResearchPlanQuestions(ctx context.Context, runID string) ([]string, error)
	RunningResearchSubagents(ctx context.Context, runID string) ([]researchRunningSubagent, error)
	ResearchBudgetExhausted(ctx context.Context, runID string) (bool, error)
}

// ResearchBudgetExhausted reports whether the Run's tree has spent its token,
// model-call, or Action budget.
func (b postgresResearchClaimBackend) ResearchBudgetExhausted(ctx context.Context, runID string) (bool, error) {
	if b.pool == nil {
		return false, errors.New("Research claim store is unavailable")
	}
	return modelBudgetExhaustedForRun(ctx, b.pool, runID)
}

type completeResearchAction struct {
	store   objectstore.Store
	backend researchCompletionBackend
}

func newCompleteResearchAction(store objectstore.Store, pool *pgxpool.Pool) Action {
	return &completeResearchAction{store: store, backend: postgresResearchClaimBackend{pool: pool}}
}

func (*completeResearchAction) CrashReplaySafe() bool { return true }

// Available keeps the declaration to the root: a subagent's Final is a
// finding for the root, not the end of the Research.
func (a *completeResearchAction) Available(execution Execution) (bool, string) {
	if execution.ParentRunID != "" {
		return false, "research_completion_root_only"
	}
	return a != nil && a.store != nil && a.backend != nil, "research_workspace_unavailable"
}

func (*completeResearchAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        completeResearchActionName,
		Description: "Declare the Research finished after the reviewed report is assembled. List every research question of the accepted plan once with its status: answered (the report answers it), partial (the report answers part of it), or not_found (the evidence was not found; say so in the report). answered and partial name the claim cards behind the answer; each must be verified or near_match and cited in report.md. The runtime checks the declaration and returns accepted or the problems to fix; return Final only after it is accepted.",
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["questions"],"properties":{"questions":{"type":"array","maxItems":%d,"items":{"type":"object","additionalProperties":false,"required":["question","status"],"properties":{"question":{"type":"integer","minimum":1,"maximum":%d},"status":{"type":"string","enum":["answered","partial","not_found"]},"cards":{"type":"array","maxItems":%d,"uniqueItems":true,"items":{"type":"string","minLength":2,"maxLength":32}},"note":{"type":"string","maxLength":%d}}}}}}`,
			claimRecordMaxQuestion, claimRecordMaxQuestion, researchCompletionMaxCards, researchCompletionMaxNote)),
	}
}

func (*completeResearchAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeCompleteResearchInput(raw)
	return err
}

func (a *completeResearchAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeCompleteResearchInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.store == nil || a.backend == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_unavailable"}, nil
	}
	runID := request.Attempt.RunID
	tree, err := a.backend.ClaimTree(ctx, runID)
	if err != nil {
		return ActionResult{}, err
	}
	root, ok := tree.run(runID)
	if !ok {
		return ActionResult{}, errors.New("Research claim tree does not contain the declaring Run")
	}
	if !hasAssembledResearchReportAfterImports(root.Prefix) {
		return completeResearchResult(completeResearchOutput{
			Problems: []string{"report.md has not been assembled since the last source import: assemble the reviewed sections first."},
		})
	}
	report, _, err := loadAssembledResearchReport(ctx, a.store, root.Prefix)
	if err != nil {
		if ctx.Err() != nil {
			return ActionResult{}, ctx.Err()
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_workspace_read_failed"}, nil
	}
	questions, err := a.backend.ResearchPlanQuestions(ctx, runID)
	if err != nil {
		return ActionResult{}, err
	}
	running, err := a.backend.RunningResearchSubagents(ctx, runID)
	if err != nil {
		return ActionResult{}, err
	}
	exhausted, err := a.backend.ResearchBudgetExhausted(ctx, runID)
	if err != nil {
		return ActionResult{}, err
	}
	return completeResearchResult(checkResearchCompletion(researchCompletionCheck{
		Input: input, Report: report, Claims: collectResearchClaims(tree), PlanQuestions: len(questions),
		Running: running, BudgetExhausted: exhausted, Retried: researchCompletionRetriedQuestions(root.Prefix),
	}))
}

func completeResearchResult(output completeResearchOutput) (ActionResult, error) {
	if !output.Accepted {
		output.Guidance = "Completion rejected. Fix each problem, then call complete_research again. Do not return Final until a declaration is accepted."
	}
	payload, err := json.Marshal(output)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, nil
}

// decodeCompleteResearchInput names the exact problem, because the controller
// feeds validation failures back to the model for a retry.
func decodeCompleteResearchInput(raw json.RawMessage) (completeResearchInput, error) {
	var input completeResearchInput
	if err := decodeExactJSON(raw, &input); err != nil {
		return completeResearchInput{}, fmt.Errorf("invalid complete_research input: %v; pass only questions", err)
	}
	if input.Questions == nil || len(input.Questions) > claimRecordMaxQuestion {
		return completeResearchInput{}, fmt.Errorf("invalid complete_research input: questions must list at most %d entries", claimRecordMaxQuestion)
	}
	for index := range input.Questions {
		question := &input.Questions[index]
		question.Note = strings.TrimSpace(question.Note)
		switch {
		case question.Question < 1 || question.Question > claimRecordMaxQuestion:
			return completeResearchInput{}, fmt.Errorf("invalid complete_research input: question must be a research question number from 1 to %d", claimRecordMaxQuestion)
		case question.Status != researchQuestionAnswered && question.Status != researchQuestionPartial && question.Status != researchQuestionNotFound:
			return completeResearchInput{}, fmt.Errorf("invalid complete_research input: Q%d status must be answered, partial, or not_found", question.Question)
		case len(question.Cards) > researchCompletionMaxCards:
			return completeResearchInput{}, fmt.Errorf("invalid complete_research input: Q%d lists more than %d cards", question.Question, researchCompletionMaxCards)
		case utf8.RuneCountInString(question.Note) > researchCompletionMaxNote:
			return completeResearchInput{}, fmt.Errorf("invalid complete_research input: Q%d note is longer than %d characters", question.Question, researchCompletionMaxNote)
		}
		for cardIndex, card := range question.Cards {
			question.Cards[cardIndex] = canonicalClaimID(strings.TrimSpace(strings.Trim(card, "[]")))
		}
	}
	return input, nil
}

type researchCompletionCheck struct {
	Input  completeResearchInput
	Report string
	Claims []researchClaim
	// PlanQuestions is how many research questions the accepted plan lists.
	PlanQuestions   int
	Running         []researchRunningSubagent
	BudgetExhausted bool
	// Retried holds the questions whose extra round has already run.
	Retried map[int]bool
}

func checkResearchCompletion(check researchCompletionCheck) completeResearchOutput {
	problems := make([]string, 0)
	if len(check.Running) > 0 {
		names := make([]string, 0, len(check.Running))
		for _, agent := range check.Running {
			names = append(names, firstNonEmpty(agent.TaskName, agent.AgentID))
		}
		problems = append(problems, fmt.Sprintf("Subagents still running: %s. Collect them with wait_agent and use their cards before completing.", listResearchCompletionNames(names)))
	}
	declared := map[int]bool{}
	for _, question := range check.Input.Questions {
		switch {
		case declared[question.Question]:
			problems = append(problems, fmt.Sprintf("Q%d is declared more than once.", question.Question))
		case check.PlanQuestions > 0 && question.Question > check.PlanQuestions:
			problems = append(problems, fmt.Sprintf("Q%d is not in the accepted plan, which lists %d research questions.", question.Question, check.PlanQuestions))
		}
		declared[question.Question] = true
	}
	missing := make([]string, 0)
	for question := 1; question <= check.PlanQuestions; question++ {
		if !declared[question] {
			missing = append(missing, fmt.Sprintf("Q%d", question))
		}
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("Missing from the declaration: %s. Declare every research question of the accepted plan.", listResearchCompletionNames(missing)))
	}

	claims := make(map[string]researchClaim, len(check.Claims))
	tagged := map[int]int{}
	for _, claim := range check.Claims {
		claims[claim.ID] = claim
		if usableResearchClaim(claim) && claim.Question > 0 {
			tagged[claim.Question]++
		}
	}
	cited := citedResearchClaimIDs(check.Report, check.Claims)
	retry := make([]int, 0)
	for _, question := range check.Input.Questions {
		if question.Status != researchQuestionNotFound && len(question.Cards) == 0 {
			problems = append(problems, fmt.Sprintf("Q%d is %s but names no claim card: list the cards behind its answer, or declare it not_found.", question.Question, question.Status))
		}
		var unknown, unusable, uncited []string
		for _, id := range question.Cards {
			claim, ok := claims[id]
			switch {
			case !ok:
				unknown = append(unknown, id)
			case !usableResearchClaim(claim):
				unusable = append(unusable, id+" ("+claim.Status+")")
			case !cited[id]:
				uncited = append(uncited, id)
			}
		}
		if len(unknown) > 0 {
			problems = append(problems, fmt.Sprintf("Q%d names cards that do not exist: %s.", question.Question, listResearchCompletionNames(unknown)))
		}
		if len(unusable) > 0 {
			problems = append(problems, fmt.Sprintf("Q%d names cards that failed verification: %s. Drop them, or record verified cards for those findings and cite those instead.", question.Question, listResearchCompletionNames(unusable)))
		}
		if len(uncited) > 0 {
			problems = append(problems, fmt.Sprintf("Q%d names cards the report never cites: %s. Cite them where the report answers Q%d, or drop them from the declaration.", question.Question, listResearchCompletionNames(uncited), question.Question))
		}
		if question.Status == researchQuestionNotFound && tagged[question.Question] == 0 && !check.BudgetExhausted && !check.Retried[question.Question] {
			retry = append(retry, question.Question)
		}
	}
	if len(retry) > 0 {
		labels := make([]string, 0, len(retry))
		for _, question := range retry {
			labels = append(labels, fmt.Sprintf("Q%d", question))
		}
		problems = append(problems, fmt.Sprintf("%s declared not_found without a single usable card for that question. Run one more targeted round for each: new queries, other source families, and the unread leads, recording cards tagged with the question. Then call complete_research again; if that round still finds nothing, not_found is accepted.", listResearchCompletionNames(labels)))
	}
	if len(problems) > 0 {
		return completeResearchOutput{Problems: problems, RetryQuestions: retry}
	}
	questions := append([]completeResearchQuestion(nil), check.Input.Questions...)
	sort.Slice(questions, func(i, j int) bool { return questions[i].Question < questions[j].Question })
	stop := researchStopAnswered
	for _, question := range questions {
		if question.Status != researchQuestionAnswered {
			stop = researchStopPartial
		}
	}
	if check.BudgetExhausted {
		stop = researchStopBudget
	}
	return completeResearchOutput{
		Accepted: true, StopReason: stop, Questions: questions,
		Guidance: "Completion accepted. Return Final now as the completion signal; do not repeat the report.",
	}
}

func usableResearchClaim(claim researchClaim) bool {
	return claim.Status == claimStatusVerified || claim.Status == claimStatusNearMatch
}

func listResearchCompletionNames(names []string) string {
	if len(names) <= researchCompletionMaxListed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:researchCompletionMaxListed], ", "), len(names)-researchCompletionMaxListed)
}

// researchCompletionRetriedQuestions returns the questions a rejected
// declaration sent back for another round that has since run: a later
// decision that searched for or read new evidence.
func researchCompletionRetriedQuestions(prefix CheckpointPrefix) map[int]bool {
	pending, done := map[int]bool{}, map[int]bool{}
	for _, proposal := range prefix.Proposals {
		searched := false
		for _, action := range proposal.Actions {
			if researchCompletionRetryActions[action.Name] && action.Result != nil && action.Result.Status == ActionSucceeded {
				searched = true
			}
		}
		if searched {
			for question := range pending {
				done[question] = true
			}
			pending = map[int]bool{}
		}
		for _, action := range proposal.Actions {
			if output, ok := decodeCompleteResearchOutput(action); ok {
				for _, question := range output.RetryQuestions {
					if !done[question] {
						pending[question] = true
					}
				}
			}
		}
	}
	return done
}

func decodeCompleteResearchOutput(action AcceptedAction) (completeResearchOutput, bool) {
	if action.Name != completeResearchActionName || action.Result == nil || action.Result.Status != ActionSucceeded {
		return completeResearchOutput{}, false
	}
	var output completeResearchOutput
	if json.Unmarshal(action.Result.Output, &output) != nil {
		return completeResearchOutput{}, false
	}
	return output, true
}

// acceptedResearchCompletion returns the accepted declaration made after the
// last successful assembly, so an edit and reassembly after declaring needs a
// new declaration.
func acceptedResearchCompletion(prefix CheckpointPrefix) (completeResearchOutput, bool) {
	var latest completeResearchOutput
	found := false
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name == "assemble_research_report" && action.Result != nil && action.Result.Status == ActionSucceeded {
				latest, found = completeResearchOutput{}, false
			}
			if output, ok := decodeCompleteResearchOutput(action); ok && output.Accepted {
				latest, found = output, true
			}
		}
	}
	return latest, found
}

func isResearchCompletionExecution(execution Execution) bool {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	return err == nil && reference.Identity == "research.executor" && reference.Version >= researchCompletionVersion
}

// researchCompletionCallable mirrors the Controller's Action boundary: once
// it closes, the Run can no longer declare and Final stands without one.
func researchCompletionCallable(execution Execution, prefix CheckpointPrefix) bool {
	return execution.ActionLimit-acceptedBusinessActions(prefix) > 0 && acceptedBusinessDecisions(prefix) < execution.ActionDecisionLimit
}

type researchCompletionRecord struct {
	StopReason           string                     `json:"stop_reason"`
	Questions            []completeResearchQuestion `json:"questions,omitempty"`
	RejectedDeclarations int                        `json:"rejected_declarations"`
	Saturation           researchSaturation         `json:"saturation"`
}

func buildResearchCompletionRecord(tree researchClaimTree, rootRunID string) researchCompletionRecord {
	record := researchCompletionRecord{StopReason: researchStopUndeclared}
	root, ok := tree.run(rootRunID)
	if !ok {
		return record
	}
	for _, proposal := range root.Prefix.Proposals {
		for _, action := range proposal.Actions {
			if output, ok := decodeCompleteResearchOutput(action); ok && !output.Accepted {
				record.RejectedDeclarations++
			}
		}
	}
	if accepted, ok := acceptedResearchCompletion(root.Prefix); ok {
		record.StopReason, record.Questions = accepted.StopReason, accepted.Questions
	}
	record.Saturation = measureResearchSaturation(tree, rootRunID)
	return record
}

// researchSaturationRound is one root decision that read or collected
// evidence, with the usable cards it added: the root's own cards from that
// decision and the cards of the subagents it collected as completed.
type researchSaturationRound struct {
	Decision         int `json:"decision"`
	Reads            int `json:"reads"`
	ReadersCollected int `json:"readers_collected"`
	NewCards         int `json:"new_cards"`
}

type researchSaturation struct {
	Rounds []researchSaturationRound `json:"rounds"`
	// UncollectedChildCards are usable cards of subagents the root never
	// collected as completed, so no round counts them.
	UncollectedChildCards int `json:"uncollected_child_cards"`
}

// measureResearchSaturation records the card yield per round, so a later
// stopping rule can be set from the observed distribution instead of a guess.
func measureResearchSaturation(tree researchClaimTree, rootRunID string) researchSaturation {
	saturation := researchSaturation{Rounds: make([]researchSaturationRound, 0)}
	root, ok := tree.run(rootRunID)
	if !ok {
		return saturation
	}
	childCards := map[string]int{}
	for _, run := range tree.Runs {
		if run.RunID != rootRunID {
			childCards[run.RunID] = countUsableResearchCards(run.Prefix.Proposals)
		}
	}
	collected := map[string]bool{}
	for _, proposal := range root.Prefix.Proposals {
		round := researchSaturationRound{Decision: proposal.DecisionNo}
		round.NewCards = countUsableResearchCards([]AcceptedProposal{proposal})
		for _, action := range proposal.Actions {
			if action.Result == nil || action.Result.Status != ActionSucceeded {
				continue
			}
			if researchSaturationReadActions[action.Name] {
				round.Reads++
			}
			if action.Name != "wait_agent" {
				continue
			}
			var output struct {
				Agents []runtimeSubagentObservation `json:"agents"`
			}
			if json.Unmarshal(action.Result.Output, &output) != nil {
				continue
			}
			for _, agent := range output.Agents {
				if agent.Status != "completed" || collected[agent.AgentID] {
					continue
				}
				if cards, ok := childCards[agent.AgentID]; ok {
					collected[agent.AgentID] = true
					round.ReadersCollected++
					round.NewCards += cards
				}
			}
		}
		if round.Reads > 0 || round.ReadersCollected > 0 || round.NewCards > 0 {
			saturation.Rounds = append(saturation.Rounds, round)
		}
	}
	for runID, cards := range childCards {
		if !collected[runID] {
			saturation.UncollectedChildCards += cards
		}
	}
	return saturation
}

func countUsableResearchCards(proposals []AcceptedProposal) int {
	count := 0
	for _, proposal := range proposals {
		for _, action := range proposal.Actions {
			if action.Name != recordClaimActionName || action.Result == nil || action.Result.Status != ActionSucceeded {
				continue
			}
			var output recordClaimOutput
			if json.Unmarshal(action.Result.Output, &output) == nil &&
				(output.Status == claimStatusVerified || output.Status == claimStatusNearMatch) {
				count++
			}
		}
	}
	return count
}
