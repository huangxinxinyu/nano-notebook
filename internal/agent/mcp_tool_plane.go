package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/websearch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	nanoAttemptHandleMeta  = "com.nano-notebook/attempt-context"
	nanoActionIDMeta       = "com.nano-notebook/action-id"
	nanoDefinitionMeta     = "com.nano-notebook/definition"
	nanoDefinitionHashMeta = "com.nano-notebook/definition-sha256"
	nanoToolHashMeta       = "com.nano-notebook/tool-sha256"
)

var stableActionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var mcpToolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type ToolErrorKind string

const (
	ToolErrorAuthorization  ToolErrorKind = "authorization"
	ToolErrorSchema         ToolErrorKind = "schema"
	ToolErrorInvariant      ToolErrorKind = "invariant"
	ToolErrorInfrastructure ToolErrorKind = "infrastructure"
)

type ToolCallError struct {
	Kind  ToolErrorKind
	Code  string
	Cause error
}

func (e *ToolCallError) Error() string {
	if e == nil {
		return "MCP tool call failed"
	}
	if e.Code == "" {
		return "MCP tool call failed: " + string(e.Kind)
	}
	return "MCP tool call failed: " + e.Code
}

func (e *ToolCallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type MCPToolRegistration struct {
	Action          Action
	Scheduling      agentcatalog.ToolScheduling
	CrashReplaySafe bool
}

type MaterializedMCPTool struct {
	Name            string
	Description     string
	InputSchema     json.RawMessage
	Scheduling      agentcatalog.ToolScheduling
	CrashReplaySafe bool
	SHA256          string
	Action          Action
}

type MCPToolRegistry struct {
	tools  []MaterializedMCPTool
	byName map[string]MaterializedMCPTool
}

func NewMCPToolRegistry(registrations ...MCPToolRegistration) (*MCPToolRegistry, error) {
	actions := make([]Action, 0, len(registrations))
	for _, registration := range registrations {
		if registration.Action == nil ||
			(registration.Scheduling != agentcatalog.ToolOrderedSync && registration.Scheduling != agentcatalog.ToolParallel && registration.Scheduling != agentcatalog.ToolExclusiveDelegation) {
			return nil, errors.New("invalid MCP Tool registration")
		}
		if registration.Scheduling == agentcatalog.ToolOrderedSync || registration.Scheduling == agentcatalog.ToolParallel {
			actions = append(actions, registration.Action)
		}
	}
	validated, err := NewActionRegistry(actions...)
	if err != nil {
		return nil, err
	}
	registry := &MCPToolRegistry{
		tools:  make([]MaterializedMCPTool, 0, len(registrations)),
		byName: make(map[string]MaterializedMCPTool, len(registrations)),
	}
	for _, registration := range registrations {
		definition := registration.Action.Definition()
		var schema map[string]json.RawMessage
		if !mcpToolNamePattern.MatchString(definition.Name) || strings.TrimSpace(definition.Description) == "" ||
			len([]rune(strings.TrimSpace(definition.Description))) > 512 || len(definition.InputSchema) == 0 ||
			len(definition.InputSchema) > 16*1024 || json.Unmarshal(definition.InputSchema, &schema) != nil || schema == nil {
			return nil, fmt.Errorf("invalid MCP Tool definition %q", definition.Name)
		}
		if _, duplicate := registry.byName[definition.Name]; duplicate {
			return nil, fmt.Errorf("duplicate MCP Tool %q", definition.Name)
		}
		if registration.Scheduling == agentcatalog.ToolOrderedSync || registration.Scheduling == agentcatalog.ToolParallel {
			if _, ok := validated.Resolve(definition.Name); !ok {
				return nil, fmt.Errorf("MCP Tool %q was not validated", definition.Name)
			}
		} else if !strings.HasPrefix(definition.Name, "delegate.") {
			return nil, fmt.Errorf("MCP Tool %q was not validated", definition.Name)
		}
		tool := MaterializedMCPTool{
			Name: definition.Name, Description: strings.TrimSpace(definition.Description),
			InputSchema: append(json.RawMessage(nil), definition.InputSchema...),
			Scheduling:  registration.Scheduling, CrashReplaySafe: registration.CrashReplaySafe,
			Action: registration.Action,
		}
		tool.SHA256, err = materializedToolSHA256(tool)
		if err != nil {
			return nil, err
		}
		registry.tools = append(registry.tools, tool)
		registry.byName[tool.Name] = tool
	}
	sort.Slice(registry.tools, func(i, j int) bool { return registry.tools[i].Name < registry.tools[j].Name })
	return registry, nil
}

func (r *MCPToolRegistry) Resolve(name string) (MaterializedMCPTool, bool) {
	if r == nil {
		return MaterializedMCPTool{}, false
	}
	tool, ok := r.byName[name]
	tool.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	return tool, ok
}

func (r *MCPToolRegistry) Scoped(names []string) ([]MaterializedMCPTool, error) {
	if r == nil {
		return nil, errors.New("MCP Tool Registry is nil")
	}
	tools := make([]MaterializedMCPTool, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("duplicate scoped MCP Tool %q", name)
		}
		seen[name] = true
		tool, ok := r.Resolve(name)
		if !ok {
			return nil, fmt.Errorf("configured MCP Tool %q is not registered", name)
		}
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

type MCPToolAuthority interface {
	CheckAuthority(context.Context, Attempt) error
}

type AttemptToolScope struct {
	Definition       agentcatalog.Reference
	Attempt          Attempt
	UserID           string
	ChatID           string
	DefaultTimeZone  string
	RemainingActions int
	Deadline         time.Time
}

type attemptToolContext struct {
	handle           string
	definition       agentcatalog.Definition
	attempt          Attempt
	userID           string
	chatID           string
	defaultTimeZone  string
	remainingActions int
	deadline         time.Time
	tools            map[string]MaterializedMCPTool
}

type MCPToolHost struct {
	catalog   agentcatalog.Catalog
	registry  *MCPToolRegistry
	authority MCPToolAuthority
	mu        sync.RWMutex
	contexts  map[string]attemptToolContext
	metrics   *TaskMetricsRecorder
}

func NewMCPToolHost(catalog agentcatalog.Catalog, registry *MCPToolRegistry, authority MCPToolAuthority) (*MCPToolHost, error) {
	if registry == nil || authority == nil {
		return nil, errors.New("MCP Tool Host dependencies are incomplete")
	}
	return &MCPToolHost{catalog: catalog, registry: registry, authority: authority, contexts: make(map[string]attemptToolContext)}, nil
}

// WithMetrics attaches the Sprint 12 task-lifecycle metrics recorder and
// returns the same Host, chainable onto NewMCPToolHost.
func (h *MCPToolHost) WithMetrics(recorder *TaskMetricsRecorder) *MCPToolHost {
	h.metrics = recorder
	return h
}

type MCPAttemptSession struct {
	host          *MCPToolHost
	handle        string
	definition    agentcatalog.Definition
	tools         []MaterializedMCPTool
	byName        map[string]MaterializedMCPTool
	clientSession *mcp.ClientSession
	serverSession *mcp.ServerSession
	closeOnce     sync.Once
	closeErr      error
}

func (h *MCPToolHost) OpenAttempt(ctx context.Context, scope AttemptToolScope) (*MCPAttemptSession, error) {
	if h == nil || h.registry == nil || h.authority == nil || scope.Attempt.RunID == "" || scope.Attempt.JobID == "" || scope.Attempt.AttemptNo < 1 || scope.Attempt.LeaseToken == "" {
		return nil, errors.New("invalid MCP Attempt Tool scope")
	}
	if scope.RemainingActions < 1 {
		return nil, &ToolCallError{Kind: ToolErrorAuthorization, Code: "action_budget_exhausted"}
	}
	if !scope.Deadline.IsZero() && !time.Now().Before(scope.Deadline) {
		return nil, &ToolCallError{Kind: ToolErrorAuthorization, Code: "attempt_deadline_expired"}
	}
	definition, ok := h.catalog.ResolveDefinition(scope.Definition)
	if !ok {
		return nil, &ToolCallError{Kind: ToolErrorInvariant, Code: "definition_not_found"}
	}
	toolNames := append([]string(nil), definition.Tools...)
	for _, child := range definition.Children {
		name, nameErr := agentcatalog.DelegationToolName(child)
		if nameErr != nil {
			return nil, &ToolCallError{Kind: ToolErrorInvariant, Code: "tool_scope_invalid", Cause: nameErr}
		}
		toolNames = append(toolNames, name)
	}
	if loader, ok := h.authority.(interface {
		IsSubagent(context.Context, Attempt) (bool, error)
	}); ok {
		child, err := loader.IsSubagent(ctx, scope.Attempt)
		if err != nil {
			return nil, err
		}
		if child {
			inherited := toolNames[:0]
			for _, name := range toolNames {
				if name == "spawn_agent" || name == "wait_agent" || name == "list_agents" || strings.HasPrefix(name, "delegate.") {
					continue
				}
				inherited = append(inherited, name)
			}
			toolNames = inherited
		}
	}
	tools, err := h.registry.Scoped(toolNames)
	if err != nil {
		return nil, &ToolCallError{Kind: ToolErrorInvariant, Code: "tool_scope_invalid", Cause: err}
	}
	handle, err := newAttemptContextHandle()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]MaterializedMCPTool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	record := attemptToolContext{
		handle: handle, definition: definition, attempt: scope.Attempt,
		userID: scope.UserID, chatID: scope.ChatID,
		defaultTimeZone: scope.DefaultTimeZone, remainingActions: scope.RemainingActions,
		deadline: scope.Deadline, tools: byName,
	}
	h.mu.Lock()
	h.contexts[handle] = record
	h.mu.Unlock()

	server := mcp.NewServer(&mcp.Implementation{Name: "nano-tools", Version: "1"}, nil)
	for _, materialized := range tools {
		tool := materialized
		server.AddTool(&mcp.Tool{
			Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.InputSchema...),
		}, func(callContext context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.executeMCPTool(callContext, request, handle, tool)
		})
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		h.removeContext(handle)
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "nano-tool-host", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		h.removeContext(handle)
		_ = serverSession.Close()
		return nil, err
	}
	if initialized := clientSession.InitializeResult(); initialized == nil || initialized.ProtocolVersion != "2026-07-28" {
		h.removeContext(handle)
		_ = clientSession.Close()
		_ = serverSession.Close()
		return nil, errors.New("official MCP 2026-07-28 negotiation failed")
	}
	return &MCPAttemptSession{
		host: h, handle: handle, definition: definition, tools: tools, byName: byName,
		clientSession: clientSession, serverSession: serverSession,
	}, nil
}

