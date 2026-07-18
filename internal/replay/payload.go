package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const MaxPlaintextBytes = 2 * 1024 * 1024

var ErrPayloadTooLarge = errors.New("Replay payload exceeds plaintext limit")

type Class string

const (
	ClassModelRequest  Class = "model_request"
	ClassModelDecision Class = "model_decision"
	ClassActionInput   Class = "action_input"
	ClassActionResult  Class = "action_result"
)

type PlainPayload struct {
	Class         Class
	SchemaVersion int
	Bytes         []byte
	SHA256        string
}

func NewPlainPayload(class Class, schemaVersion int, encoded []byte) (PlainPayload, error) {
	if !class.Valid() || schemaVersion != 1 || len(encoded) == 0 {
		return PlainPayload{}, errors.New("Replay payload header is invalid")
	}
	if len(encoded) > MaxPlaintextBytes {
		return PlainPayload{}, ErrPayloadTooLarge
	}
	digest := sha256.Sum256(encoded)
	return PlainPayload{
		Class: class, SchemaVersion: schemaVersion,
		Bytes: append([]byte(nil), encoded...), SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func (c Class) Valid() bool {
	switch c {
	case ClassModelRequest, ClassModelDecision, ClassActionInput, ClassActionResult:
		return true
	default:
		return false
	}
}
