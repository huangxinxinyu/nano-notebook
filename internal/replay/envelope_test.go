package replay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestSealerCompressesEnvelopeEncryptsAndAuthenticatesReplay(t *testing.T) {
	provider, err := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("NewDevelopmentKeyProvider: %v", err)
	}
	sealer, err := replay.NewSealer(provider)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	plain, err := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[{"role":"user","text":"sensitive dinner plan"}]}`))
	if err != nil {
		t.Fatalf("NewPlainPayload: %v", err)
	}
	sealed, err := sealer.Seal(context.Background(), plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.Class != plain.Class || sealed.SchemaVersion != 1 || sealed.PlaintextSHA256 != plain.SHA256 ||
		sealed.Compression != replay.CompressionGZIP || sealed.Encryption != replay.EncryptionAES256GCM ||
		sealed.KeyID != "dev-key-v1" || len(sealed.WrappedKey) == 0 || len(sealed.Nonce) == 0 ||
		len(sealed.Ciphertext) == 0 || sealed.CiphertextSHA256 == "" {
		t.Fatalf("sealed Replay = %#v", sealed)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("sensitive dinner plan")) {
		t.Fatal("ciphertext contains Replay plaintext")
	}
	opened, err := sealer.Open(context.Background(), sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.Class != plain.Class || opened.SHA256 != plain.SHA256 || !bytes.Equal(opened.Bytes, plain.Bytes) {
		t.Fatalf("opened Replay = %#v", opened)
	}

	tampered := sealed
	tampered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	tampered.Ciphertext[len(tampered.Ciphertext)/2] ^= 0xff
	if _, err := sealer.Open(context.Background(), tampered); !errors.Is(err, replay.ErrIntegrity) {
		t.Fatalf("tampered Open error = %v, want ErrIntegrity", err)
	}
}

func TestSealerRejectsCiphertextAboveAttachmentLimit(t *testing.T) {
	provider, err := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x24}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := replay.NewSealer(provider)
	if err != nil {
		t.Fatal(err)
	}
	incompressible := make([]byte, replay.MaxPlaintextBytes)
	if _, err := rand.Read(incompressible); err != nil {
		t.Fatalf("random plaintext: %v", err)
	}
	plain, err := replay.NewPlainPayload(replay.ClassActionResult, 1, incompressible)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealer.Seal(context.Background(), plain); !errors.Is(err, replay.ErrCiphertextTooLarge) {
		t.Fatalf("Seal error = %v, want ErrCiphertextTooLarge", err)
	}
}

func TestDevelopmentKeyProviderRejectsInvalidKeysAndWrongKEK(t *testing.T) {
	if _, err := replay.NewDevelopmentKeyProvider("dev-key-v1", []byte("short")); err == nil {
		t.Fatal("NewDevelopmentKeyProvider accepted short KEK")
	}
	first, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{1}, 32))
	second, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{2}, 32))
	firstSealer, _ := replay.NewSealer(first)
	secondSealer, _ := replay.NewSealer(second)
	plain, _ := replay.NewPlainPayload(replay.ClassActionInput, 1, []byte(`{"schema_version":1,"class":"action_input","input":{}}`))
	sealed, err := firstSealer.Seal(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondSealer.Open(context.Background(), sealed); !errors.Is(err, replay.ErrKeyUnavailable) {
		t.Fatalf("Open with wrong KEK error = %v, want ErrKeyUnavailable", err)
	}
}
