package semconv

const Version = 1

const (
	AgentExecution  = "agent.execution"
	ModelCall       = "agent.model.call"
	AgentAction     = "agent.action"
	Retrieval       = "agent.retrieval"
	MemoryOperation = "agent.memory"
)

const (
	OperationNameKey          = "agent.operation.name"
	OperationStatusKey        = "agent.operation.status"
	DecisionOrdinalKey        = "agent.decision.ordinal"
	ErrorKindKey              = "agent.error.kind"
	DurationMillisecondsKey   = "agent.operation.duration_ms"
	ModelProviderKey          = "agent.model.provider"
	ModelNameKey              = "agent.model.name"
	ModelFinishReasonKey      = "agent.model.finish_reason"
	ModelResultKindKey        = "agent.model.result_kind"
	TokenInputKey             = "agent.token.input"
	TokenOutputKey            = "agent.token.output"
	TokenTotalKey             = "agent.token.total"
	TokenCachedKey            = "agent.token.cached"
	TokenReasoningKey         = "agent.token.reasoning"
	CostKnownKey              = "agent.cost.known"
	CostAmountKey             = "agent.cost.amount"
	CostCurrencyKey           = "agent.cost.currency"
	CostSourceKey             = "agent.cost.source"
	InstrumentationScopeKey   = "agent.instrumentation.scope"
	InstrumentationVersionKey = "agent.instrumentation.version"
)

const (
	LinkContinues   = "continues"
	LinkRetries     = "retries"
	LinkRetriedFrom = "retried_from"
)
