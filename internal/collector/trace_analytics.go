package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
)

const TraceAnalyticsSchemaVersion = 1

type TraceAnalyticsQueryKind string

const (
	TraceAnalyticsOverview   TraceAnalyticsQueryKind = "overview"
	TraceAnalyticsTimeseries TraceAnalyticsQueryKind = "timeseries"
	TraceAnalyticsLatency    TraceAnalyticsQueryKind = "latency"
	TraceAnalyticsBreakdowns TraceAnalyticsQueryKind = "breakdowns"
	TraceAnalyticsTools      TraceAnalyticsQueryKind = "tools"
)

type AnalyticsBucket string

const (
	AnalyticsBucketFiveMinutes    AnalyticsBucket = "5m"
	AnalyticsBucketFifteenMinutes AnalyticsBucket = "15m"
	AnalyticsBucketHour           AnalyticsBucket = "1h"
	AnalyticsBucketSixHours       AnalyticsBucket = "6h"
	AnalyticsBucketDay            AnalyticsBucket = "1d"
)

type AnalyticsDimension string

const (
	AnalyticsDimensionAgent             AnalyticsDimension = "agent"
	AnalyticsDimensionModel             AnalyticsDimension = "model"
	AnalyticsDimensionStatus            AnalyticsDimension = "status"
	AnalyticsDimensionErrorCode         AnalyticsDimension = "error_code"
	AnalyticsDimensionProvider          AnalyticsDimension = "provider"
	AnalyticsDimensionTool              AnalyticsDimension = "tool"
	AnalyticsDimensionStopReason        AnalyticsDimension = "stop_reason"
	AnalyticsDimensionDefinition        AnalyticsDimension = "agent_definition"
	AnalyticsDimensionPrompt            AnalyticsDimension = "prompt_version"
	AnalyticsDimensionConfiguration     AnalyticsDimension = "configuration_version"
	AnalyticsDimensionDelegationTarget  AnalyticsDimension = "delegation_target"
	AnalyticsDimensionDelegationOutcome AnalyticsDimension = "delegation_outcome"
	AnalyticsDimensionRAGStage          AnalyticsDimension = "rag_stage"
	AnalyticsDimensionRAGDegradation    AnalyticsDimension = "rag_degradation"
	AnalyticsDimensionCitationOutcome   AnalyticsDimension = "citation_outcome"
)

// TraceAnalyticsQuery is the bounded internal query contract shared by every
// analytics endpoint. NotebookIDs is an authorization scope injected by the
// Control Plane; it is never accepted from browser query parameters.
type TraceAnalyticsQuery struct {
	StartedAfterUnixNano  int64
	StartedBeforeUnixNano int64
	Bucket                AnalyticsBucket
	AgentName             string
	ModelName             string
	Status                string
	WorkloadKind          WorkloadKind
	NotebookIDs           []string
	GroupBy               AnalyticsDimension
	Limit                 int
}

type TraceSpanAnalyticsProjection struct {
	TraceID             agentobs.TraceID `json:"trace_id"`
	NotebookID          string           `json:"notebook_id"`
	SpanID              agentobs.SpanID  `json:"span_id"`
	SpanKind            string           `json:"span_kind"`
	Name                string           `json:"name"`
	ToolName            string           `json:"tool_name"`
	Status              agentobs.Status  `json:"status"`
	Outcome             string           `json:"outcome"`
	DurationNanoseconds *int64           `json:"duration_nanoseconds"`
	Provider            string           `json:"provider"`
	RequestedModel      string           `json:"requested_model"`
	SelectedModel       string           `json:"selected_model"`
	CachedTokens        *int64           `json:"cached_tokens"`
	ReasoningTokens     *int64           `json:"reasoning_tokens"`
	ErrorCode           string           `json:"error_code"`
	Retryable           *bool            `json:"retryable"`
	// Prefix-cache diagnostics for model spans: input tokens beside cached
	// ones, the decision's order within its Trace, and hashes of the request's
	// leading system prompt and tool list.
	InputTokens       *int64 `json:"input_tokens"`
	DecisionOrdinal   *int64 `json:"decision_ordinal"`
	StartedAtUnixNano int64  `json:"started_at_unix_nano"`
	SystemSHA256      string `json:"system_sha256"`
	ToolsSHA256       string `json:"tools_sha256"`
}

type TraceAnalyticsProjection struct {
	TraceID              agentobs.TraceID               `json:"trace_id"`
	NotebookID           string                         `json:"notebook_id"`
	Providers            []string                       `json:"providers"`
	CachedTokens         *int64                         `json:"cached_tokens"`
	ReasoningTokens      *int64                         `json:"reasoning_tokens"`
	ErrorCode            string                         `json:"error_code"`
	StopReason           string                         `json:"stop_reason"`
	AgentDefinition      string                         `json:"agent_definition"`
	PromptVersion        string                         `json:"prompt_version"`
	ConfigurationVersion string                         `json:"configuration_version"`
	DelegationTargets    []string                       `json:"delegation_targets"`
	DelegationOutcomes   []string                       `json:"delegation_outcomes"`
	RAGStages            []string                       `json:"rag_stages"`
	RAGDegradations      []string                       `json:"rag_degradations"`
	CitationOutcomes     []string                       `json:"citation_outcomes"`
	Spans                []TraceSpanAnalyticsProjection `json:"spans"`
}

