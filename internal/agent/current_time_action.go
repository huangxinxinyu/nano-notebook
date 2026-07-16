package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type currentTimeAction struct {
	now func() time.Time
}

type currentTimeInput struct {
	TimeZone *string `json:"time_zone"`
}

func NewCurrentTimeAction(now func() time.Time) Action {
	if now == nil {
		now = time.Now
	}
	return currentTimeAction{now: now}
}

func (currentTimeAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "current_time",
		Description: "Read the current time in an optional IANA time zone.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"time_zone":{"type":"string"}},"additionalProperties":false}`),
	}
}

func (currentTimeAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeCurrentTimeInput(raw)
	return err
}

func (a currentTimeAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	input, err := decodeCurrentTimeInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	timeZone := request.DefaultTimeZone
	explicit := input.TimeZone != nil
	if explicit {
		timeZone = strings.TrimSpace(*input.TimeZone)
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil || timeZone == "" {
		if explicit {
			return ActionResult{Status: ActionDomainError, ErrorCode: "invalid_time_zone"}, nil
		}
		timeZone = "UTC"
		location = time.UTC
	}
	observedAt := a.now().UTC()
	localTime := observedAt.In(location)
	_, offset := localTime.Zone()
	output, err := json.Marshal(struct {
		ObservedAt       string `json:"observed_at"`
		TimeZone         string `json:"time_zone"`
		LocalTime        string `json:"local_time"`
		UTCOffsetSeconds int    `json:"utc_offset_seconds"`
	}{
		ObservedAt:       observedAt.Format(time.RFC3339Nano),
		TimeZone:         timeZone,
		LocalTime:        localTime.Format(time.RFC3339Nano),
		UTCOffsetSeconds: offset,
	})
	if err != nil {
		return ActionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

func decodeCurrentTimeInput(raw json.RawMessage) (currentTimeInput, error) {
	if len(raw) == 0 || len(raw) > 4*1024 {
		return currentTimeInput{}, errors.New("invalid current_time input")
	}
	var input currentTimeInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return currentTimeInput{}, errors.New("invalid current_time input")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return currentTimeInput{}, errors.New("invalid current_time input")
	}
	return input, nil
}
