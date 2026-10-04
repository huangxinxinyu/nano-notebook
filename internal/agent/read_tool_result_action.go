package agent

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

var toolResultReferencePattern = regexp.MustCompile(`^(?:tr_[A-Za-z0-9_-]{12,128}|run:[A-Za-z0-9_-]{1,160}/checkpoint:decision:[1-9][0-9]*/action:[0-9]+)$`)

type readToolResultAction struct {
	reader    ToolResultReader
	compacted *CompactedToolResultReader
}

type readToolResultInput struct {
	ResultRef string `json:"result_ref"`
	Offset    int    `json:"offset,omitempty"`
	MaxBytes  int    `json:"max_bytes,omitempty"`
}

func NewReadToolResultAction(reader ToolResultReader, compacted ...CompactedToolResultReader) Action {
	action := &readToolResultAction{reader: reader}
	if len(compacted) > 0 {
		action.compacted = &compacted[0]
	}
	return action
}

func (*readToolResultAction) CrashReplaySafe() bool { return true }

func (*readToolResultAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "read_tool_result",
		Description: "Read one bounded byte range from a recent externalized Tool Result or a compacted checkpoint result. Reads do not extend ephemeral expiry. When complete=false, call again at next_offset until complete=true.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["result_ref"],"properties":{"result_ref":{"type":"string","pattern":"^(?:tr_[A-Za-z0-9_-]{12,128}|run:[A-Za-z0-9_-]{1,160}/checkpoint:decision:[1-9][0-9]*/action:[0-9]+)$"},"offset":{"type":"integer","minimum":0},"max_bytes":{"type":"integer","minimum":1}}}`),
	}
}

func (*readToolResultAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeReadToolResultInput(raw)
	return err
}

func (a *readToolResultAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeReadToolResultInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	scope := ToolResultScope{
		UserID: request.UserID, ChatID: request.ChatID, RunID: request.Attempt.RunID,
	}
	var page ToolResultPage
	if compactedToolResultReferencePattern.MatchString(input.ResultRef) && a.compacted != nil {
		page, err = a.compacted.Read(ctx, scope, input.ResultRef, input.Offset, input.MaxBytes)
	} else {
		page, err = a.reader.Read(ctx, scope, input.ResultRef, input.Offset, input.MaxBytes)
	}
	if err != nil {
		code := "tool_result_read_failed"
		switch {
		case errors.Is(err, ErrToolResultExpired):
			code = "tool_result_expired"
		case errors.Is(err, ErrToolResultUnauthorized):
			code = "tool_result_unauthorized"
		case errors.Is(err, ErrToolResultCorrupt):
			code = "tool_result_corrupt"
		case errors.Is(err, ErrToolResultInvalidOffset), errors.Is(err, ErrToolResultInvalidPageSize):
			code = "tool_result_range_invalid"
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: code}, nil
	}
	output, err := marshalBoundedToolResultPage(page, request.ActionID, a.reader.MaximumOutputBytes)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

func marshalBoundedToolResultPage(page ToolResultPage, actionID string, maximumOutputBytes int) ([]byte, error) {
	encoded, err := json.Marshal(page)
	if err != nil || maximumOutputBytes <= 0 {
		return encoded, err
	}
	visibleBytes, err := modelVisibleToolResultBytes(actionID, encoded)
	if err != nil || visibleBytes <= maximumOutputBytes {
		return encoded, err
	}
	runes := []rune(page.Content)
	low, high := 0, len(runes)
	for low < high {
		middle := (low + high + 1) / 2
		candidate := page
		candidate.Content = string(runes[:middle])
		candidate.NextOffset = candidate.Offset + len([]byte(candidate.Content))
		candidate.Complete = page.Complete && middle == len(runes)
		candidate.Notice = toolResultPageNotice(candidate.ResultRef, candidate.Offset, candidate.NextOffset, candidate.ResultBytes, candidate.Complete)
		payload, marshalErr := json.Marshal(candidate)
		candidateVisibleBytes, sizeErr := modelVisibleToolResultBytes(actionID, payload)
		if marshalErr == nil && sizeErr == nil && candidateVisibleBytes <= maximumOutputBytes {
			low = middle
		} else {
			high = middle - 1
		}
	}
	page.Content = string(runes[:low])
	page.NextOffset = page.Offset + len([]byte(page.Content))
	page.Complete = page.Complete && low == len(runes)
	page.Notice = toolResultPageNotice(page.ResultRef, page.Offset, page.NextOffset, page.ResultBytes, page.Complete)
	encoded, err = json.Marshal(page)
	if err != nil {
		return nil, err
	}
	visibleBytes, sizeErr := modelVisibleToolResultBytes(actionID, encoded)
	if sizeErr != nil {
		return nil, sizeErr
	}
	if low == 0 || visibleBytes > maximumOutputBytes {
		return nil, ErrToolResultInvalidPageSize
	}
	return encoded, nil
}

func decodeReadToolResultInput(raw json.RawMessage) (readToolResultInput, error) {
	var input readToolResultInput
	if decodeExactJSON(raw, &input) != nil || !toolResultReferencePattern.MatchString(input.ResultRef) || input.Offset < 0 || input.MaxBytes < 0 {
		return readToolResultInput{}, errors.New("invalid read_tool_result input")
	}
	return input, nil
}