func BuildTraceAnalyticsProjection(projection TraceProjection) TraceAnalyticsProjection {
	result := TraceAnalyticsProjection{
		TraceID: projection.Summary.TraceID, NotebookID: projection.Summary.NotebookID,
		Providers: []string{}, DelegationTargets: []string{}, DelegationOutcomes: []string{},
		RAGStages: []string{}, RAGDegradations: []string{}, CitationOutcomes: []string{},
		Spans: make([]TraceSpanAnalyticsProjection, 0, len(projection.Spans)),
	}
	providers, delegationTargets, delegationOutcomes := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	ragStages, ragDegradations, citationOutcomes := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	models := make([]*ModelAnalysisProjection, 0)
	for _, span := range projection.Spans {
		if span.Name == semconv.AgentExecution {
			result.AgentDefinition = analyticsStringAttribute(span.StartAttributes, "nano.agent.definition")
			result.PromptVersion = analyticsStringAttribute(span.StartAttributes, "nano.run.prompt_version")
			result.ConfigurationVersion = analyticsStringAttribute(span.StartAttributes, "nano.configuration_set.id")
			result.ErrorCode = analyticsStringAttribute(span.EndAttributes, "nano.error.code")
			result.StopReason = result.ErrorCode
			if result.StopReason == "" {
				result.StopReason = analyticsStringAttribute(span.EndAttributes, "nano.run.status")
			}
			continue
		}
		row := TraceSpanAnalyticsProjection{
			TraceID: result.TraceID, NotebookID: result.NotebookID, SpanID: span.SpanID, Name: span.Name,
			Status: span.Status, DurationNanoseconds: span.DurationNanoseconds,
			Outcome:   analyticsStringAttribute(span.EndAttributes, semconv.OperationStatusKey),
			ErrorCode: analyticsStringAttribute(span.EndAttributes, semconv.ErrorKindKey),
			Retryable: analyticsBoolAttribute(span.EndAttributes, "nano.error.retryable"),
		}
		row.ToolName = analyticsStringAttribute(span.StartAttributes, semconv.ActionNameKey)
		if row.ToolName == "" {
			row.ToolName = analyticsStringAttribute(span.EndAttributes, semconv.ActionNameKey)
		}
		switch {
		case span.Model != nil:
			row.SpanKind = "model"
			row.Provider, row.RequestedModel, row.SelectedModel = span.Model.Provider, span.Model.RequestedModel, span.Model.SelectedModel
			row.CachedTokens, row.ReasoningTokens = span.Model.CachedTokens, span.Model.ReasoningTokens
			row.InputTokens, row.StartedAtUnixNano = span.Model.InputTokens, span.StartedAtUnixNano
			row.DecisionOrdinal = intAttributeValue(span.StartAttributes, semconv.DecisionOrdinalKey)
			row.SystemSHA256 = analyticsStringAttribute(span.StartAttributes, "nano.context.system_sha256")
			row.ToolsSHA256 = analyticsStringAttribute(span.StartAttributes, "nano.context.tools_sha256")
			models = append(models, span.Model)
			if row.Provider != "" {
				providers[row.Provider] = struct{}{}
			}
		case row.ToolName != "":
			row.SpanKind = "tool"
			if strings.HasPrefix(row.ToolName, "delegate.") {
				delegationTargets[row.ToolName] = struct{}{}
			}
			if row.ToolName == "search_evidence" {
				ragStages["retrieval"] = struct{}{}
			}
		case span.Name == "nano.grounding":
			row.SpanKind = "rag"
			ragStages["grounding"] = struct{}{}
		default:
			row.SpanKind = "other"
		}
		for _, degradation := range analyticsStringListAttribute(span.EndAttributes, "nano.rag.retrieval.degradations") {
			ragDegradations[degradation] = struct{}{}
		}
		if outcome := analyticsStringAttribute(span.EndAttributes, "nano.rag.grounding.outcome"); outcome != "" {
			citationOutcomes[outcome] = struct{}{}
		}
		result.Spans = append(result.Spans, row)
	}
	for _, event := range projection.Events {
		if strings.HasPrefix(event.Name, "nano.delegation.") {
			if outcome := analyticsStringAttribute(event.Attributes, "nano.delegation.state"); outcome != "" {
				delegationOutcomes[outcome] = struct{}{}
			}
		}
		for _, degradation := range analyticsStringListAttribute(event.Attributes, "nano.rag.retrieval.degradations") {
			ragDegradations[degradation] = struct{}{}
		}
		if outcome := analyticsStringAttribute(event.Attributes, "nano.rag.grounding.outcome"); outcome != "" {
			citationOutcomes[outcome] = struct{}{}
		}
	}
	result.CachedTokens = sumKnownModelMetric(models, func(model *ModelAnalysisProjection) *int64 { return model.CachedTokens })
	result.ReasoningTokens = sumKnownModelMetric(models, func(model *ModelAnalysisProjection) *int64 { return model.ReasoningTokens })
	result.Providers = sortedAnalyticsValues(providers)
	result.DelegationTargets = sortedAnalyticsValues(delegationTargets)
	result.DelegationOutcomes = sortedAnalyticsValues(delegationOutcomes)
	result.RAGStages = sortedAnalyticsValues(ragStages)
	result.RAGDegradations = sortedAnalyticsValues(ragDegradations)
	result.CitationOutcomes = sortedAnalyticsValues(citationOutcomes)
	return result
}

func analyticsStringAttribute(attributes []agentobs.Attribute, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueString {
			return attribute.Value.String
		}
	}
	return ""
}

func analyticsBoolAttribute(attributes []agentobs.Attribute, key string) *bool {
	for _, attribute := range attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueBool {
			value := attribute.Value.Bool
			return &value
		}
	}
	return nil
}

func analyticsStringListAttribute(attributes []agentobs.Attribute, key string) []string {
	value := analyticsStringAttribute(attributes, key)
	if value == "" {
		return nil
	}
	var result []string
	if json.Unmarshal([]byte(value), &result) != nil {
		return nil
	}
	return result
}

