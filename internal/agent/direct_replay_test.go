package agent

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestDirectReplayDescriptorBindsToReferencingRecordIdentity(t *testing.T) {
	stager := &descriptorReplayStager{staged: replay.StagedAttachment{
		AttachmentID: "019bf000-0000-7000-8000-000000000303", Class: replay.ClassModelRequest,
		SchemaVersion: 1, PlaintextSHA256: "plain", ObjectKey: "staging/object", CiphertextBytes: 10,
		CiphertextSHA256: "cipher", Compression: replay.CompressionGZIP, Encryption: replay.EncryptionAES256GCM,
		KeyID: "key", WrappedKey: []byte("wrapped"), Nonce: []byte("nonce"), ExpiresAt: time.Now().Add(time.Hour),
	}}
	record := agentobs.Record{IdentityKey: "run/1/model/1/start", Attributes: []agentobs.Attribute{
		agentobs.String(replay.ModelRequestAttachmentKey, stager.staged.AttachmentID),
	}}
	descriptors, err := directReplayAttachments(stager, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].RecordIdentityKey != record.IdentityKey ||
		descriptors[0].AttachmentID != stager.staged.AttachmentID {
		t.Fatalf("direct Replay descriptors = %#v", descriptors)
	}
}

type descriptorReplayStager struct {
	staged replay.StagedAttachment
}

func (s *descriptorReplayStager) Stage(context.Context, replay.StageRequest) (replay.StagedAttachment, error) {
	return s.staged, nil
}

func (s *descriptorReplayStager) StagedAttachment(attachmentID string) (replay.StagedAttachment, bool) {
	return s.staged, attachmentID == s.staged.AttachmentID
}
