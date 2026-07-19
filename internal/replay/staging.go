package replay

import (
	"errors"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

var ErrIdentityConflict = errors.New("Replay attachment identity conflicts with staged payload")

type StagerConfig struct {
	ObjectPrefix string
	Retention    time.Duration
}

type StageRequest struct {
	TraceID     agentobs.TraceID
	IdentityKey string
	Payload     PlainPayload
}

type StagedAttachment struct {
	AttachmentID     string
	TraceID          agentobs.TraceID
	IdentityKey      string
	Class            Class
	SchemaVersion    int
	PlaintextSHA256  string
	ObjectKey        string
	CiphertextBytes  int
	CiphertextSHA256 string
	Compression      string
	Encryption       string
	KeyID            string
	WrappedKey       []byte
	Nonce            []byte
	ExpiresAt        time.Time
}

func validateStageRequest(request StageRequest) error {
	if strings.TrimSpace(string(request.TraceID)) == "" || len(request.TraceID) > 160 ||
		strings.TrimSpace(request.IdentityKey) == "" || len(request.IdentityKey) > 200 {
		return errors.New("Replay Stage request identity is invalid")
	}
	validated, err := NewPlainPayload(request.Payload.Class, request.Payload.SchemaVersion, request.Payload.Bytes)
	if err != nil {
		return err
	}
	if request.Payload.SHA256 != "" && request.Payload.SHA256 != validated.SHA256 {
		return ErrIntegrity
	}
	return nil
}

func reconcileStagedAttachment(staged StagedAttachment, payload PlainPayload) error {
	if staged.Class != payload.Class || staged.SchemaVersion != payload.SchemaVersion || staged.PlaintextSHA256 != payload.SHA256 {
		return ErrIdentityConflict
	}
	return nil
}