func sumKnownModelMetric(models []*ModelAnalysisProjection, metric func(*ModelAnalysisProjection) *int64) *int64 {
	if len(models) == 0 {
		return nil
	}
	var sum int64
	for _, model := range models {
		value := metric(model)
		if value == nil {
			return nil
		}
		sum += *value
	}
	return &sum
}

func sortedAnalyticsValues(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

type TraceAnalyticsFilters struct {
	StartedAfter  time.Time    `json:"started_after"`
	StartedBefore time.Time    `json:"started_before"`
	AgentName     string       `json:"agent,omitempty"`
	ModelName     string       `json:"model,omitempty"`
	Status        string       `json:"status,omitempty"`
	WorkloadKind  WorkloadKind `json:"workload_kind"`
}

type TraceAnalyticsCoverage struct {
	TotalSamples int64 `json:"total_samples"`
	TokenSamples int64 `json:"token_samples"`
	CostSamples  int64 `json:"cost_samples"`
}

type TraceAnalyticsMetadata struct {
	SchemaVersion int                    `json:"schema_version"`
	GeneratedAt   time.Time              `json:"generated_at"`
	FreshThrough  *time.Time             `json:"fresh_through"`
	Filters       TraceAnalyticsFilters  `json:"filters"`
	Bucket        AnalyticsBucket        `json:"bucket"`
	GroupBy       AnalyticsDimension     `json:"group_by,omitempty"`
	Coverage      TraceAnalyticsCoverage `json:"coverage"`
}

type AnalyticsCurrencyTotal struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}

type TraceAnalyticsOverviewData struct {
	RunCount               int64                    `json:"run_count"`
	CompletedCount         int64                    `json:"completed_count"`
	SuccessRate            *float64                 `json:"success_rate"`
	ErrorRate              *float64                 `json:"error_rate"`
	RetryRate              *float64                 `json:"retry_rate"`
	P95DurationNanoseconds *int64                   `json:"p95_duration_nanoseconds"`
	InputTokens            *int64                   `json:"input_tokens"`
	OutputTokens           *int64                   `json:"output_tokens"`
	TotalTokens            *int64                   `json:"total_tokens"`
	Costs                  []AnalyticsCurrencyTotal `json:"costs"`
}

type TraceAnalyticsOverviewResult struct {
	TraceAnalyticsMetadata
	Data TraceAnalyticsOverviewData `json:"data"`
}

type TraceAnalyticsTimeseriesPoint struct {
	BucketStartedAt        time.Time                `json:"bucket_started_at"`
	RunCount               int64                    `json:"run_count"`
	CompletedCount         int64                    `json:"completed_count"`
	SuccessCount           int64                    `json:"success_count"`
	ErrorCount             int64                    `json:"error_count"`
	ActiveCount            int64                    `json:"active_count"`
	RetryCount             int64                    `json:"retry_count"`
	P50DurationNanoseconds *int64                   `json:"p50_duration_nanoseconds"`
	P95DurationNanoseconds *int64                   `json:"p95_duration_nanoseconds"`
	P99DurationNanoseconds *int64                   `json:"p99_duration_nanoseconds"`
	InputTokens            *int64                   `json:"input_tokens"`
	OutputTokens           *int64                   `json:"output_tokens"`
	TotalTokens            *int64                   `json:"total_tokens"`
	Costs                  []AnalyticsCurrencyTotal `json:"costs"`
	Coverage               TraceAnalyticsCoverage   `json:"coverage"`
}

type TraceAnalyticsTimeseriesResult struct {
	TraceAnalyticsMetadata
	Data []TraceAnalyticsTimeseriesPoint `json:"data"`
}

type TraceAnalyticsLatencyRow struct {
	Value                  string `json:"value"`
	SampleCount            int64  `json:"sample_count"`
	P50DurationNanoseconds int64  `json:"p50_duration_nanoseconds"`
	P95DurationNanoseconds int64  `json:"p95_duration_nanoseconds"`
	P99DurationNanoseconds int64  `json:"p99_duration_nanoseconds"`
}

type TraceAnalyticsLatencyResult struct {
	TraceAnalyticsMetadata
	Data []TraceAnalyticsLatencyRow `json:"data"`
}

type TraceAnalyticsBreakdownRow struct {
	Value                  string   `json:"value"`
	RunCount               int64    `json:"run_count"`
	CompletedCount         int64    `json:"completed_count"`
	SuccessRate            *float64 `json:"success_rate"`
	ErrorRate              *float64 `json:"error_rate"`
	RetryRate              *float64 `json:"retry_rate"`
	P95DurationNanoseconds *int64   `json:"p95_duration_nanoseconds"`
}

type TraceAnalyticsBreakdownResult struct {
	TraceAnalyticsMetadata
	Data []TraceAnalyticsBreakdownRow `json:"data"`
}

type TraceAnalyticsToolRow struct {
	Value                  string   `json:"value"`
	CallCount              int64    `json:"call_count"`
	CompletedCount         int64    `json:"completed_count"`
	SuccessRate            *float64 `json:"success_rate"`
	ErrorRate              *float64 `json:"error_rate"`
	P95DurationNanoseconds *int64   `json:"p95_duration_nanoseconds"`
}

type TraceAnalyticsToolsResult struct {
	TraceAnalyticsMetadata
	Data []TraceAnalyticsToolRow `json:"data"`
}

type ClickHouseTraceAnalyticsQueryStore struct {
	connection driver.Conn
	now        func() time.Time
}

func NewClickHouseTraceAnalyticsQueryStore(connection driver.Conn) (*ClickHouseTraceAnalyticsQueryStore, error) {
	if connection == nil {
		return nil, errors.New("Collector ClickHouse Trace analytics database is required")
	}
	return &ClickHouseTraceAnalyticsQueryStore{connection: connection, now: time.Now}, nil
}

