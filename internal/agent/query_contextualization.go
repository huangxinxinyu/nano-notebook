package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const QueryContextSystemPrompt = `You create one retrieval query for the user's current request. The current Message is authoritative: preserve its key terms, named entities, qualifiers, units, and original language wherever meaningful. Recent completed conversation is reference-only: use it only to resolve pronouns, ellipsis, ambiguous shorthand, or omitted subjects in the current Message. Add only the minimum context needed to make the query standalone. Do not translate ambiguous terms or silently change what a limit, count, date, unit, or comparison refers to. When wording is ambiguous, copy it rather than choose an interpretation unless context explicitly supplies the missing dimension. Never replace a self-contained current topic with an older topic. Call search_evidence exactly once with a concise standalone query and a short purpose. Do not answer the user, summarize Sources, or call any other Action.`

const (
	queryContextPairLimit        = 3
	queryContextHistoryRuneLimit = 4000
	queryContextMessageRuneLimit = 1200
	queryContextCurrentRuneLimit = 4000
	searchQueryRuneLimit         = 2000
	composerCurrentRuneLimit     = 16000
)

type completedConversationPair struct {
	user      string
	assistant string
}

func (r *PostgresRuntime) BuildQueryContextRequest(
	ctx context.Context,
	execution Execution,
	definition models.ActionDefinition,
) (models.ModelRequest, models.ActionProposal, int, error) {
	if execution.PromptVersion != GroundedPromptVersion || execution.SelectedSourceCount < 1 {
		return models.ModelRequest{}, models.ActionProposal{}, 0, errors.New("query contextualization requires selected Sources")
	}
	if definition.Name != "search_evidence" {
		return models.ModelRequest{}, models.ActionProposal{}, 0, errors.New("query contextualization requires search_evidence")
	}
	current, pairs, err := r.loadCompletedConversation(ctx, execution)
	if err != nil {
		return models.ModelRequest{}, models.ActionProposal{}, 0, err
	}
	return buildQueryContextRequest(execution.Model, current, pairs, definition)
}

func (r *PostgresRuntime) loadCompletedConversation(ctx context.Context, execution Execution) (string, []completedConversationPair, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx)
	var current string
	if err := tx.QueryRow(ctx, `
		select content from chat_messages where id=$2 and chat_id=$1 and role='user'
	`, execution.ChatID, execution.InputMessageID).Scan(&current); err != nil {
		return "", nil, err
	}
	rows, err := tx.Query(ctx, `
		with cutoff as (
			select created_at,id from chat_messages where id=$2 and chat_id=$1
		)
		select input.content,output.content
		from agent_runs prior
		join chat_messages input on input.id=prior.input_message_id and input.chat_id=prior.chat_id and input.role='user'
		join chat_messages output on output.id=prior.output_message_id and output.chat_id=prior.chat_id and output.role='assistant'
		cross join cutoff
		where prior.chat_id=$1 and prior.status='completed'
			and (input.created_at,input.id)<(cutoff.created_at,cutoff.id)
		order by input.created_at desc,input.id desc
		limit $3
	`, execution.ChatID, execution.InputMessageID, queryContextPairLimit)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	pairsNewestFirst := make([]completedConversationPair, 0, queryContextPairLimit)
	for rows.Next() {
		var pair completedConversationPair
		if err := rows.Scan(&pair.user, &pair.assistant); err != nil {
			return "", nil, err
		}
		pairsNewestFirst = append(pairsNewestFirst, pair)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	pairs := make([]completedConversationPair, len(pairsNewestFirst))
	for index := range pairsNewestFirst {
		pairs[len(pairsNewestFirst)-1-index] = pairsNewestFirst[index]
	}
	return current, pairs, nil
}

func (r *PostgresRuntime) buildGroundedComposerRequest(ctx context.Context, execution Execution) (models.ModelRequest, error) {
	current, err := r.loadCurrentMessage(ctx, execution)
	if err != nil {
		return models.ModelRequest{}, err
	}
	systemPrompt := r.systemPrompt
	if systemPrompt == BareSystemPrompt {
		systemPrompt = GroundedSystemPrompt
	}
	messages := []models.ModelMessage{{Role: models.RoleSystem, Content: systemPrompt}}
	current = truncateRunes(strings.TrimSpace(current), composerCurrentRuneLimit)
	if current == "" {
		return models.ModelRequest{}, errors.New("current Message is invalid")
	}
	messages = append(messages, models.ModelMessage{Role: models.RoleUser, Content: current})
	return models.ModelRequest{Model: execution.Model, Messages: messages}, nil
}

