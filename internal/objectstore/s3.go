package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Bucket          string
	Region          string
	UseTLS          bool
}

type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3Store(config S3Config) (*S3Store, error) {
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.AccessKeyID = strings.TrimSpace(config.AccessKeyID)
	config.SecretAccessKey = strings.TrimSpace(config.SecretAccessKey)
	config.Bucket = strings.TrimSpace(config.Bucket)
	if config.Endpoint == "" || config.AccessKeyID == "" || config.SecretAccessKey == "" || config.Bucket == "" {
		return nil, errors.New("S3 object Store configuration is incomplete")
	}
	if strings.Contains(config.Endpoint, "://") {
		return nil, errors.New("S3 endpoint must not include a URL scheme")
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKeyID, config.SecretAccessKey, config.SessionToken),
		Secure: config.UseTLS,
		Region: strings.TrimSpace(config.Region),
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return &S3Store{client: client, bucket: config.Bucket}, nil
}

func (s *S3Store) CheckReady(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("nil S3 object Store")
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check S3 bucket %q: %w", s.bucket, err)
	}
	if !exists {
		return fmt.Errorf("S3 bucket %q does not exist", s.bucket)
	}
	return nil
}

func (s *S3Store) Put(ctx context.Context, key string, payload []byte) error {
	if s == nil || s.client == nil {
		return errors.New("nil S3 object Store")
	}
	if err := validateObjectWrite(key, payload); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("put S3 object: %w", err)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("nil S3 object Store")
	}
	if strings.TrimSpace(key) == "" || maxBytes < 1 {
		return nil, errors.New("object key and positive read limit are required")
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3Error(err)
	}
	defer object.Close()
	info, err := object.Stat()
	if err != nil {
		return nil, mapS3Error(err)
	}
	if info.Size > maxBytes {
		return nil, ErrObjectTooLarge
	}
	payload, err := io.ReadAll(io.LimitReader(object, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read S3 object: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return nil, ErrObjectTooLarge
	}
	if int64(len(payload)) != info.Size {
		return nil, errors.New("S3 object size changed while reading")
	}
	return payload, nil
}

func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if s == nil || s.client == nil {
		return ObjectInfo{}, errors.New("nil S3 object Store")
	}
	if strings.TrimSpace(key) == "" {
		return ObjectInfo{}, errors.New("object key is required")
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, mapS3Error(err)
	}
	return ObjectInfo{Key: key, Size: info.Size, ModifiedAt: info.LastModified}, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil {
		return errors.New("nil S3 object Store")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("object key is required")
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete S3 object: %w", mapS3Error(err))
	}
	return nil
}

func (s *S3Store) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("nil S3 object Store")
	}
	if limit < 1 {
		return nil, errors.New("object list limit must be positive")
	}
	items := make([]ObjectInfo, 0, limit)
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix: prefix, StartAfter: after, Recursive: true, MaxKeys: limit,
	}) {
		if object.Err != nil {
			return nil, mapS3Error(object.Err)
		}
		items = append(items, ObjectInfo{Key: object.Key, Size: object.Size, ModifiedAt: object.LastModified})
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

func validateObjectWrite(key string, payload []byte) error {
	if strings.TrimSpace(key) == "" || len(payload) == 0 {
		return errors.New("object key and payload are required")
	}
	return nil
}

func mapS3Error(err error) error {
	response := minio.ToErrorResponse(err)
	switch response.Code {
	case "NoSuchKey", "NoSuchObject", "NoSuchBucket", "NotFound":
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	default:
		return err
	}
}