func (s *ClickHouseTraceAnalyticsQueryStore) Overview(ctx context.Context, query TraceAnalyticsQuery) (TraceAnalyticsOverviewResult, error) {
	if s == nil || s.connection == nil {
		return TraceAnalyticsOverviewResult{}, errors.New("nil Collector ClickHouse Trace analytics Store")
	}
	now := s.now().UTC()
	query, err := NormalizeTraceAnalyticsQuery(now, TraceAnalyticsOverview, query)
	if err != nil {
		return TraceAnalyticsOverviewResult{}, err
	}
	where, args := clickHouseAnalyticsWhere(query)
	var runCount, completed, succeeded, failed, retried uint64
	var tokenSamples, costSamples uint64
	var inputTokens, outputTokens, totalTokens, freshThrough int64
	var p95 *int64
	err = s.connection.QueryRow(ctx, `
		SELECT
			count(),
			countIf(NOT s.active),
			countIf(NOT s.active AND s.status = 'ok'),
			countIf(NOT s.active AND s.status = 'error'),
			countIf(NOT s.active AND s.attempt_count > 1),
			countIf(isNotNull(s.total_tokens)),
			countIf(s.cost_known AND isNotNull(s.cost_amount)),
			sumIf(assumeNotNull(s.input_tokens), isNotNull(s.input_tokens)),
			sumIf(assumeNotNull(s.output_tokens), isNotNull(s.output_tokens)),
			sumIf(assumeNotNull(s.total_tokens), isNotNull(s.total_tokens)),
			max(s.last_observed_unix_nano),
			if(countIf(NOT s.active AND isNotNull(s.duration_nanoseconds)) = 0,
				CAST(NULL, 'Nullable(Int64)'),
				toNullable(quantileExactIf(0.95)(assumeNotNull(s.duration_nanoseconds), NOT s.active AND isNotNull(s.duration_nanoseconds))))
		FROM obs_trace_summaries AS s FINAL
		`+where,
		args...,
	).Scan(&runCount, &completed, &succeeded, &failed, &retried, &tokenSamples, &costSamples,
		&inputTokens, &outputTokens, &totalTokens, &freshThrough, &p95)
	if err != nil {
		return TraceAnalyticsOverviewResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics overview: %w", err)
	}
	result := TraceAnalyticsOverviewResult{
		TraceAnalyticsMetadata: traceAnalyticsMetadata(now, query),
		Data: TraceAnalyticsOverviewData{
			RunCount: int64(runCount), CompletedCount: int64(completed),
			P95DurationNanoseconds: p95, Costs: make([]AnalyticsCurrencyTotal, 0),
		},
	}
	result.Coverage = TraceAnalyticsCoverage{TotalSamples: int64(runCount), TokenSamples: int64(tokenSamples), CostSamples: int64(costSamples)}
	if runCount > 0 {
		fresh := time.Unix(0, int64(freshThrough)).UTC()
		result.FreshThrough = &fresh
	}
	if completed > 0 {
		result.Data.SuccessRate = float64Pointer(float64(succeeded) / float64(completed))
		result.Data.ErrorRate = float64Pointer(float64(failed) / float64(completed))
		result.Data.RetryRate = float64Pointer(float64(retried) / float64(completed))
	}
	if tokenSamples > 0 {
		result.Data.InputTokens = int64Pointer(inputTokens)
		result.Data.OutputTokens = int64Pointer(outputTokens)
		result.Data.TotalTokens = int64Pointer(totalTokens)
	}
	costRows, err := s.connection.Query(ctx, `
		SELECT s.cost_currency, sum(assumeNotNull(s.cost_amount))
		FROM obs_trace_summaries AS s FINAL
		`+where+` AND s.cost_known AND isNotNull(s.cost_amount)
		GROUP BY s.cost_currency
		ORDER BY s.cost_currency
	`, args...)
	if err != nil {
		return TraceAnalyticsOverviewResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics costs: %w", err)
	}
	defer costRows.Close()
	for costRows.Next() {
		var total AnalyticsCurrencyTotal
		if err := costRows.Scan(&total.Currency, &total.Amount); err != nil {
			return TraceAnalyticsOverviewResult{}, err
		}
		result.Data.Costs = append(result.Data.Costs, total)
	}
	if err := costRows.Err(); err != nil {
		return TraceAnalyticsOverviewResult{}, err
	}
	return result, nil
}

