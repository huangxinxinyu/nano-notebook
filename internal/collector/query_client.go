package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type QueryClient interface {
	List(context.Context, TraceListQuery) (TraceListResult, error)
	Detail(context.Context, agentobs.TraceID) (ProjectedTrace, error)
	Replay(context.Context, agentobs.TraceID, agentobs.SpanID, string) (OpaqueReplay, error)
}

type HTTPQueryClientConfig struct {
	Endpoint         string
	ServiceToken     string
	Client           *http.Client
	MaxResponseBytes int64
}

type HTTPQueryClient struct {
	endpoint, token string
	client          *http.Client
	maxBytes        int64
}

func NewHTTPQueryClient(config HTTPQueryClientConfig) (*HTTPQueryClient, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 4 * 1024 * 1024
	}
	if endpoint == "" || strings.TrimSpace(config.ServiceToken) == "" || config.MaxResponseBytes < 1 {
		return nil, errors.New("Collector Query Client configuration is incomplete")
	}
	return &HTTPQueryClient{endpoint: endpoint, token: config.ServiceToken, client: config.Client, maxBytes: config.MaxResponseBytes}, nil
}

func (c *HTTPQueryClient) List(ctx context.Context, query TraceListQuery) (TraceListResult, error) {
	parameters := url.Values{}
	setQueryInt64(parameters, "started_after_unix_nano", query.StartedAfterUnixNano)
	setQueryInt64(parameters, "started_before_unix_nano", query.StartedBeforeUnixNano)
	parameters.Set("identity", query.IdentityExact)
	parameters.Set("identity_prefix", query.IdentityPrefix)
	parameters.Set("agent", query.AgentName)
	parameters.Set("model", query.ModelName)
	parameters.Set("status", query.Status)
	parameters.Set("cursor", query.Cursor)
	if query.Active != nil {
		parameters.Set("active", strconv.FormatBool(*query.Active))
	}
	if query.PageSize > 0 {
		parameters.Set("page_size", strconv.Itoa(query.PageSize))
	}
	var response struct {
		Data TraceListResult `json:"data"`
	}
	err := c.get(ctx, "/internal/agent-observability/v1/traces?"+parameters.Encode(), &response)
	return response.Data, err
}

func (c *HTTPQueryClient) Detail(ctx context.Context, traceID agentobs.TraceID) (ProjectedTrace, error) {
	var response struct {
		Data ProjectedTrace `json:"data"`
	}
	err := c.get(ctx, "/internal/agent-observability/v1/traces/"+url.PathEscape(string(traceID)), &response)
	return response.Data, err
}

func (c *HTTPQueryClient) Replay(ctx context.Context, traceID agentobs.TraceID, spanID agentobs.SpanID, replayID string) (OpaqueReplay, error) {
	var response struct {
		Data OpaqueReplay `json:"data"`
	}
	path := "/internal/agent-observability/v1/traces/" + url.PathEscape(string(traceID)) + "/replay/" + url.PathEscape(replayID) + "?span_id=" + url.QueryEscape(string(spanID))
	err := c.get(ctx, path, &response)
	return response.Data, err
}

func (c *HTTPQueryClient) get(ctx context.Context, path string, target any) error {
	if c == nil || c.client == nil {
		return errors.New("nil Collector Query Client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil || int64(len(body)) > c.maxBytes {
		return errors.New("Collector Query response is unavailable")
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		switch failure.Error.Code {
		case "trace_not_found":
			return ErrTraceNotFound
		case "trace_projection_pending":
			return ErrProjectionPending
		case "replay_expired":
			return ErrReplayExpired
		case "replay_not_found":
			return ErrReplayNotFound
		case "replay_unavailable":
			return ErrReplayUnavailable
		default:
			return fmt.Errorf("Collector Query failed with status %d", response.StatusCode)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(target); err != nil {
		return errors.New("Collector Query response is invalid")
	}
	return nil
}

func setQueryInt64(values url.Values, key string, value *int64) {
	if value != nil {
		values.Set(key, strconv.FormatInt(*value, 10))
	}
}
