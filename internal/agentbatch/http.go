package agentbatch

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

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

const defaultMaxResultBytes int64 = 2 * 1024 * 1024

type HTTPSenderConfig struct {
	Endpoint       string
	ServiceToken   string
	HTTPClient     *http.Client
	MaxResultBytes int64
}

type HTTPSender struct {
	endpoint       string
	serviceToken   string
	httpClient     *http.Client
	maxResultBytes int64
}

type deliveryError struct {
	retryable bool
	err       error
}

func (e *deliveryError) Error() string { return e.err.Error() }
func (e *deliveryError) Unwrap() error { return e.err }

func newDeliveryError(retryable bool, err error) error {
	return &deliveryError{retryable: retryable, err: err}
}

// Retryable reports whether sending the exact same Batch may safely be attempted again.
// Unknown Sender errors are conservatively retryable because the commit outcome is uncertain.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var delivery *deliveryError
	if errors.As(err, &delivery) {
		return delivery.retryable
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func NewHTTPSender(config HTTPSenderConfig) (*HTTPSender, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, errors.New("Agent Trace Collector endpoint is invalid")
	}
	config.ServiceToken = strings.TrimSpace(config.ServiceToken)
	if config.ServiceToken == "" {
		return nil, errors.New("Agent Trace Collector service token is required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.MaxResultBytes == 0 {
		config.MaxResultBytes = defaultMaxResultBytes
	}
	if config.MaxResultBytes < 1 {
		return nil, errors.New("Agent Trace Collector result bound must be positive")
	}
	return &HTTPSender{
		endpoint: endpoint.String(), serviceToken: config.ServiceToken,
		httpClient: config.HTTPClient, maxResultBytes: config.MaxResultBytes,
	}, nil
}

func (s *HTTPSender) Send(ctx context.Context, batch collector.Batch) (collector.BatchResult, error) {
	if s == nil || s.httpClient == nil {
		return collector.BatchResult{}, errors.New("nil Agent Trace HTTP Sender")
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return collector.BatchResult{}, newDeliveryError(false, fmt.Errorf("encode Collector Batch: %w", err))
	}
	var body bytes.Buffer
	compressor := gzip.NewWriter(&body)
	if _, err := compressor.Write(encoded); err != nil {
		return collector.BatchResult{}, newDeliveryError(false, fmt.Errorf("compress Collector Batch: %w", err))
	}
	if err := compressor.Close(); err != nil {
		return collector.BatchResult{}, newDeliveryError(false, fmt.Errorf("finish Collector Batch compression: %w", err))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, &body)
	if err != nil {
		return collector.BatchResult{}, newDeliveryError(false, err)
	}
	request.Header.Set("Authorization", "Bearer "+s.serviceToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return collector.BatchResult{}, newDeliveryError(Retryable(err), fmt.Errorf("send Collector Batch: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return collector.BatchResult{}, newDeliveryError(retryable, fmt.Errorf("Collector Batch returned HTTP %d", response.StatusCode))
	}
	encodedResult, err := io.ReadAll(io.LimitReader(response.Body, s.maxResultBytes+1))
	if err != nil {
		return collector.BatchResult{}, newDeliveryError(true, fmt.Errorf("read Collector Batch result: %w", err))
	}
	if int64(len(encodedResult)) > s.maxResultBytes {
		return collector.BatchResult{}, newDeliveryError(false, errors.New("Collector Batch result exceeds configured limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedResult))
	decoder.DisallowUnknownFields()
	var result collector.BatchResult
	if err := decoder.Decode(&result); err != nil {
		return collector.BatchResult{}, newDeliveryError(false, fmt.Errorf("decode Collector Batch result: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return collector.BatchResult{}, newDeliveryError(false, errors.New("Collector Batch result has trailing data"))
	}
	return result, nil
}
