package objectstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

func TestS3StorePresignsChecksumBoundDirectUploadPolicy(t *testing.T) {
	endpoint := os.Getenv("NANO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("NANO_TEST_S3_ENDPOINT is not configured")
	}
	store, err := objectstore.NewS3Store(objectstore.S3Config{
		Endpoint: endpoint, AccessKeyID: directUploadTestEnv("NANO_TEST_S3_ACCESS_KEY_ID", "nano"),
		SecretAccessKey: directUploadTestEnv("NANO_TEST_S3_SECRET_ACCESS_KEY", "nano-password"),
		Bucket:          directUploadTestEnv("NANO_TEST_S3_BUCKET", "nano-agent-replay-staging"),
		Region:          directUploadTestEnv("NANO_TEST_S3_REGION", "us-east-1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	checksumHex := "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592"
	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	policy, err := store.PresignUpload(context.Background(), objectstore.UploadPolicyRequest{
		Key: "sources/upl_policy/original", MediaType: "text/plain", ByteSize: 43,
		ContentSHA256: checksumHex, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	if policy.URL == "" || policy.Method != "POST" || !policy.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("upload policy = %+v", policy)
	}
	rawChecksum, err := hex.DecodeString(checksumHex)
	if err != nil {
		t.Fatal(err)
	}
	wantChecksum := base64.StdEncoding.EncodeToString(rawChecksum)
	for key, want := range map[string]string{
		"key":                      "sources/upl_policy/original",
		"Content-Type":             "text/plain",
		"x-amz-checksum-algorithm": "SHA256",
		"x-amz-checksum-sha256":    wantChecksum,
	} {
		if got := policy.Fields[key]; got != want {
			t.Fatalf("policy field %q = %q, want %q", key, got, want)
		}
	}
}

func TestS3StoreValidatesACompletedDirectUpload(t *testing.T) {
	endpoint := os.Getenv("NANO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("NANO_TEST_S3_ENDPOINT is not configured")
	}
	store, err := objectstore.NewS3Store(objectstore.S3Config{
		Endpoint: endpoint, AccessKeyID: directUploadTestEnv("NANO_TEST_S3_ACCESS_KEY_ID", "nano"),
		SecretAccessKey: directUploadTestEnv("NANO_TEST_S3_SECRET_ACCESS_KEY", "nano-password"),
		Bucket:          directUploadTestEnv("NANO_TEST_S3_BUCKET", "nano-agent-replay-staging"),
		Region:          directUploadTestEnv("NANO_TEST_S3_REGION", "us-east-1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("The quick brown fox jumps over the lazy dog")
	digest := sha256.Sum256(payload)
	request := objectstore.UploadPolicyRequest{
		Key: "sources/upl_completed/original", MediaType: "text/plain", ByteSize: int64(len(payload)),
		ContentSHA256: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), request.Key) })
	policy, err := store.PresignUpload(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range policy.Fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	httpRequest, err := http.NewRequestWithContext(context.Background(), policy.Method, policy.URL, &body)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("direct upload status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	info, err := store.ValidateUpload(context.Background(), request)
	if err != nil {
		t.Fatalf("ValidateUpload: %v", err)
	}
	if info.Key != request.Key || info.Size != request.ByteSize {
		t.Fatalf("validated object = %+v, want key %q size %d", info, request.Key, request.ByteSize)
	}
}

func directUploadTestEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
