package objectstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

var ErrUploadMismatch = errors.New("uploaded object does not match intent")

type UploadPolicyRequest struct {
	Key           string
	MediaType     string
	ByteSize      int64
	ContentSHA256 string
	ExpiresAt     time.Time
}

type UploadPolicy struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func (s *S3Store) PresignUpload(ctx context.Context, request UploadPolicyRequest) (UploadPolicy, error) {
	if s == nil || s.client == nil {
		return UploadPolicy{}, errors.New("nil S3 object Store")
	}
	request.Key = strings.TrimSpace(request.Key)
	request.MediaType = strings.TrimSpace(request.MediaType)
	request.ContentSHA256 = strings.ToLower(strings.TrimSpace(request.ContentSHA256))
	checksum, err := hex.DecodeString(request.ContentSHA256)
	if request.Key == "" || request.MediaType == "" || request.ByteSize < 1 || request.ByteSize > 100*1024*1024 ||
		err != nil || len(checksum) != 32 || !request.ExpiresAt.After(time.Now().UTC()) {
		return UploadPolicy{}, errors.New("invalid direct upload policy request")
	}

	policy := minio.NewPostPolicy()
	if err := policy.SetBucket(s.bucket); err != nil {
		return UploadPolicy{}, err
	}
	if err := policy.SetKey(request.Key); err != nil {
		return UploadPolicy{}, err
	}
	if err := policy.SetExpires(request.ExpiresAt); err != nil {
		return UploadPolicy{}, err
	}
	if err := policy.SetContentType(request.MediaType); err != nil {
		return UploadPolicy{}, err
	}
	if err := policy.SetContentLengthRange(request.ByteSize, request.ByteSize); err != nil {
		return UploadPolicy{}, err
	}
	if err := policy.SetChecksum(minio.NewChecksum(minio.ChecksumSHA256, checksum)); err != nil {
		return UploadPolicy{}, err
	}
	uploadURL, fields, err := s.client.PresignedPostPolicy(ctx, policy)
	if err != nil {
		return UploadPolicy{}, err
	}
	return UploadPolicy{
		Method: http.MethodPost, URL: uploadURL.String(), Fields: fields, ExpiresAt: request.ExpiresAt,
	}, nil
}

func (s *S3Store) ValidateUpload(ctx context.Context, request UploadPolicyRequest) (ObjectInfo, error) {
	if s == nil || s.client == nil {
		return ObjectInfo{}, errors.New("nil S3 object Store")
	}
	info, err := s.client.StatObject(ctx, s.bucket, strings.TrimSpace(request.Key), minio.StatObjectOptions{Checksum: true})
	if err != nil {
		return ObjectInfo{}, mapS3Error(err)
	}
	if err := validateUploadedObject(request, info); err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: info.Key, Size: info.Size, ModifiedAt: info.LastModified}, nil
}

func (s *S3Store) PromoteUpload(ctx context.Context, request UploadPolicyRequest, destinationKey string) (ObjectInfo, error) {
	if s == nil || s.client == nil {
		return ObjectInfo{}, errors.New("nil S3 object Store")
	}
	request.Key = strings.TrimSpace(request.Key)
	destinationKey = strings.TrimSpace(destinationKey)
	if destinationKey == "" || destinationKey == request.Key {
		return ObjectInfo{}, errors.New("invalid direct upload destination")
	}
	staged, err := s.client.StatObject(ctx, s.bucket, request.Key, minio.StatObjectOptions{Checksum: true})
	if err != nil {
		return ObjectInfo{}, mapS3Error(err)
	}
	if err := validateUploadedObject(request, staged); err != nil {
		return ObjectInfo{}, err
	}
	if _, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: destinationKey},
		minio.CopySrcOptions{Bucket: s.bucket, Object: request.Key, MatchETag: staged.ETag},
	); err != nil {
		return ObjectInfo{}, err
	}
	destinationRequest := request
	destinationRequest.Key = destinationKey
	promoted, err := s.client.StatObject(ctx, s.bucket, destinationKey, minio.StatObjectOptions{Checksum: true})
	if err != nil {
		return ObjectInfo{}, mapS3Error(err)
	}
	if err := validateUploadedObject(destinationRequest, promoted); err != nil {
		return ObjectInfo{}, err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, request.Key, minio.RemoveObjectOptions{}); err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: promoted.Key, Size: promoted.Size, ModifiedAt: promoted.LastModified}, nil
}

func validateUploadedObject(request UploadPolicyRequest, info minio.ObjectInfo) error {
	checksum, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(request.ContentSHA256)))
	if err != nil || len(checksum) != 32 {
		return errors.New("invalid direct upload checksum")
	}
	wantChecksum := minio.NewChecksum(minio.ChecksumSHA256, checksum).Encoded()
	if info.Size != request.ByteSize || info.ContentType != strings.TrimSpace(request.MediaType) || info.ChecksumSHA256 != wantChecksum {
		return fmt.Errorf(
			"%w: got size=%d media_type=%q checksum=%q; want size=%d media_type=%q checksum=%q",
			ErrUploadMismatch, info.Size, info.ContentType, info.ChecksumSHA256,
			request.ByteSize, strings.TrimSpace(request.MediaType), wantChecksum,
		)
	}
	return nil
}
