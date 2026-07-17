package agentobs

import (
	"errors"
	"math"
	"regexp"
)

const (
	DefaultMaxAttributes           = 64
	DefaultMaxAttributeStringBytes = 4096
)

var attributeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

type ValueKind string

const (
	ValueString  ValueKind = "string"
	ValueInt64   ValueKind = "int64"
	ValueFloat64 ValueKind = "float64"
	ValueBool    ValueKind = "bool"
)

type AttributeValue struct {
	Kind    ValueKind
	String  string
	Int64   int64
	Float64 float64
	Bool    bool
}

type Attribute struct {
	Key   string
	Value AttributeValue
}

func String(key, value string) Attribute {
	return Attribute{Key: key, Value: AttributeValue{Kind: ValueString, String: value}}
}

func Int64(key string, value int64) Attribute {
	return Attribute{Key: key, Value: AttributeValue{Kind: ValueInt64, Int64: value}}
}

func Float64(key string, value float64) Attribute {
	return Attribute{Key: key, Value: AttributeValue{Kind: ValueFloat64, Float64: value}}
}

func Bool(key string, value bool) Attribute {
	return Attribute{Key: key, Value: AttributeValue{Kind: ValueBool, Bool: value}}
}

func validateAttributes(attributes []Attribute, limits Limits) error {
	if len(attributes) > limits.MaxAttributes {
		return errors.New("record has too many attributes")
	}
	seen := make(map[string]struct{}, len(attributes))
	for _, attribute := range attributes {
		if !attributeKeyPattern.MatchString(attribute.Key) {
			return errors.New("attribute key is invalid")
		}
		if _, duplicate := seen[attribute.Key]; duplicate {
			return errors.New("attribute key is duplicated")
		}
		seen[attribute.Key] = struct{}{}
		switch attribute.Value.Kind {
		case ValueString:
			if len([]byte(attribute.Value.String)) > limits.MaxAttributeStringBytes {
				return errors.New("attribute string is too large")
			}
		case ValueInt64, ValueBool:
		case ValueFloat64:
			if math.IsInf(attribute.Value.Float64, 0) || math.IsNaN(attribute.Value.Float64) {
				return errors.New("attribute float is not finite")
			}
		default:
			return errors.New("attribute type is invalid")
		}
	}
	return nil
}
