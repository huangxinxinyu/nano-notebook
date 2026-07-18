package replay

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

const (
	ModelRequestAttachmentKey  = "replay.model_request.attachment_id"
	ModelDecisionAttachmentKey = "replay.model_decision.attachment_id"
	ActionInputAttachmentKey   = "replay.action_input.attachment_id"
	ActionResultAttachmentKey  = "replay.action_result.attachment_id"
)

type AttachmentReference struct {
	Class        Class
	AttachmentID string
}

func AttachmentReferences(attributes []agentobs.Attribute) ([]AttachmentReference, error) {
	references := make([]AttachmentReference, 0, 2)
	for _, attribute := range attributes {
		class, matched := attachmentClassForKey(attribute.Key)
		if !matched {
			continue
		}
		if attribute.Value.Kind != agentobs.ValueString {
			return nil, errors.New("Replay Attachment reference must be a string")
		}
		if _, err := uuid.Parse(attribute.Value.String); err != nil {
			return nil, fmt.Errorf("Replay Attachment reference is invalid: %w", err)
		}
		references = append(references, AttachmentReference{Class: class, AttachmentID: attribute.Value.String})
	}
	return references, nil
}

func attachmentClassForKey(key string) (Class, bool) {
	switch key {
	case ModelRequestAttachmentKey:
		return ClassModelRequest, true
	case ModelDecisionAttachmentKey:
		return ClassModelDecision, true
	case ActionInputAttachmentKey:
		return ClassActionInput, true
	case ActionResultAttachmentKey:
		return ClassActionResult, true
	default:
		return "", false
	}
}