func (s *ClickHouseTraceAnalyticsQueryStore) Timeseries(ctx context.Context, query TraceAnalyticsQuery) (TraceAnalyticsTimeseriesResult, error) {
	if s == nil || s.connection == nil {
		return TraceAnalyticsTimeseriesResult{}, errors.New("nil Collector ClickHouse Trace analytics Store")
	}
	now := s.now().UTC()
	query, err := NormalizeTraceAnalyticsQuery(now, TraceAnalyticsTimeseries, query)
	if err != nil {
		return TraceAnalyticsTimeseriesResult{}, err
	}
	bucketExpression := clickHouseAnalyticsBucketExpression(query.Bucket)
	where, args := clickHouseAnalyticsWhere(query)
	rows, err := s.connection.Query(ctx, `
		SELECT
			`+bucketExpression+` AS bucket_started_at,
			count(), countIf(NOT s.active), countIf(NOT s.active AND s.status = 'ok'),
			countIf(NOT s.active AND s.status = 'error'), countIf(s.active),
			countIf(NOT s.active AND s.attempt_count > 1),
			countIf(isNotNull(s.total_tokens)), countIf(s.cost_known AND isNotNull(s.cost_amount)),
			sumIf(assumeNotNull(s.input_tokens), isNotNull(s.input_tokens)),
			sumIf(assumeNotNull(s.output_tokens), isNotNull(s.output_tokens)),
			sumIf(assumeNotNull(s.total_tokens), isNotNull(s.total_tokens)),
			max(s.last_observed_unix_nano),
			if(countIf(NOT s.active AND isNotNull(s.duration_nanoseconds)) = 0, CAST(NULL, 'Nullable(Int64)'), toNullable(quantileExactIf(0.50)(assumeNotNull(s.duration_nanoseconds), NOT s.active AND isNotNull(s.duration_nanoseconds)))),
			if(countIf(NOT s.active AND isNotNull(s.duration_nanoseconds)) = 0, CAST(NULL, 'Nullable(Int64)'), toNullable(quantileExactIf(0.95)(assumeNotNull(s.duration_nanoseconds), NOT s.active AND isNotNull(s.duration_nanoseconds)))),
			if(countIf(NOT s.active AND isNotNull(s.duration_nanoseconds)) = 0, CAST(NULL, 'Nullable(Int64)'), toNullable(quantileExactIf(0.99)(assumeNotNull(s.duration_nanoseconds), NOT s.active AND isNotNull(s.duration_nanoseconds))))
		FROM obs_trace_summaries AS s FINAL
		`+where+`
		GROUP BY bucket_started_at
		ORDER BY bucket_started_at
	`, args...)
	if err != nil {
		return TraceAnalyticsTimeseriesResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics timeseries: %w", err)
	}
	defer rows.Close()
	result := TraceAnalyticsTimeseriesResult{TraceAnalyticsMetadata: traceAnalyticsMetadata(now, query), Data: make([]TraceAnalyticsTimeseriesPoint, 0)}
	pointIndexes := make(map[int64]int)
	var newestObserved int64
	for rows.Next() {
		var point TraceAnalyticsTimeseriesPoint
		var runCount, completed, succeeded, failed, active, retried, tokenSamples, costSamples uint64
		var inputTokens, outputTokens, totalTokens, freshThrough int64
		if err := rows.Scan(&point.BucketStartedAt, &runCount, &completed, &succeeded, &failed, &active, &retried,
			&tokenSamples, &costSamples, &inputTokens, &outputTokens, &totalTokens, &freshThrough,
			&point.P50DurationNanoseconds, &point.P95DurationNanoseconds, &point.P99DurationNanoseconds); err != nil {
			return TraceAnalyticsTimeseriesResult{}, err
		}
		point.BucketStartedAt = point.BucketStartedAt.UTC()
		point.RunCount, point.CompletedCount = int64(runCount), int64(completed)
		point.SuccessCount, point.ErrorCount, point.ActiveCount, point.RetryCount = int64(succeeded), int64(failed), int64(active), int64(retried)
		point.Coverage = TraceAnalyticsCoverage{TotalSamples: int64(runCount), TokenSamples: int64(tokenSamples), CostSamples: int64(costSamples)}
		point.Costs = make([]AnalyticsCurrencyTotal, 0)
		if tokenSamples > 0 {
			point.InputTokens, point.OutputTokens, point.TotalTokens = int64Pointer(inputTokens), int64Pointer(outputTokens), int64Pointer(totalTokens)
		}
		result.Coverage.TotalSamples += int64(runCount)
		result.Coverage.TokenSamples += int64(tokenSamples)
		result.Coverage.CostSamples += int64(costSamples)
		if freshThrough > newestObserved {
			newestObserved = freshThrough
		}
		pointIndexes[point.BucketStartedAt.UnixNano()] = len(result.Data)
		result.Data = append(result.Data, point)
	}
	if err := rows.Err(); err != nil {
		return TraceAnalyticsTimeseriesResult{}, err
	}
	if newestObserved != 0 {
		fresh := time.Unix(0, newestObserved).UTC()
		result.FreshThrough = &fresh
	}
	costRows, err := s.connection.Query(ctx, `
		SELECT `+bucketExpression+` AS bucket_started_at, s.cost_currency, sum(assumeNotNull(s.cost_amount))
		FROM obs_trace_summaries AS s FINAL
		`+where+` AND s.cost_known AND isNotNull(s.cost_amount)
		GROUP BY bucket_started_at, s.cost_currency
		ORDER BY bucket_started_at, s.cost_currency
	`, args...)
	if err != nil {
		return TraceAnalyticsTimeseriesResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics timeseries costs: %w", err)
	}
	defer costRows.Close()
	for costRows.Next() {
		var bucket time.Time
		var cost AnalyticsCurrencyTotal
		if err := costRows.Scan(&bucket, &cost.Currency, &cost.Amount); err != nil {
			return TraceAnalyticsTimeseriesResult{}, err
		}
		if index, ok := pointIndexes[bucket.UTC().UnixNano()]; ok {
			result.Data[index].Costs = append(result.Data[index].Costs, cost)
		}
	}
	if err := costRows.Err(); err != nil {
		return TraceAnalyticsTimeseriesResult{}, err
	}
	return result, nil
}

