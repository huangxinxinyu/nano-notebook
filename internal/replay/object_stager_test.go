package replay_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestObjectStagerPersistsReplayWithoutPostgresAndRecoversManifest(t *testing.T) {
	objects := objectstore.NewMemoryStore()
	keys, err := replay.NewDevelopmentKeyProvider("test-key", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := replay.NewSealer(keys)
	if err != nil {
		t.Fatal(err)
	}
	stager, err := replay.NewObjectStager(sealer, objects, replay.StagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request := replay.StageRequest{TraceID: "trace-object-stage", IdentityKey: "model/1/request", Payload: payload}
	first, err := stager.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := replay.NewObjectStager(sealer, objects, replay.StagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.AttachmentID == "" || first.AttachmentID != second.AttachmentID ||
		first.ObjectKey != second.ObjectKey || first.CiphertextSHA256 != second.CiphertextSHA256 {
		t.Fatalf("recovered attachment changed: %#v / %#v", first, second)
	}
	if got, ok := reopened.StagedAttachment(first.AttachmentID); !ok || !reflect.DeepEqual(got, second) {
		t.Fatalf("StagedAttachment = %#v, %t", got, ok)
	}
}
