package agent

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var compactedToolResultReferencePattern = regexp.MustCompile(`^run:([A-Za-z0-9_-]{1,160})/checkpoint:(decision:[1-9][0-9]*/action:[0-9]+)$`)

type CheckpointResultStore interface {
	GetActionResult(context.Context, string, string) (ActionResult, error)
}

type PostgresCheckpointResultStore struct {
	Pool *pgxpool.Pool
}

func (s PostgresCheckpointResultStore) GetActionResult(ctx context.Context, runID, actionID string) (ActionResult, error) {
	if s.Pool == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(actionID) == "" {
		return ActionResult{}, ErrToolResultExpired
	}
	checkpoint, err := scanCheckpoint(s.Pool.QueryRow(ctx, `
		select `+selectCheckpointColumns+`
		from agent_run_checkpoints
		where run_id=$1 and action_id=$2 and kind='action_result'
	`, runID, actionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ActionResult{}, ErrToolResultExpired
	}
	if errors.Is(err, ErrCheckpointInvalid) {
		return ActionResult{}, ErrToolResultCorrupt
	}
	if err != nil {
		return ActionResult{}, err
	}
	if checkpoint.ActionIndex == nil {
		return ActionResult{}, ErrToolResultCorrupt
	}
	var payload actionResultCheckpointPayload
	if decodeCheckpointPayload(checkpoint.Payload, &payload) != nil {
		return ActionResult{}, ErrToolResultCorrupt
	}
	result := ActionResult{Status: payload.Status, Output: payload.Output, ErrorCode: payload.ErrorCode, Error: payload.Error}
	expected, err := NewActionResultCheckpoint(checkpoint.DecisionNo, *checkpoint.ActionIndex, checkpoint.ActionID, result)
	if err != nil || !checkpointMatches(checkpoint, expected) {
		return ActionResult{}, ErrToolResultCorrupt
	}
	if result.Output != nil {
		result.Output = append(json.RawMessage(nil), result.Output...)
	}
	return result, nil
}

type CompactedToolResultReader struct {
	Store            CheckpointResultStore
	Externalized     ToolResultReader
	MaximumPageBytes int
}

func (r CompactedToolResultReader) Read(ctx context.Context, scope ToolResultScope, resultRef string, offset, maxBytes int) (ToolResultPage, error) {
	runID, actionID, ok := parseCompactedToolResultReference(resultRef)
	if !ok || r.Store == nil {
		return ToolResultPage{}, ErrToolResultExpired
	}
	if runID != scope.RunID {
		return ToolResultPage{}, ErrToolResultUnauthorized
	}
	result, err := r.Store.GetActionResult(ctx, runID, actionID)
	if err != nil {
		return ToolResultPage{}, err
	}
	if result.Status != ActionSucceeded {
		return ToolResultPage{}, ErrToolResultExpired
	}
	var projection ToolResultProjection
	if json.Unmarshal(result.Output, &projection) == nil && projection.ContentState == ToolResultExternalized && projection.ResultRef != "" {
		pageScope := scope
		pageScope.ActionID = actionID
		page, readErr := r.Externalized.Read(ctx, pageScope, projection.ResultRef, offset, maxBytes)
		if readErr != nil {
			return ToolResultPage{}, readErr
		}
		page.ResultRef = resultRef
		page.Notice = toolResultPageNotice(resultRef, page.Offset, page.NextOffset, page.ResultBytes, page.Complete)
		return page, nil
	}
	return pageCompactedToolResult(resultRef, result.Output, offset, maxBytes, r.MaximumPageBytes)
}

func parseCompactedToolResultReference(resultRef string) (string, string, bool) {
	matches := compactedToolResultReferencePattern.FindStringSubmatch(resultRef)
	if len(matches) != 3 {
		return "", "", false
	}
	return matches[1], matches[2], true
}

func pageCompactedToolResult(resultRef string, body []byte, offset, requestedBytes, maximumBytes int) (ToolResultPage, error) {
	pageLimit := requestedBytes
	if pageLimit <= 0 || pageLimit > maximumBytes {
		pageLimit = maximumBytes
	}
	if pageLimit < utf8.UTFMax || maximumBytes < utf8.UTFMax {
		return ToolResultPage{}, ErrToolResultInvalidPageSize
	}
	if offset < 0 || offset > len(body) || !utf8.Valid(body[offset:]) {
		return ToolResultPage{}, ErrToolResultInvalidOffset
	}
	end := offset + pageLimit
	if end > len(body) {
		end = len(body)
	}
	for end > offset && !utf8.Valid(body[offset:end]) {
		end--
	}
	if end == offset && offset < len(body) {
		return ToolResultPage{}, ErrToolResultInvalidPageSize
	}
	page := ToolResultPage{
		ResultRef: resultRef, Offset: offset, Content: string(body[offset:end]), NextOffset: end,
		Complete: end == len(body), ResultBytes: len(body),
	}
	page.Notice = toolResultPageNotice(resultRef, page.Offset, page.NextOffset, page.ResultBytes, page.Complete)
	return page, nil
}

var _ CheckpointResultStore = PostgresCheckpointResultStore{}