func (s *ClickHouseTraceAnalyticsQueryStore) Latency(ctx context.Context, query TraceAnalyticsQuery) (TraceAnalyticsLatencyResult, error) {
	if s == nil || s.connection == nil {
		return TraceAnalyticsLatencyResult{}, errors.New("nil Collector ClickHouse Trace analytics Store")
	}
	now := s.now().UTC()
	query, err := NormalizeTraceAnalyticsQuery(now, TraceAnalyticsLatency, query)
	if err != nil {
		return TraceAnalyticsLatencyResult{}, err
	}
	where, args := clickHouseAnalyticsWhere(query)
	dimension := "'all'"
	arrayJoin := ""
	switch query.GroupBy {
	case AnalyticsDimensionAgent:
		dimension = "s.agent_name"
	case AnalyticsDimensionModel:
		dimension = "analytics_model"
		arrayJoin = " ARRAY JOIN s.models AS analytics_model"
	}
	args = append(args, query.Limit)
	rows, err := s.connection.Query(ctx, `
		SELECT `+dimension+` AS dimension_value, count(),
			quantileExact(0.50)(assumeNotNull(s.duration_nanoseconds)),
			quantileExact(0.95)(assumeNotNull(s.duration_nanoseconds)),
			quantileExact(0.99)(assumeNotNull(s.duration_nanoseconds)),
			max(s.last_observed_unix_nano)
		FROM obs_trace_summaries AS s FINAL`+arrayJoin+`
		`+where+` AND NOT s.active AND isNotNull(s.duration_nanoseconds)
		GROUP BY dimension_value
		ORDER BY dimension_value
		LIMIT ?
	`, args...)
	if err != nil {
		return TraceAnalyticsLatencyResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics latency: %w", err)
	}
	defer rows.Close()
	result := TraceAnalyticsLatencyResult{TraceAnalyticsMetadata: traceAnalyticsMetadata(now, query), Data: make([]TraceAnalyticsLatencyRow, 0)}
	var newestObserved int64
	for rows.Next() {
		var row TraceAnalyticsLatencyRow
		var samples uint64
		var freshThrough int64
		if err := rows.Scan(&row.Value, &samples, &row.P50DurationNanoseconds, &row.P95DurationNanoseconds, &row.P99DurationNanoseconds, &freshThrough); err != nil {
			return TraceAnalyticsLatencyResult{}, err
		}
		row.SampleCount = int64(samples)
		result.Data = append(result.Data, row)
		result.Coverage.TotalSamples += int64(samples)
		if freshThrough > newestObserved {
			newestObserved = freshThrough
		}
	}
	if err := rows.Err(); err != nil {
		return TraceAnalyticsLatencyResult{}, err
	}
	if newestObserved != 0 {
		fresh := time.Unix(0, newestObserved).UTC()
		result.FreshThrough = &fresh
	}
	return result, nil
}

func (s *ClickHouseTraceAnalyticsQueryStore) Breakdowns(ctx context.Context, query TraceAnalyticsQuery) (TraceAnalyticsBreakdownResult, error) {
	if s == nil || s.connection == nil {
		return TraceAnalyticsBreakdownResult{}, errors.New("nil Collector ClickHouse Trace analytics Store")
	}
	now := s.now().UTC()
	query, err := NormalizeTraceAnalyticsQuery(now, TraceAnalyticsBreakdowns, query)
	if err != nil {
		return TraceAnalyticsBreakdownResult{}, err
	}
	dimension, arrayJoin := clickHouseBreakdownDimension(query.GroupBy)
	where, args := clickHouseAnalyticsWhere(query)
	args = append(args, query.Limit)
	rows, err := s.connection.Query(ctx, `
		SELECT `+dimension+` AS dimension_value,
			count(), countIf(NOT s.active), countIf(NOT s.active AND s.status = 'ok'),
			countIf(NOT s.active AND s.status = 'error'), countIf(NOT s.active AND s.attempt_count > 1),
			if(countIf(NOT s.active AND isNotNull(s.duration_nanoseconds)) = 0, CAST(NULL, 'Nullable(Int64)'),
				toNullable(quantileExactIf(0.95)(assumeNotNull(s.duration_nanoseconds), NOT s.active AND isNotNull(s.duration_nanoseconds)))),
			max(s.last_observed_unix_nano)
		FROM obs_trace_summaries AS s FINAL`+arrayJoin+`
		`+where+`
		GROUP BY dimension_value
		ORDER BY count() DESC, dimension_value
		LIMIT ?
	`, args...)
	if err != nil {
		return TraceAnalyticsBreakdownResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics breakdown: %w", err)
	}
	defer rows.Close()
	result := TraceAnalyticsBreakdownResult{TraceAnalyticsMetadata: traceAnalyticsMetadata(now, query), Data: make([]TraceAnalyticsBreakdownRow, 0)}
	var newestObserved int64
	for rows.Next() {
		var row TraceAnalyticsBreakdownRow
		var runs, completed, succeeded, failed, retried uint64
		var freshThrough int64
		if err := rows.Scan(&row.Value, &runs, &completed, &succeeded, &failed, &retried, &row.P95DurationNanoseconds, &freshThrough); err != nil {
			return TraceAnalyticsBreakdownResult{}, err
		}
		row.RunCount, row.CompletedCount = int64(runs), int64(completed)
		if completed > 0 {
			row.SuccessRate = float64Pointer(float64(succeeded) / float64(completed))
			row.ErrorRate = float64Pointer(float64(failed) / float64(completed))
			row.RetryRate = float64Pointer(float64(retried) / float64(completed))
		}
		result.Data = append(result.Data, row)
		result.Coverage.TotalSamples += int64(runs)
		if freshThrough > newestObserved {
			newestObserved = freshThrough
		}
	}
	if err := rows.Err(); err != nil {
		return TraceAnalyticsBreakdownResult{}, err
	}
	if newestObserved != 0 {
		fresh := time.Unix(0, newestObserved).UTC()
		result.FreshThrough = &fresh
	}
	return result, nil
}