func (r *PostgresRuntime) loadCurrentMessage(ctx context.Context, execution Execution) (string, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var current string
	if err := tx.QueryRow(ctx, `
		select content from chat_messages where id=$2 and chat_id=$1 and role='user'
	`, execution.ChatID, execution.InputMessageID).Scan(&current); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return current, nil
}

func buildQueryContextRequest(
	model string,
	current string,
	pairs []completedConversationPair,
	definition models.ActionDefinition,
) (models.ModelRequest, models.ActionProposal, int, error) {
	current = strings.TrimSpace(current)
	if current == "" || !utf8.ValidString(current) {
		return models.ModelRequest{}, models.ActionProposal{}, 0, errors.New("current Message is invalid")
	}
	fallbackInput, err := json.Marshal(searchEvidenceInput{
		Query:   truncateRunes(current, searchQueryRuneLimit),
		Purpose: "Answer the current user request using selected Sources.",
	})
	if err != nil {
		return models.ModelRequest{}, models.ActionProposal{}, 0, err
	}
	fallback := models.ActionProposal{Name: "search_evidence", Input: fallbackInput}

	boundedPairs := boundCompletedPairs(pairs)
	var prompt strings.Builder
	prompt.WriteString("RECENT COMPLETED CONTEXT (reference only):\n")
	if len(boundedPairs) == 0 {
		prompt.WriteString("(none)\n")
	} else {
		for index, pair := range boundedPairs {
			_, _ = fmt.Fprintf(&prompt, "Pair %d user: %s\nPair %d assistant: %s\n", index+1, pair.user, index+1, pair.assistant)
		}
	}
	prompt.WriteString("\nCURRENT MESSAGE (authoritative):\n")
	prompt.WriteString(truncateRunes(current, queryContextCurrentRuneLimit))

	request := models.ModelRequest{
		Model: model,
		Messages: []models.ModelMessage{
			{Role: models.RoleSystem, Content: QueryContextSystemPrompt},
			{Role: models.RoleUser, Content: prompt.String()},
		},
		ActionDefinitions:  cloneActionDefinitions([]models.ActionDefinition{definition}),
		RequiredActionName: "search_evidence",
	}
	return request, fallback, len(boundedPairs), nil
}

func preserveCurrentSearchQuery(proposal, fallback models.ActionProposal) (models.ActionProposal, error) {
	var current searchEvidenceInput
	if err := json.Unmarshal(fallback.Input, &current); err != nil {
		return models.ActionProposal{}, err
	}
	var contextualized searchEvidenceInput
	if err := json.Unmarshal(proposal.Input, &contextualized); err != nil {
		return models.ActionProposal{}, err
	}
	if !strings.Contains(contextualized.Query, current.Query) {
		contextualized.Query = truncateRunes(strings.TrimSpace(current.Query+" "+contextualized.Query), searchQueryRuneLimit)
	}
	input, err := json.Marshal(contextualized)
	if err != nil {
		return models.ActionProposal{}, err
	}
	proposal.Input = input
	return proposal, nil
}

func boundCompletedPairs(pairs []completedConversationPair) []completedConversationPair {
	return boundConversationPairs(pairs, queryContextPairLimit, queryContextHistoryRuneLimit, queryContextMessageRuneLimit)
}

func boundConversationPairs(pairs []completedConversationPair, pairLimit, totalRuneLimit, messageRuneLimit int) []completedConversationPair {
	if len(pairs) > pairLimit {
		pairs = pairs[len(pairs)-pairLimit:]
	}
	remaining := totalRuneLimit
	newestFirst := make([]completedConversationPair, 0, len(pairs))
	for index := len(pairs) - 1; index >= 0 && remaining > 0; index-- {
		user := truncateRunes(strings.TrimSpace(pairs[index].user), minInt(messageRuneLimit, remaining))
		remaining -= utf8.RuneCountInString(user)
		assistant := truncateRunes(strings.TrimSpace(pairs[index].assistant), minInt(messageRuneLimit, remaining))
		remaining -= utf8.RuneCountInString(assistant)
		if user == "" || assistant == "" {
			continue
		}
		newestFirst = append(newestFirst, completedConversationPair{user: user, assistant: assistant})
	}
	bounded := make([]completedConversationPair, len(newestFirst))
	for index := range newestFirst {
		bounded[len(newestFirst)-1-index] = newestFirst[index]
	}
	return bounded
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