func (s *MCPAttemptSession) ProtocolVersion() string {
	if s == nil || s.clientSession == nil || s.clientSession.InitializeResult() == nil {
		return ""
	}
	return s.clientSession.InitializeResult().ProtocolVersion
}

func (s *MCPAttemptSession) ListTools(ctx context.Context) ([]MaterializedMCPTool, error) {
	if s == nil || s.host == nil || s.clientSession == nil {
		return nil, &ToolCallError{Kind: ToolErrorInvariant, Code: "attempt_session_closed"}
	}
	record, err := s.host.resolveContext(s.handle)
	if err != nil {
		return nil, err
	}
	if err := s.host.checkContextAuthority(ctx, record); err != nil {
		return nil, err
	}
	result, err := s.clientSession.ListTools(ctx, &mcp.ListToolsParams{Meta: mcp.Meta{
		nanoAttemptHandleMeta: s.handle, nanoDefinitionMeta: s.definition.Reference().String(), nanoDefinitionHashMeta: s.definition.SHA256,
	}})
	if err != nil {
		return nil, &ToolCallError{Kind: ToolErrorInfrastructure, Code: "mcp_list_failed", Cause: err}
	}
	if len(result.Tools) != len(s.tools) {
		return nil, &ToolCallError{Kind: ToolErrorInvariant, Code: "mcp_tool_list_mismatch"}
	}
	listed := make(map[string]bool, len(result.Tools))
	for _, tool := range result.Tools {
		listed[tool.Name] = true
	}
	tools := make([]MaterializedMCPTool, 0, len(s.tools))
	for _, tool := range s.tools {
		if !listed[tool.Name] {
			return nil, &ToolCallError{Kind: ToolErrorInvariant, Code: "mcp_tool_list_mismatch"}
		}
		copy := tool
		copy.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
		copy.Action = nil
		tools = append(tools, copy)
	}
	return tools, nil
}

