package agentobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type RecordPayload struct {
	SemanticConventionVersion int
	Status                    Status
	Attributes                []Attribute
}

func DecodeCanonicalPayload(encoded []byte) (RecordPayload, error) {
	return DecodeCanonicalPayloadWithLimits(encoded, DefaultLimits())
}

func DecodeCanonicalPayloadWithLimits(encoded []byte, limits Limits) (RecordPayload, error) {
	if err := limits.Validate(); err != nil {
		return RecordPayload{}, err
	}
	if len(encoded) > limits.MaxPayloadBytes {
		return RecordPayload{}, errors.New("record payload is too large")
	}
	var envelope struct {
		SemanticConventionVersion int                   `json:"semantic_convention_version"`
		Status                    Status                `json:"status,omitempty"`
		Attributes                *[]canonicalAttribute `json:"attributes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return RecordPayload{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RecordPayload{}, errors.New("record payload has trailing data")
	}
	if envelope.SemanticConventionVersion < 1 || envelope.Attributes == nil {
		return RecordPayload{}, errors.New("record payload envelope is incomplete")
	}
	if envelope.Status != "" && !envelope.Status.validTerminal() {
		return RecordPayload{}, errors.New("record payload status is invalid")
	}
	attributes := make([]Attribute, 0, len(*envelope.Attributes))
	for _, encodedAttribute := range *envelope.Attributes {
		value, err := decodeCanonicalAttribute(encodedAttribute)
		if err != nil {
			return RecordPayload{}, err
		}
		attributes = append(attributes, Attribute{Key: encodedAttribute.Key, Value: value})
	}
	if err := validateAttributes(attributes, limits); err != nil {
		return RecordPayload{}, err
	}
	return RecordPayload{
		SemanticConventionVersion: envelope.SemanticConventionVersion,
		Status:                    envelope.Status,
		Attributes:                attributes,
	}, nil
}

func decodeCanonicalAttribute(attribute canonicalAttribute) (AttributeValue, error) {
	populated := 0
	if attribute.String != nil {
		populated++
	}
	if attribute.Int64 != nil {
		populated++
	}
	if attribute.Float64 != nil {
		populated++
	}
	if attribute.Bool != nil {
		populated++
	}
	if populated != 1 {
		return AttributeValue{}, errors.New("canonical attribute value is ambiguous")
	}
	switch attribute.Kind {
	case ValueString:
		if attribute.String == nil {
			return AttributeValue{}, errors.New("canonical string attribute has wrong value field")
		}
		return AttributeValue{Kind: ValueString, String: *attribute.String}, nil
	case ValueInt64:
		if attribute.Int64 == nil {
			return AttributeValue{}, errors.New("canonical int64 attribute has wrong value field")
		}
		return AttributeValue{Kind: ValueInt64, Int64: *attribute.Int64}, nil
	case ValueFloat64:
		if attribute.Float64 == nil {
			return AttributeValue{}, errors.New("canonical float64 attribute has wrong value field")
		}
		return AttributeValue{Kind: ValueFloat64, Float64: *attribute.Float64}, nil
	case ValueBool:
		if attribute.Bool == nil {
			return AttributeValue{}, errors.New("canonical bool attribute has wrong value field")
		}
		return AttributeValue{Kind: ValueBool, Bool: *attribute.Bool}, nil
	default:
		return AttributeValue{}, errors.New("canonical attribute type is invalid")
	}
}