func (s *ClickHouseTraceAnalyticsQueryStore) Tools(ctx context.Context, query TraceAnalyticsQuery) (TraceAnalyticsToolsResult, error) {
	if s == nil || s.connection == nil {
		return TraceAnalyticsToolsResult{}, errors.New("nil Collector ClickHouse Trace analytics Store")
	}
	now := s.now().UTC()
	query, err := NormalizeTraceAnalyticsQuery(now, TraceAnalyticsTools, query)
	if err != nil {
		return TraceAnalyticsToolsResult{}, err
	}
	where, args := clickHouseAnalyticsWhere(query)
	dimension := "if(empty(a.tool_name), 'unknown', a.tool_name)"
	if query.GroupBy == AnalyticsDimensionErrorCode {
		dimension = "if(empty(a.error_code), 'unknown', a.error_code)"
	}
	args = append(args, query.Limit)
	rows, err := s.connection.Query(ctx, `
		SELECT `+dimension+` AS dimension_value,
			count(), countIf(a.status != ''), countIf(a.status = 'ok'), countIf(a.status = 'error'),
			if(countIf(a.status != '' AND isNotNull(a.duration_nanoseconds)) = 0, CAST(NULL, 'Nullable(Int64)'),
				toNullable(quantileExactIf(0.95)(assumeNotNull(a.duration_nanoseconds), a.status != '' AND isNotNull(a.duration_nanoseconds)))),
			max(s.last_observed_unix_nano)
		FROM obs_span_analytics AS a FINAL
		INNER JOIN (SELECT * FROM obs_trace_summaries FINAL) AS s ON s.trace_id = a.trace_id
		`+where+` AND a.span_kind = 'tool'
		GROUP BY dimension_value
		ORDER BY count() DESC, dimension_value
		LIMIT ?
	`, args...)
	if err != nil {
		return TraceAnalyticsToolsResult{}, fmt.Errorf("query Collector ClickHouse Trace analytics tools: %w", err)
	}
	defer rows.Close()
	result := TraceAnalyticsToolsResult{TraceAnalyticsMetadata: traceAnalyticsMetadata(now, query), Data: make([]TraceAnalyticsToolRow, 0)}
	var newestObserved int64
	for rows.Next() {
		var row TraceAnalyticsToolRow
		var calls, completed, succeeded, failed uint64
		var freshThrough int64
		if err := rows.Scan(&row.Value, &calls, &completed, &succeeded, &failed, &row.P95DurationNanoseconds, &freshThrough); err != nil {
			return TraceAnalyticsToolsResult{}, err
		}
		row.CallCount, row.CompletedCount = int64(calls), int64(completed)
		if completed > 0 {
			row.SuccessRate = float64Pointer(float64(succeeded) / float64(completed))
			row.ErrorRate = float64Pointer(float64(failed) / float64(completed))
		}
		result.Data = append(result.Data, row)
		result.Coverage.TotalSamples += int64(calls)
		if freshThrough > newestObserved {
			newestObserved = freshThrough
		}
	}
	if err := rows.Err(); err != nil {
		return TraceAnalyticsToolsResult{}, err
	}
	if newestObserved != 0 {
		fresh := time.Unix(0, newestObserved).UTC()
		result.FreshThrough = &fresh
	}
	return result, nil
}

func traceAnalyticsMetadata(now time.Time, query TraceAnalyticsQuery) TraceAnalyticsMetadata {
	return TraceAnalyticsMetadata{
		SchemaVersion: TraceAnalyticsSchemaVersion,
		GeneratedAt:   now.UTC(),
		Filters: TraceAnalyticsFilters{
			StartedAfter: time.Unix(0, query.StartedAfterUnixNano).UTC(), StartedBefore: time.Unix(0, query.StartedBeforeUnixNano).UTC(),
			AgentName: query.AgentName, ModelName: query.ModelName, Status: query.Status, WorkloadKind: query.WorkloadKind,
		},
		Bucket:  query.Bucket,
		GroupBy: query.GroupBy,
	}
}

func clickHouseAnalyticsBucketExpression(bucket AnalyticsBucket) string {
	switch bucket {
	case AnalyticsBucketFiveMinutes:
		return "toStartOfInterval(s.started_at, INTERVAL 5 MINUTE)"
	case AnalyticsBucketFifteenMinutes:
		return "toStartOfInterval(s.started_at, INTERVAL 15 MINUTE)"
	case AnalyticsBucketSixHours:
		return "toStartOfInterval(s.started_at, INTERVAL 6 HOUR)"
	case AnalyticsBucketDay:
		return "toStartOfInterval(s.started_at, INTERVAL 1 DAY)"
	default:
		return "toStartOfInterval(s.started_at, INTERVAL 1 HOUR)"
	}
}

func clickHouseBreakdownDimension(dimension AnalyticsDimension) (string, string) {
	scalar := func(column string) (string, string) {
		return "if(empty(" + column + "), 'unknown', " + column + ")", ""
	}
	array := func(column, alias string) (string, string) {
		return alias, " ARRAY JOIN if(empty(" + column + "), ['unknown'], " + column + ") AS " + alias
	}
	switch dimension {
	case AnalyticsDimensionModel:
		return array("s.models", "analytics_model")
	case AnalyticsDimensionStatus:
		return scalar("s.status")
	case AnalyticsDimensionErrorCode:
		return scalar("s.error_code")
	case AnalyticsDimensionProvider:
		return array("s.providers", "analytics_provider")
	case AnalyticsDimensionStopReason:
		return scalar("s.stop_reason")
	case AnalyticsDimensionDefinition:
		return scalar("s.agent_definition")
	case AnalyticsDimensionPrompt:
		return scalar("s.prompt_version")
	case AnalyticsDimensionConfiguration:
		return scalar("s.configuration_version")
	case AnalyticsDimensionDelegationTarget:
		return array("s.delegation_targets", "analytics_delegation_target")
	case AnalyticsDimensionDelegationOutcome:
		return array("s.delegation_outcomes", "analytics_delegation_outcome")
	case AnalyticsDimensionRAGStage:
		return array("s.rag_stages", "analytics_rag_stage")
	case AnalyticsDimensionRAGDegradation:
		return array("s.rag_degradations", "analytics_rag_degradation")
	case AnalyticsDimensionCitationOutcome:
		return array("s.citation_outcomes", "analytics_citation_outcome")
	default:
		return scalar("s.agent_name")
	}
}