func (s *MCPAttemptSession) ActionDefinitions(ctx context.Context, policy ActionPolicy, tracers ...*agentobs.Tracer) ([]models.ActionDefinition, error) {
	var tracer *agentobs.Tracer
	if len(tracers) > 0 {
		tracer = tracers[0]
	}
	tools, err := s.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	definitions := make([]models.ActionDefinition, 0, len(tools))
	for _, tool := range tools {
		registered := s.byName[tool.Name]
		planMutation := false
		if mutation, ok := registered.Action.(PlanMutationPolicy); ok {
			planMutation = mutation.IsPlanMutation()
		}
		if !planMutation && policy.RemainingActions <= 0 {
			continue
		}
		if policy.Execution != nil {
			if availability, ok := registered.Action.(ActionAvailability); ok {
				available, reasonCode := availability.Available(*policy.Execution)
				if !available {
					recordActionAvailabilityFiltered(ctx, tracer, tool.Name, reasonCode)
					continue
				}
			}
		}
		definitions = append(definitions, actionDefinitionFromMaterialized(tool))
	}
	return definitions, nil
}

func (s *MCPAttemptSession) ValidateProposal(proposals []models.ActionProposal) error {
	if len(proposals) == 0 {
		return errors.New("Action proposal batch is empty")
	}
	for index, proposal := range proposals {
		tool, ok := s.byName[proposal.Name]
		if !ok {
			return fmt.Errorf("Action proposal %d names unavailable MCP Tool %q", index, proposal.Name)
		}
		if err := tool.Action.ValidateInput(proposal.Input); err != nil {
			return fmt.Errorf("Action proposal %d input is invalid: %w", index, err)
		}
		if tool.Scheduling == agentcatalog.ToolExclusiveDelegation && len(proposals) != 1 {
			return errors.New("exclusive delegation must be the only Action proposal")
		}
	}
	return nil
}

