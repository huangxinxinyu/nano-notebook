package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCalculateActionUsesDeterministicDecimalArithmetic(t *testing.T) {
	tests := []struct {
		operation string
		left      string
		right     string
		want      string
	}{
		{operation: "add", left: "0.1", right: "0.2", want: "0.3"},
		{operation: "subtract", left: "12.5", right: "3.2", want: "9.3"},
		{operation: "multiply", left: "1.25", right: "8", want: "10"},
		{operation: "divide", left: "1", right: "8", want: "0.125"},
		{operation: "divide", left: "1", right: "3", want: "0.333333333333333333"},
	}
	action := NewCalculateAction()
	for _, tt := range tests {
		t.Run(tt.operation+"/"+tt.left+"/"+tt.right, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"operation": tt.operation, "operands": []string{tt.left, tt.right}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := action.Execute(context.Background(), ActionRequest{Input: input})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != ActionSucceeded || result.ErrorCode != "" {
				t.Fatalf("result = %+v", result)
			}
			var output struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(result.Output, &output); err != nil {
				t.Fatal(err)
			}
			if output.Value != tt.want {
				t.Fatalf("value = %q, want %q", output.Value, tt.want)
			}
		})
	}
}

func TestCalculateActionReturnsTypedDomainErrors(t *testing.T) {
	tooLarge := strings.Repeat("9", 128)
	tests := []struct {
		name      string
		operation string
		operands  []string
		wantCode  string
	}{
		{name: "invalid decimal", operation: "add", operands: []string{"01", "2"}, wantCode: "invalid_decimal"},
		{name: "wrong count", operation: "add", operands: []string{"1"}, wantCode: "invalid_operand_count"},
		{name: "unsupported", operation: "power", operands: []string{"2", "3"}, wantCode: "unsupported_operation"},
		{name: "division by zero", operation: "divide", operands: []string{"1", "0"}, wantCode: "division_by_zero"},
		{name: "result too large", operation: "multiply", operands: []string{tooLarge, tooLarge}, wantCode: "calculation_result_too_large"},
	}
	action := NewCalculateAction()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"operation": tt.operation, "operands": tt.operands})
			if err != nil {
				t.Fatal(err)
			}
			result, err := action.Execute(context.Background(), ActionRequest{Input: input})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != ActionDomainError || result.ErrorCode != tt.wantCode || result.Output != nil {
				t.Fatalf("result = %+v, want domain error %q", result, tt.wantCode)
			}
		})
	}
}

func TestCalculateActionRejectsMalformedInputAndCancellation(t *testing.T) {
	action := NewCalculateAction()
	if _, err := action.Execute(context.Background(), ActionRequest{Input: json.RawMessage(`{"operation":"add","operands":["1","2"],"extra":true}`)}); err == nil {
		t.Fatal("unknown input field error = nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := action.Execute(cancelled, ActionRequest{Input: json.RawMessage(`{"operation":"add","operands":["1","2"]}`)}); err == nil {
		t.Fatal("cancelled execution error = nil")
	}
}
