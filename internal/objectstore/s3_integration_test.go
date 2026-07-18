package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

func TestS3StoreRoundTripsOpaqueBytesAgainstCompatibleStorage(t *testing.T) {
	endpoint := os.Getenv("NANO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("NANO_TEST_S3_ENDPOINT is not configured")
	}
	config := objectstore.S3Config{
		Endpoint: endpoint, AccessKeyID: testEnv("NANO_TEST_S3_ACCESS_KEY_ID", "nano"),
		SecretAccessKey: testEnv("NANO_TEST_S3_SECRET_ACCESS_KEY", "nano-password"),
		Bucket:          testEnv("NANO_TEST_S3_BUCKET", "nano-agent-replay-staging"),
		Region:          testEnv("NANO_TEST_S3_REGION", "us-east-1"),
	}
	store, err := objectstore.NewS3Store(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.CheckReady(ctx); err != nil {
		t.Fatal(err)
	}
	key := "integration/" + uuid.NewString()
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })
	payload := bytes.Repeat([]byte{0xa5}, 4096)
	if err := store.Put(ctx, key, payload); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := store.Stat(ctx, key)
	if err != nil || info.Key != key || info.Size != int64(len(payload)) {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	if _, err := store.Get(ctx, key, int64(len(payload)-1)); !errors.Is(err, objectstore.ErrObjectTooLarge) {
		t.Fatalf("bounded Get error = %v", err)
	}
	got, err := store.Get(ctx, key, int64(len(payload)))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("Get bytes=%d err=%v", len(got), err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Stat(ctx, key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("Stat after Delete error = %v", err)
	}
}

func testEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
