package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type calculateAction struct{}

type calculateInput struct {
	Operation string   `json:"operation"`
	Operands  []string `json:"operands"`
}

var canonicalDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func NewCalculateAction() Action {
	return calculateAction{}
}

func (calculateAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "calculate",
		Description: "Perform one bounded decimal arithmetic operation on exactly two operands.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["add","subtract","multiply","divide"]},"operands":{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":2}},"required":["operation","operands"],"additionalProperties":false}`),
	}
}

func (calculateAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeCalculateInput(raw)
	return err
}

func (calculateAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	input, err := decodeCalculateInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if len(input.Operands) != 2 {
		return calculateDomainError("invalid_operand_count"), nil
	}
	left, ok := parseCanonicalDecimal(input.Operands[0])
	if !ok {
		return calculateDomainError("invalid_decimal"), nil
	}
	right, ok := parseCanonicalDecimal(input.Operands[1])
	if !ok {
		return calculateDomainError("invalid_decimal"), nil
	}
	value := new(big.Rat)
	switch input.Operation {
	case "add":
		value.Add(left, right)
	case "subtract":
		value.Sub(left, right)
	case "multiply":
		value.Mul(left, right)
	case "divide":
		if right.Sign() == 0 {
			return calculateDomainError("division_by_zero"), nil
		}
		value.Quo(left, right)
	default:
		return calculateDomainError("unsupported_operation"), nil
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	canonical := canonicalRat(value)
	if len(canonical) > 128 {
		return calculateDomainError("calculation_result_too_large"), nil
	}
	output, err := json.Marshal(struct {
		Value string `json:"value"`
	}{Value: canonical})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

func decodeCalculateInput(raw json.RawMessage) (calculateInput, error) {
	if len(raw) == 0 || len(raw) > 4*1024 {
		return calculateInput{}, errors.New("invalid calculate input")
	}
	var input calculateInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return calculateInput{}, errors.New("invalid calculate input")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return calculateInput{}, errors.New("invalid calculate input")
	}
	return input, nil
}

func calculateDomainError(code string) ActionResult {
	return ActionResult{Status: ActionDomainError, ErrorCode: code}
}

func parseCanonicalDecimal(value string) (*big.Rat, bool) {
	if len(value) == 0 || len(value) > 128 || !canonicalDecimalPattern.MatchString(value) {
		return nil, false
	}
	parsed, ok := new(big.Rat).SetString(value)
	if !ok || (parsed.Sign() == 0 && strings.HasPrefix(value, "-")) {
		return nil, false
	}
	return parsed, true
}

func canonicalRat(value *big.Rat) string {
	denominator := new(big.Int).Abs(new(big.Int).Set(value.Denom()))
	two, five := big.NewInt(2), big.NewInt(5)
	remainder := new(big.Int)
	twos, fives := 0, 0
	for {
		quotient := new(big.Int)
		quotient.QuoRem(denominator, two, remainder)
		if remainder.Sign() != 0 {
			break
		}
		denominator = quotient
		twos++
	}
	for {
		quotient := new(big.Int)
		quotient.QuoRem(denominator, five, remainder)
		if remainder.Sign() != 0 {
			break
		}
		denominator = quotient
		fives++
	}
	precision := 18
	if denominator.Cmp(big.NewInt(1)) == 0 {
		precision = max(twos, fives)
	}
	result := value.FloatString(precision)
	if strings.Contains(result, ".") {
		result = strings.TrimRight(strings.TrimRight(result, "0"), ".")
	}
	if result == "-0" || result == "" {
		return "0"
	}
	return result
}
