package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCurrentTimeActionUsesPinnedOrExplicitIANAZone(t *testing.T) {
	observed := time.Date(2026, 7, 16, 7, 30, 45, 123000000, time.UTC).In(time.FixedZone("host-zone", 9*60*60))
	action := NewCurrentTimeAction(func() time.Time { return observed })
	tests := []struct {
		name        string
		defaultZone string
		input       string
		wantZone    string
		wantLocal   string
		wantOffset  int
	}{
		{name: "pinned default", defaultZone: "Asia/Shanghai", input: `{}`, wantZone: "Asia/Shanghai", wantLocal: "2026-07-16T15:30:45.123+08:00", wantOffset: 8 * 60 * 60},
		{name: "explicit", defaultZone: "Asia/Shanghai", input: `{"time_zone":"America/New_York"}`, wantZone: "America/New_York", wantLocal: "2026-07-16T03:30:45.123-04:00", wantOffset: -4 * 60 * 60},
		{name: "invalid pin fallback", defaultZone: "Mars/Olympus", input: `{}`, wantZone: "UTC", wantLocal: "2026-07-16T07:30:45.123Z", wantOffset: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := action.Execute(context.Background(), ActionRequest{Input: json.RawMessage(tt.input), DefaultTimeZone: tt.defaultZone})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != ActionSucceeded || result.ErrorCode != "" {
				t.Fatalf("result = %+v", result)
			}
			var output struct {
				ObservedAt       string `json:"observed_at"`
				TimeZone         string `json:"time_zone"`
				LocalTime        string `json:"local_time"`
				UTCOffsetSeconds int    `json:"utc_offset_seconds"`
			}
			if err := json.Unmarshal(result.Output, &output); err != nil {
				t.Fatal(err)
			}
			if output.ObservedAt != "2026-07-16T07:30:45.123Z" || output.TimeZone != tt.wantZone || output.LocalTime != tt.wantLocal || output.UTCOffsetSeconds != tt.wantOffset {
				t.Fatalf("output = %+v", output)
			}
		})
	}
}

func TestCurrentTimeActionReturnsInvalidTimeZoneDomainError(t *testing.T) {
	action := NewCurrentTimeAction(func() time.Time { return time.Unix(0, 0) })
	result, err := action.Execute(context.Background(), ActionRequest{
		Input: json.RawMessage(`{"time_zone":"Mars/Olympus"}`), DefaultTimeZone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ActionDomainError || result.ErrorCode != "invalid_time_zone" || result.Output != nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestCurrentTimeActionRejectsMalformedInputAndCancellation(t *testing.T) {
	action := NewCurrentTimeAction(func() time.Time { return time.Unix(0, 0) })
	if _, err := action.Execute(context.Background(), ActionRequest{Input: json.RawMessage(`{"extra":true}`), DefaultTimeZone: "UTC"}); err == nil {
		t.Fatal("unknown input field error = nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := action.Execute(cancelled, ActionRequest{Input: json.RawMessage(`{}`), DefaultTimeZone: "UTC"}); err == nil {
		t.Fatal("cancelled execution error = nil")
	}
}
