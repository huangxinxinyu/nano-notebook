package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

const maxReplayManifestBytes = 32 * 1024

type ObjectStager struct {
	sealer       *Sealer
	objects      objectstore.Store
	objectPrefix string
	retention    time.Duration

	mu          sync.Mutex
	attachments map[string]StagedAttachment
}

type objectStagerManifest struct {
	TraceID     string           `json:"trace_id"`
	IdentityKey string           `json:"identity_key"`
	Attachment  StagedAttachment `json:"attachment"`
}

func NewObjectStager(sealer *Sealer, objects objectstore.Store, config StagerConfig) (*ObjectStager, error) {
	if sealer == nil || objects == nil {
		return nil, errors.New("Replay object Stager dependencies are incomplete")
	}
	config.ObjectPrefix = strings.Trim(strings.TrimSpace(config.ObjectPrefix), "/")
	if config.ObjectPrefix == "" {
		config.ObjectPrefix = "agent-replay-staging"
	}
	if len(config.ObjectPrefix) > 200 {
		return nil, errors.New("Replay staging object prefix is too long")
	}
	if config.Retention == 0 {
		config.Retention = 7 * 24 * time.Hour
	}
	if config.Retention <= 0 {
		return nil, errors.New("Replay retention must be positive")
	}
	return &ObjectStager{
		sealer: sealer, objects: objects, objectPrefix: config.ObjectPrefix,
		retention: config.Retention, attachments: make(map[string]StagedAttachment),
	}, nil
}

func (s *ObjectStager) Stage(ctx context.Context, request StageRequest) (StagedAttachment, error) {
	if s == nil || s.sealer == nil || s.objects == nil {
		return StagedAttachment{}, errors.New("nil Replay object Stager")
	}
	if err := validateStageRequest(request); err != nil {
		return StagedAttachment{}, err
	}
	attachmentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(request.TraceID)+"\x00"+request.IdentityKey)).String()
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.attachments[attachmentID]; ok {
		if err := reconcileStagedAttachment(existing, request.Payload); err != nil {
			return StagedAttachment{}, err
		}
		return cloneStagedAttachment(existing), nil
	}
	tracePrefix := StagingTracePrefix(s.objectPrefix, request.TraceID)
	manifestKey := tracePrefix + "/manifests/" + attachmentID + ".json"
	encodedManifest, err := s.objects.Get(ctx, manifestKey, maxReplayManifestBytes)
	if err == nil {
		var manifest objectStagerManifest
		if err := json.Unmarshal(encodedManifest, &manifest); err != nil {
			return StagedAttachment{}, fmt.Errorf("decode Replay staging manifest: %w", err)
		}
		if manifest.TraceID != string(request.TraceID) || manifest.IdentityKey != request.IdentityKey {
			return StagedAttachment{}, ErrIdentityConflict
		}
		if err := reconcileStagedAttachment(manifest.Attachment, request.Payload); err != nil {
			return StagedAttachment{}, err
		}
		s.attachments[attachmentID] = cloneStagedAttachment(manifest.Attachment)
		return cloneStagedAttachment(manifest.Attachment), nil
	}
	if !errors.Is(err, objectstore.ErrNotFound) {
		return StagedAttachment{}, fmt.Errorf("load Replay staging manifest: %w", err)
	}
	sealed, err := s.sealer.Seal(ctx, request.Payload)
	if err != nil {
		return StagedAttachment{}, err
	}
	objectKey := tracePrefix + "/objects/" + attachmentID
	if err := s.objects.Put(ctx, objectKey, sealed.Ciphertext); err != nil {
		return StagedAttachment{}, fmt.Errorf("stage Replay object: %w", err)
	}
	staged := StagedAttachment{
		AttachmentID: attachmentID, TraceID: request.TraceID, IdentityKey: request.IdentityKey,
		Class: sealed.Class, SchemaVersion: sealed.SchemaVersion, PlaintextSHA256: sealed.PlaintextSHA256,
		ObjectKey: objectKey, CiphertextBytes: len(sealed.Ciphertext), CiphertextSHA256: sealed.CiphertextSHA256,
		Compression: sealed.Compression, Encryption: sealed.Encryption, KeyID: sealed.KeyID,
		WrappedKey: append([]byte(nil), sealed.WrappedKey...), Nonce: append([]byte(nil), sealed.Nonce...),
		ExpiresAt: time.Now().UTC().Add(s.retention),
	}
	manifest := objectStagerManifest{TraceID: string(request.TraceID), IdentityKey: request.IdentityKey, Attachment: staged}
	encodedManifest, err = json.Marshal(manifest)
	if err != nil {
		return StagedAttachment{}, err
	}
	if err := s.objects.Put(ctx, manifestKey, encodedManifest); err != nil {
		return StagedAttachment{}, fmt.Errorf("store Replay staging manifest: %w", err)
	}
	s.attachments[attachmentID] = cloneStagedAttachment(staged)
	return cloneStagedAttachment(staged), nil
}

func StagingTracePrefix(objectPrefix string, traceID agentobs.TraceID) string {
	objectPrefix = strings.Trim(strings.TrimSpace(objectPrefix), "/")
	if objectPrefix == "" {
		objectPrefix = "agent-replay-staging"
	}
	digest := sha256.Sum256([]byte(traceID))
	return objectPrefix + "/traces/" + hex.EncodeToString(digest[:])
}

func (s *ObjectStager) StagedAttachment(attachmentID string) (StagedAttachment, bool) {
	if s == nil {
		return StagedAttachment{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	staged, ok := s.attachments[attachmentID]
	return cloneStagedAttachment(staged), ok
}

func cloneStagedAttachment(staged StagedAttachment) StagedAttachment {
	staged.WrappedKey = append([]byte(nil), staged.WrappedKey...)
	staged.Nonce = append([]byte(nil), staged.Nonce...)
	return staged
}