func (s *MCPAttemptSession) actionAdapter(name string) (Action, bool) {
	tool, ok := s.byName[name]
	if !ok {
		return nil, false
	}
	return mcpSessionAction{session: s, tool: tool}, true
}

type mcpSessionAction struct {
	session *MCPAttemptSession
	tool    MaterializedMCPTool
}

func (a mcpSessionAction) Definition() models.ActionDefinition {
	return actionDefinitionFromMaterialized(a.tool)
}

func (a mcpSessionAction) ValidateInput(input json.RawMessage) error {
	return a.tool.Action.ValidateInput(input)
}

func (a mcpSessionAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	return a.session.CallTool(ctx, a.tool.Name, request.Input, request.ActionID)
}

func (a mcpSessionAction) CacheLongToolResults(definition agentcatalog.Reference) bool {
	policy, ok := a.tool.Action.(ToolResultCacheEligibility)
	return ok && policy.CacheLongToolResults(definition)
}

func (s *MCPAttemptSession) CallTool(ctx context.Context, name string, input json.RawMessage, actionID string) (toolResult ActionResult, callErr error) {
	if s == nil || s.host == nil || s.clientSession == nil {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorInvariant, Code: "attempt_session_closed"}
	}
	startedAt := time.Now()
	defer func() {
		if s.host.metrics == nil {
			return
		}
		outcome := "completed"
		if callErr != nil {
			outcome = "failed"
		}
		s.host.metrics.RecordToolExecution(name, outcome, time.Since(startedAt))
	}()
	tool, allowed := s.byName[name]
	if !allowed {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorAuthorization, Code: "tool_not_allowed"}
	}
	if !stableActionIDPattern.MatchString(actionID) {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorInvariant, Code: "action_id_invalid"}
	}
	if len(input) == 0 || !json.Valid(input) {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorSchema, Code: "tool_input_invalid"}
	}
	result, err := s.clientSession.CallTool(ctx, &mcp.CallToolParams{
		Meta: mcp.Meta{
			nanoAttemptHandleMeta: s.handle, nanoActionIDMeta: actionID,
			nanoDefinitionMeta: s.definition.Reference().String(), nanoDefinitionHashMeta: s.definition.SHA256,
			nanoToolHashMeta: tool.SHA256,
		},
		Name: name, Arguments: json.RawMessage(input),
	})
	if err != nil {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorInfrastructure, Code: "mcp_call_failed", Cause: err}
	}
	envelope, err := decodeMCPToolEnvelope(result.StructuredContent)
	if err != nil {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorInvariant, Code: "mcp_result_invalid", Cause: err}
	}
	if result.IsError {
		cause := mcpToolErrorCause(envelope.ErrorCode)
		return ActionResult{}, &ToolCallError{Kind: envelope.ErrorKind, Code: envelope.ErrorCode, Cause: cause}
	}
	actionResult := ActionResult{Status: envelope.Status, Output: envelope.Output, ErrorCode: envelope.ErrorCode, Error: envelope.Error, traceAttributes: envelope.Attributes}
	if err := actionResult.Validate(); err != nil {
		return ActionResult{}, &ToolCallError{Kind: ToolErrorInvariant, Code: "action_result_invalid", Cause: err}
	}
	return actionResult, nil
}

