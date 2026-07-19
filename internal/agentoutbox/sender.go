package agentoutbox

import (
	"bytes"
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

type PurgeSenderStore interface {
	ClaimPurgeBatch(context.Context) (ClaimedPurgeBatch, bool, error)
	ApplyPurgeResult(context.Context, ClaimedPurgeBatch, collector.PurgeBatchResult) error
	ReleasePurgeBatch(context.Context, ClaimedPurgeBatch, string) error
}

type SenderConfig struct {
	PurgeEndpoint  string
	ServiceToken   string
	HTTPClient     *http.Client
	MaxResultBytes int64
	ReportError    func(error)
}

type PurgeSender struct {
	store          PurgeSenderStore
	purgeEndpoint  string
	serviceToken   string
	httpClient     *http.Client
	maxResultBytes int64
	reportError    func(error)
}

func NewPurgeSender(store PurgeSenderStore, config SenderConfig) (*PurgeSender, error) {
	if store == nil {
		return nil, errors.New("purge Outbox Sender store is required")
	}
	purgeEndpoint, err := url.Parse(config.PurgeEndpoint)
	if err != nil || (purgeEndpoint.Scheme != "http" && purgeEndpoint.Scheme != "https") || purgeEndpoint.Host == "" {
		return nil, errors.New("purge Outbox Sender endpoint is invalid")
	}
	if strings.TrimSpace(config.ServiceToken) == "" {
		return nil, errors.New("purge Outbox Sender service token is required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.MaxResultBytes == 0 {
		config.MaxResultBytes = defaultMaxResultBytes
	}
	if config.MaxResultBytes < 1 {
		return nil, errors.New("purge Outbox Sender result limit must be positive")
	}
	return &PurgeSender{
		store: store, purgeEndpoint: purgeEndpoint.String(), serviceToken: config.ServiceToken,
		httpClient: config.HTTPClient, maxResultBytes: config.MaxResultBytes, reportError: config.ReportError,
	}, nil
}

func (s *PurgeSender) SendOnce(ctx context.Context) (bool, error) {
	claimed, ok, err := s.store.ClaimPurgeBatch(ctx)
	if err != nil || !ok {
		return false, err
	}
	encoded, err := json.Marshal(claimed.Batch)
	if err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("encode Collector purge Batch: %w", err))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.purgeEndpoint, bytes.NewReader(encoded))
	if err != nil {
		return s.retryClaim(ctx, claimed, err)
	}
	request.Header.Set("Authorization", "Bearer "+s.serviceToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("send Collector purge Batch: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return s.retryClaim(ctx, claimed, fmt.Errorf("Collector purge Batch returned HTTP %d", response.StatusCode))
	}
	encodedResult, err := io.ReadAll(io.LimitReader(response.Body, s.maxResultBytes+1))
	if err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("read Collector purge result: %w", err))
	}
	if int64(len(encodedResult)) > s.maxResultBytes {
		return s.retryClaim(ctx, claimed, errors.New("Collector purge result exceeds configured limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedResult))
	decoder.DisallowUnknownFields()
	var result collector.PurgeBatchResult
	if err := decoder.Decode(&result); err != nil {
		return s.retryClaim(ctx, claimed, fmt.Errorf("decode Collector purge result: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return s.retryClaim(ctx, claimed, errors.New("Collector purge result has trailing data"))
	}
	if err := s.store.ApplyPurgeResult(ctx, claimed, result); err != nil {
		return false, fmt.Errorf("apply Collector purge result: %w", err)
	}
	return true, nil
}

func (s *PurgeSender) retryClaim(ctx context.Context, claimed ClaimedPurgeBatch, cause error) (bool, error) {
	if err := s.store.ReleasePurgeBatch(ctx, claimed, CodeTransportFailure); err != nil {
		return true, errors.Join(cause, fmt.Errorf("release failed Collector purge Batch: %w", err))
	}
	return true, cause
}

func (s *PurgeSender) Run(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return errors.New("purge Outbox Sender poll interval must be positive")
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

func (s *PurgeSender) ForceFlush(ctx context.Context) error {
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
