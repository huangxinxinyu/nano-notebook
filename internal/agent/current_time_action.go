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

func (a currentTimeAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	var input struct {
		TimeZone *string `json:"time_zone"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return ActionResult{}, errors.New("invalid current_time input")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ActionResult{}, errors.New("invalid current_time input")
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