func clickHouseAnalyticsWhere(query TraceAnalyticsQuery) (string, []any) {
	clauses := []string{
		"s.trace_id NOT IN (SELECT trace_id FROM obs_trace_tombstones FINAL)",
		"s.started_at_unix_nano >= ?",
		"s.started_at_unix_nano < ?",
		"s.workload_kind = ?",
	}
	args := []any{query.StartedAfterUnixNano, query.StartedBeforeUnixNano, string(query.WorkloadKind)}
	if query.AgentName != "" {
		clauses, args = append(clauses, "s.agent_name = ?"), append(args, query.AgentName)
	}
	if query.ModelName != "" {
		clauses, args = append(clauses, "has(s.models, ?)"), append(args, query.ModelName)
	}
	if query.Status != "" {
		clauses, args = append(clauses, "s.status = ?"), append(args, query.Status)
	}
	if len(query.NotebookIDs) > 0 {
		clauses, args = append(clauses, "has(?, s.notebook_id)"), append(args, query.NotebookIDs)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func int64Pointer(value int64) *int64       { return &value }
func float64Pointer(value float64) *float64 { return &value }

func NormalizeTraceAnalyticsQuery(now time.Time, kind TraceAnalyticsQueryKind, query TraceAnalyticsQuery) (TraceAnalyticsQuery, error) {
	now = now.UTC()
	if query.StartedBeforeUnixNano == 0 {
		query.StartedBeforeUnixNano = now.UnixNano()
	}
	if query.StartedAfterUnixNano == 0 {
		query.StartedAfterUnixNano = time.Unix(0, query.StartedBeforeUnixNano).UTC().Add(-24 * time.Hour).UnixNano()
	}
	if query.StartedAfterUnixNano >= query.StartedBeforeUnixNano ||
		time.Duration(query.StartedBeforeUnixNano-query.StartedAfterUnixNano) > 30*24*time.Hour {
		return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics time range is invalid")
	}
	if query.Bucket == "" {
		query.Bucket = AnalyticsBucketHour
	}
	bucketDuration, ok := analyticsBucketDuration(query.Bucket)
	if !ok || (query.StartedBeforeUnixNano-query.StartedAfterUnixNano)/bucketDuration.Nanoseconds() > 720 {
		return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics bucket is invalid for the time range")
	}
	query.AgentName = strings.TrimSpace(query.AgentName)
	query.ModelName = strings.TrimSpace(query.ModelName)
	query.Status = strings.TrimSpace(query.Status)
	if len(query.AgentName) > 160 || len(query.ModelName) > 160 || len(query.Status) > 32 {
		return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics filter bounds are invalid")
	}
	if query.WorkloadKind == "" {
		query.WorkloadKind = WorkloadAgentRun
	}
	if query.WorkloadKind != WorkloadAgentRun && query.WorkloadKind != WorkloadSourceProcessing {
		return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics workload is invalid")
	}
	if query.Limit == 0 {
		query.Limit = 10
	}
	if query.Limit < 1 || query.Limit > 50 || len(query.NotebookIDs) > 500 {
		return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics query bounds are invalid")
	}
	for _, notebookID := range query.NotebookIDs {
		if strings.TrimSpace(notebookID) == "" || len(notebookID) > 128 {
			return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics authorization scope is invalid")
		}
	}
	if !analyticsDimensionAllowed(kind, query.GroupBy) {
		return TraceAnalyticsQuery{}, errors.New("Collector Trace analytics group_by is invalid")
	}
	if kind == TraceAnalyticsBreakdowns && query.GroupBy == "" {
		query.GroupBy = AnalyticsDimensionAgent
	}
	if kind == TraceAnalyticsTools && query.GroupBy == "" {
		query.GroupBy = AnalyticsDimensionTool
	}
	return query, nil
}

func analyticsBucketDuration(bucket AnalyticsBucket) (time.Duration, bool) {
	switch bucket {
	case AnalyticsBucketFiveMinutes:
		return 5 * time.Minute, true
	case AnalyticsBucketFifteenMinutes:
		return 15 * time.Minute, true
	case AnalyticsBucketHour:
		return time.Hour, true
	case AnalyticsBucketSixHours:
		return 6 * time.Hour, true
	case AnalyticsBucketDay:
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

func analyticsDimensionAllowed(kind TraceAnalyticsQueryKind, dimension AnalyticsDimension) bool {
	if dimension == "" {
		return true
	}
	switch kind {
	case TraceAnalyticsLatency:
		return dimension == AnalyticsDimensionAgent || dimension == AnalyticsDimensionModel
	case TraceAnalyticsBreakdowns:
		switch dimension {
		case AnalyticsDimensionAgent, AnalyticsDimensionModel, AnalyticsDimensionStatus,
			AnalyticsDimensionErrorCode, AnalyticsDimensionProvider, AnalyticsDimensionStopReason,
			AnalyticsDimensionDefinition, AnalyticsDimensionPrompt, AnalyticsDimensionConfiguration,
			AnalyticsDimensionDelegationTarget, AnalyticsDimensionDelegationOutcome,
			AnalyticsDimensionRAGStage, AnalyticsDimensionRAGDegradation, AnalyticsDimensionCitationOutcome:
			return true
		}
	case TraceAnalyticsTools:
		return dimension == AnalyticsDimensionTool || dimension == AnalyticsDimensionErrorCode
	}
	return false
}