func (s *MCPAttemptSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.host != nil {
			s.host.removeContext(s.handle)
		}
		if s.clientSession != nil {
			s.closeErr = s.clientSession.Close()
			s.clientSession = nil
		}
		if s.serverSession != nil {
			if err := s.serverSession.Close(); s.closeErr == nil {
				s.closeErr = err
			}
			s.serverSession = nil
		}
	})
	return s.closeErr
}

type mcpToolEnvelope struct {
	Status     ActionResultStatus   `json:"status,omitempty"`
	Output     json.RawMessage      `json:"output,omitempty"`
	ErrorKind  ToolErrorKind        `json:"error_kind,omitempty"`
	ErrorCode  string               `json:"error_code,omitempty"`
	Error      *ActionError         `json:"error,omitempty"`
	Attributes []agentobs.Attribute `json:"attributes,omitempty"`
}

func (h *MCPToolHost) executeMCPTool(ctx context.Context, request *mcp.CallToolRequest, expectedHandle string, expectedTool MaterializedMCPTool) (*mcp.CallToolResult, error) {
	if request == nil || request.Params == nil || request.ProtocolVersion() != "2026-07-28" {
		return mcpToolErrorResult(ToolErrorInvariant, "mcp_request_invalid"), nil
	}
	meta := request.Params.Meta
	handle, _ := meta[nanoAttemptHandleMeta].(string)
	actionID, _ := meta[nanoActionIDMeta].(string)
	definitionRef, _ := meta[nanoDefinitionMeta].(string)
	definitionHash, _ := meta[nanoDefinitionHashMeta].(string)
	toolHash, _ := meta[nanoToolHashMeta].(string)
	if handle == "" || handle != expectedHandle || !stableActionIDPattern.MatchString(actionID) {
		return mcpToolErrorResult(ToolErrorInvariant, "attempt_context_invalid"), nil
	}
	record, err := h.resolveContext(handle)
	if err != nil {
		return mcpToolErrorResult(ToolErrorAuthorization, "attempt_context_expired"), nil
	}
	tool, allowed := record.tools[request.Params.Name]
	if !allowed || tool.Name != expectedTool.Name {
		return mcpToolErrorResult(ToolErrorAuthorization, "tool_not_allowed"), nil
	}
	if definitionRef != record.definition.Reference().String() || definitionHash != record.definition.SHA256 || toolHash != tool.SHA256 {
		return mcpToolErrorResult(ToolErrorInvariant, "materialized_tool_mismatch"), nil
	}
	if err := h.checkContextAuthority(ctx, record); err != nil {
		return mcpToolErrorResult(ToolErrorAuthorization, "attempt_authority_lost"), nil
	}
	input := append(json.RawMessage(nil), request.Params.Arguments...)
	if err := tool.Action.ValidateInput(input); err != nil {
		return mcpToolErrorResult(ToolErrorSchema, "tool_input_invalid"), nil
	}
	result, err := tool.Action.Execute(ctx, ActionRequest{
		ActionID: actionID, Input: input, UserID: record.userID, ChatID: record.chatID,
		DefaultTimeZone: record.defaultTimeZone, Attempt: record.attempt,
		Definition: record.definition.Reference(), DefinitionSHA256: record.definition.SHA256,
	})
	if err != nil {
		kind, code := classifyMCPToolExecutionError(err)
		return mcpToolErrorResult(kind, code), nil
	}
	if err := result.Validate(); err != nil {
		return mcpToolErrorResult(ToolErrorInvariant, "action_result_invalid"), nil
	}
	envelope := mcpToolEnvelope{Status: result.Status, Output: result.Output, ErrorCode: result.ErrorCode, Error: result.Error, Attributes: result.traceAttributes}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(result.Status)}}, StructuredContent: envelope,
	}, nil
}

