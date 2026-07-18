package objectstore_test

import (
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

func TestS3StoreRejectsIncompleteOrAmbiguousConfiguration(t *testing.T) {
	tests := []objectstore.S3Config{
		{},
		{Endpoint: "127.0.0.1:9000", AccessKeyID: "key", SecretAccessKey: "secret"},
		{Endpoint: "http://127.0.0.1:9000", AccessKeyID: "key", SecretAccessKey: "secret", Bucket: "replay"},
	}
	for _, config := range tests {
		if _, err := objectstore.NewS3Store(config); err == nil {
			t.Fatalf("NewS3Store(%#v) succeeded", config)
		}
	}
}

func TestS3StoreAcceptsExplicitS3CompatibleConfigurationWithoutConnecting(t *testing.T) {
	store, err := objectstore.NewS3Store(objectstore.S3Config{
		Endpoint: "127.0.0.1:9000", AccessKeyID: "key", SecretAccessKey: "secret",
		Bucket: "nano-agent-replay", Region: "us-east-1",
	})
	if err != nil || store == nil {
		t.Fatalf("NewS3Store = %#v, %v", store, err)
	}
}
