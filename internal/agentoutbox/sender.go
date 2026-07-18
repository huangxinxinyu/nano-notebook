package agentoutbox

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

const defaultMaxResultBytes int64 = 2 * 1024 * 1024

const CodeTransportFailure = "transport_failure"

type SenderStore interface {
	ClaimBatch(context.Context) (ClaimedBatch, bool, error)
	ApplyResult(context.Context, ClaimedBatch, collector.BatchResult) error
}

type PurgeSenderStore interface {
	ClaimPurgeBatch(context.Context) (ClaimedPurgeBatch, bool, error)
	ApplyPurgeResult(context.Context, ClaimedPurgeBatch, collector.PurgeBatchResult) error
	ReleasePurgeBatch(context.Context, ClaimedPurgeBatch, string) error
}

type SenderConfig struct {
	Endpoint       string
	PurgeEndpoint  string
	ServiceToken   string
	HTTPClient     *http.Client
	MaxResultBytes int64
	ReportError    func(error)
}

type Sender struct {
	store          SenderStore
	endpoint       string
	purgeEndpoint  string
	serviceToken   string
	httpClient     *http.Client
	maxResultBytes int64
	reportError    func(error)
}

func NewSender(store SenderStore, config SenderConfig) (*Sender, error) {
	if store == nil {
		return nil, errors.New("Outbox Sender store is required")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, errors.New("Outbox Sender endpoint is invalid")
	}
	if strings.TrimSpace(config.ServiceToken) == "" {
		return nil, errors.New("Outbox Sender service token is required")
	}
	if config.PurgeEndpoint == "" {
		config.PurgeEndpoint = strings.TrimSuffix(endpoint.String(), "/batches") + "/purges"
	}
	purgeEndpoint, err := url.Parse(config.PurgeEndpoint)
	if err != nil || (purgeEndpoint.Scheme != "http" && purgeEndpoint.Scheme != "https") || purgeEndpoint.Host == "" {
		return nil, errors.New("Outbox Sender purge endpoint is invalid")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.MaxResultBytes == 0 {
		config.MaxResultBytes = defaultMaxResultBytes
	}
	if config.MaxResultBytes < 1 {
		return nil, errors.New("Outbox Sender result limit must be positive")
	}
	return &Sender{
		store: store, endpoint: endpoint.String(), purgeEndpoint: purgeEndpoint.String(), serviceToken: config.ServiceToken,
		httpClient: config.HTTPClient, maxResultBytes: config.MaxResultBytes,
		reportError: config.ReportError,
	}, nil
}

func (s *Sender) SendOnce(ctx context.Context) (bool, error) {
	if purgeStore, ok := s.store.(PurgeSenderStore); ok {
		attempted, err := s.sendPurgeOnce(ctx, purgeStore)
		if attempted || err != nil {
			return attempted, err
		}
	}
	claimed, ok, err := s.store.ClaimBatch(ctx)
	if err != nil || !ok {
		return false, err
	}
	encoded, err := json.Marshal(claimed.Batch)
	if err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("encode Collector Batch: %w", err))
	}
	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write(encoded); err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("compress Collector Batch: %w", err))
	}
	if err := compressor.Close(); err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("finish Collector Batch compression: %w", err))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, &compressed)
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+s.serviceToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("send Collector Batch: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return s.retryClaim(ctx, claimed, fmt.Errorf("Collector Batch returned HTTP %d", response.StatusCode))
	}
	encodedResult, err := io.ReadAll(io.LimitReader(response.Body, s.maxResultBytes+1))
	if err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("read Collector Batch result: %w", err))
	}
	if int64(len(encodedResult)) > s.maxResultBytes {
		return s.retryClaim(ctx, claimed, errors.New("Collector Batch result exceeds configured limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedResult))
	decoder.DisallowUnknownFields()
	var result collector.BatchResult
	if err := decoder.Decode(&result); err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("decode Collector Batch result: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return s.retryClaim(ctx, claimed, errors.New("Collector Batch result has trailing data"))
	}
	if err := s.store.ApplyResult(ctx, claimed, result); err != nil {
		return false, fmt.Errorf("apply Collector Batch result: %w", err)
	}
	return true, nil
}

func (s *Sender) sendPurgeOnce(ctx context.Context, store PurgeSenderStore) (bool, error) {
	claimed, ok, err := store.ClaimPurgeBatch(ctx)
	if err != nil || !ok {
		return false, err
	}
	encoded, err := json.Marshal(claimed.Batch)
	if err != nil {
		return s.retryPurgeClaim(ctx, store, claimed, fmt.Errorf("encode Collector purge Batch: %w", err))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.purgeEndpoint, bytes.NewReader(encoded))
	if err != nil {
		return s.retryPurgeClaim(ctx, store, claimed, err)
	}
	request.Header.Set("Authorization", "Bearer "+s.serviceToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return s.retryPurgeClaim(ctx, store, claimed, fmt.Errorf("send Collector purge Batch: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return s.retryPurgeClaim(ctx, store, claimed, fmt.Errorf("Collector purge Batch returned HTTP %d", response.StatusCode))
	}
	encodedResult, err := io.ReadAll(io.LimitReader(response.Body, s.maxResultBytes+1))
	if err != nil {
		return s.retryPurgeClaim(ctx, store, claimed, fmt.Errorf("read Collector purge result: %w", err))
	}
	if int64(len(encodedResult)) > s.maxResultBytes {
		return s.retryPurgeClaim(ctx, store, claimed, errors.New("Collector purge result exceeds configured limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedResult))
	decoder.DisallowUnknownFields()
	var result collector.PurgeBatchResult
	if err := decoder.Decode(&result); err != nil {
		return s.retryPurgeClaim(ctx, store, claimed, fmt.Errorf("decode Collector purge result: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return s.retryPurgeClaim(ctx, store, claimed, errors.New("Collector purge result has trailing data"))
	}
	if err := store.ApplyPurgeResult(ctx, claimed, result); err != nil {
		return false, fmt.Errorf("apply Collector purge result: %w", err)
	}
	return true, nil
}

func (s *Sender) retryPurgeClaim(ctx context.Context, store PurgeSenderStore, claimed ClaimedPurgeBatch, cause error) (bool, error) {
	if err := store.ReleasePurgeBatch(ctx, claimed, CodeTransportFailure); err != nil {
		return true, errors.Join(cause, fmt.Errorf("release failed Collector purge Batch: %w", err))
	}
	return true, cause
}

func (s *Sender) Run(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return errors.New("Outbox Sender poll interval must be positive")
	}
	for {
		attempted, err := s.SendOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && s.reportError != nil {
			s.reportError(err)
		}
		if attempted && err == nil {
			continue
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (s *Sender) ForceFlush(ctx context.Context) error {
	for {
		attempted, err := s.SendOnce(ctx)
		if err != nil {
			return err
		}
		if !attempted {
			return nil
		}
	}
}

func (s *Sender) retryClaim(ctx context.Context, claimed ClaimedBatch, cause error) (bool, error) {
	result := collector.BatchResult{
		BatchID: claimed.Batch.BatchID,
		Chunks:  make([]collector.ChunkResult, 0, len(claimed.Batch.Chunks)),
	}
	for _, chunk := range claimed.Batch.Chunks {
		result.Chunks = append(result.Chunks, collector.ChunkResult{
			TraceID: chunk.Trace.TraceID, Status: collector.ChunkRetryable,
			CommittedThrough: chunk.FirstSequence - 1, Code: CodeTransportFailure,
		})
	}
	if err := s.store.ApplyResult(ctx, claimed, result); err != nil {
		return true, errors.Join(cause, fmt.Errorf("release failed Collector Batch: %w", err))
	}
	return true, cause
}