func classifyMCPToolExecutionError(err error) (ToolErrorKind, string) {
	var toolErr *ToolCallError
	if errors.As(err, &toolErr) && toolErr.Kind != "" && actionCodePattern.MatchString(toolErr.Code) {
		return toolErr.Kind, toolErr.Code
	}
	switch {
	case errors.Is(err, ErrLeaseLost), errors.Is(err, ErrRunDeadlineExceeded), errors.Is(err, context.Canceled):
		return ToolErrorAuthorization, "attempt_authority_lost"
	case errors.Is(err, websearch.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return ToolErrorInfrastructure, "discovery_timeout"
	case errors.Is(err, websearch.ErrRateLimited):
		return ToolErrorInfrastructure, "discovery_rate_limited"
	case errors.Is(err, websearch.ErrUnavailable):
		return ToolErrorInfrastructure, "discovery_unavailable"
	case errors.Is(err, websearch.ErrNotConfigured):
		return ToolErrorInvariant, "discovery_not_configured"
	case errors.Is(err, websearch.ErrInvalidQuery):
		return ToolErrorSchema, "discovery_invalid_query"
	case errors.Is(err, websearch.ErrInvalidResponse):
		return ToolErrorInvariant, "discovery_invalid_response"
	default:
		return ToolErrorInfrastructure, "tool_execution_failed"
	}
}

func mcpToolErrorCause(code string) error {
	switch code {
	case "attempt_authority_lost":
		return ErrLeaseLost
	case "discovery_timeout":
		return websearch.ErrTimeout
	case "discovery_rate_limited":
		return websearch.ErrRateLimited
	case "discovery_unavailable":
		return websearch.ErrUnavailable
	case "discovery_not_configured":
		return websearch.ErrNotConfigured
	case "discovery_invalid_query":
		return websearch.ErrInvalidQuery
	case "discovery_invalid_response":
		return websearch.ErrInvalidResponse
	default:
		return nil
	}
}

func (h *MCPToolHost) resolveContext(handle string) (attemptToolContext, error) {
	h.mu.RLock()
	record, ok := h.contexts[handle]
	h.mu.RUnlock()
	if !ok {
		return attemptToolContext{}, &ToolCallError{Kind: ToolErrorAuthorization, Code: "attempt_context_expired"}
	}
	return record, nil
}

func (h *MCPToolHost) checkContextAuthority(ctx context.Context, record attemptToolContext) error {
	if !record.deadline.IsZero() && !time.Now().Before(record.deadline) {
		return &ToolCallError{Kind: ToolErrorAuthorization, Code: "attempt_deadline_expired"}
	}
	if record.remainingActions < 1 {
		return &ToolCallError{Kind: ToolErrorAuthorization, Code: "action_budget_exhausted"}
	}
	if err := h.authority.CheckAuthority(ctx, record.attempt); err != nil {
		return &ToolCallError{Kind: ToolErrorAuthorization, Code: "attempt_authority_lost", Cause: err}
	}
	return nil
}

func (h *MCPToolHost) removeContext(handle string) {
	h.mu.Lock()
	delete(h.contexts, handle)
	h.mu.Unlock()
}

func mcpToolErrorResult(kind ToolErrorKind, code string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: code}}, IsError: true,
		StructuredContent: mcpToolEnvelope{ErrorKind: kind, ErrorCode: code},
	}
}

func decodeMCPToolEnvelope(value any) (mcpToolEnvelope, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return mcpToolEnvelope{}, err
	}
	var envelope mcpToolEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return mcpToolEnvelope{}, err
	}
	if envelope.ErrorKind != "" {
		switch envelope.ErrorKind {
		case ToolErrorAuthorization, ToolErrorSchema, ToolErrorInvariant, ToolErrorInfrastructure:
		default:
			return mcpToolEnvelope{}, errors.New("unknown MCP Tool error kind")
		}
		if !actionCodePattern.MatchString(envelope.ErrorCode) {
			return mcpToolEnvelope{}, errors.New("invalid MCP Tool error code")
		}
	}
	return envelope, nil
}

func materializedToolSHA256(tool MaterializedMCPTool) (string, error) {
	canonical := struct {
		Name            string                      `json:"name"`
		Description     string                      `json:"description"`
		InputSchema     json.RawMessage             `json:"input_schema"`
		Scheduling      agentcatalog.ToolScheduling `json:"scheduling"`
		CrashReplaySafe bool                        `json:"crash_replay_safe"`
	}{tool.Name, tool.Description, tool.InputSchema, tool.Scheduling, tool.CrashReplaySafe}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func newAttemptContextHandle() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func actionDefinitionFromMaterialized(tool MaterializedMCPTool) models.ActionDefinition {
	return models.ActionDefinition{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.InputSchema...)}
}
