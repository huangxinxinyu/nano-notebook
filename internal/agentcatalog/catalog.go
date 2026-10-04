package agentcatalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const maxMCPToolNameLength = 128

//go:embed definitions/*.json archive/definitions/*.json model-policies/*.json provider-capabilities/*.json model-context-policies/*.json contracts/*.schema.json releases/*.json
var embeddedFiles embed.FS

type Reference struct {
	Identity string
	Version  int
}

func ParseReference(value string) (Reference, error) {
	identity, rawVersion, found := strings.Cut(value, "@")
	if !found || strings.Contains(rawVersion, "@") || !validIdentifier(identity) {
		return Reference{}, fmt.Errorf("invalid immutable reference %q", value)
	}
	if rawVersion == "" || (len(rawVersion) > 1 && rawVersion[0] == '0') {
		return Reference{}, fmt.Errorf("invalid immutable reference %q", value)
	}
	version, err := strconv.Atoi(rawVersion)
	if err != nil || version < 1 || strconv.Itoa(version) != rawVersion {
		return Reference{}, fmt.Errorf("invalid immutable reference %q", value)
	}
	return Reference{Identity: identity, Version: version}, nil
}

func MustParseReference(value string) Reference {
	reference, err := ParseReference(value)
	if err != nil {
		panic(err)
	}
	return reference
}

func (r Reference) String() string {
	return r.Identity + "@" + strconv.Itoa(r.Version)
}

func (r Reference) MarshalJSON() ([]byte, error) {
	if err := validateReference(r); err != nil {
		return nil, err
	}
	return json.Marshal(r.String())
}

func (r *Reference) UnmarshalJSON(payload []byte) error {
	var value string
	if err := json.Unmarshal(payload, &value); err != nil {
		return errors.New("immutable reference must be a string")
	}
	reference, err := ParseReference(value)
	if err != nil {
		return err
	}
	*r = reference
	return nil
}

type Limits struct {
	ModelCalls      int `json:"model_calls"`
	ActionDecisions int `json:"action_decisions,omitempty"`
	Actions         int `json:"actions"`
	// LegacyPlanMutations preserves immutable historical Definition payloads.
	// Runtime tool availability and execution budgets intentionally ignore it.
	LegacyPlanMutations int `json:"plan_mutations,omitempty"`
	ActionBatch         int `json:"action_batch"`
	ContextBytes        int `json:"context_bytes"`
	ResultBytes         int `json:"result_bytes"`
	Attempts            int `json:"attempts"`
}

type ContractBindings struct {
	Input  Reference `json:"input"`
	Result Reference `json:"result"`
}

type DelegationMetadata struct {
	Description string `json:"description"`
}

type Definition struct {
	Identity    string               `json:"identity"`
	Version     int                  `json:"version"`
	Executor    string               `json:"executor"`
	ModelPolicy Reference            `json:"model_policy"`
	Prompts     map[string]Reference `json:"prompts"`
	Contracts   ContractBindings     `json:"contracts"`
	Skills      []Reference          `json:"skills,omitempty"`
	Tools       []string             `json:"tools"`
	Children    []Reference          `json:"children"`
	Limits      Limits               `json:"limits"`
	Delegation  *DelegationMetadata  `json:"delegation,omitempty"`
	SHA256      string               `json:"-"`
	SourcePath  string               `json:"-"`
	Archived    bool                 `json:"-"`
}

func (d Definition) Reference() Reference {
	return Reference{Identity: d.Identity, Version: d.Version}
}

type ModelPolicy struct {
	Identity        string  `json:"identity"`
	Version         int     `json:"version"`
	ProviderModel   string  `json:"provider_model"`
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	TimeoutMS       int     `json:"timeout_ms"`
	EnableThinking  *bool   `json:"enable_thinking,omitempty"`
	SHA256          string  `json:"-"`
	SourcePath      string  `json:"-"`
}

func (p ModelPolicy) Reference() Reference {
	return Reference{Identity: p.Identity, Version: p.Version}
}

func (p ModelPolicy) ThinkingEnabled() bool {
	return p.EnableThinking != nil && *p.EnableThinking
}

type ProviderModelCapability struct {
	Identity            string `json:"identity"`
	Version             int    `json:"version"`
	ProviderModel       string `json:"provider_model"`
	ResolvedModel       string `json:"resolved_model"`
	ContextWindowTokens int    `json:"context_window_tokens"`
	MaxInputTokens      int    `json:"max_input_tokens"`
	MaxOutputTokens     int    `json:"max_output_tokens"`
	TokenizerIdentity   string `json:"tokenizer_identity"`
	TokenizerVersion    string `json:"tokenizer_version"`
	InvocationMode      string `json:"invocation_mode"`
	SHA256              string `json:"-"`
	SourcePath          string `json:"-"`
}

func (c ProviderModelCapability) Reference() Reference {
	return Reference{Identity: c.Identity, Version: c.Version}
}

type ModelContextPolicy struct {
	Identity               string    `json:"identity"`
	Version                int       `json:"version"`
	InvocationModelPolicy  Reference `json:"invocation_model_policy"`
	ProviderCapability     Reference `json:"provider_capability"`
	PinnedMaxOutputTokens  int       `json:"pinned_max_output_tokens"`
	SoftInputLimitTokens   int       `json:"soft_input_limit_tokens"`
	EstimationSafetyTokens int       `json:"estimation_safety_tokens"`
	KeepRecentTokens       int       `json:"keep_recent_tokens"`
	SummaryMaxOutputTokens int       `json:"summary_max_output_tokens"`
	OverflowRetryLimit     int       `json:"overflow_retry_limit"`
	SHA256                 string    `json:"-"`
	SourcePath             string    `json:"-"`
}

func (p ModelContextPolicy) Reference() Reference {
	return Reference{Identity: p.Identity, Version: p.Version}
}

type ModelContextBudgets struct {
	HardInputTokens         int
	SafeInputTokens         int
	CompactionTriggerTokens int
}

type ResolvedModelContextPolicy struct {
	Policy     ModelContextPolicy
	Capability ProviderModelCapability
	Budgets    ModelContextBudgets
}

type ContractVersion struct {
	Identity   string          `json:"identity"`
	Version    int             `json:"version"`
	Schema     json.RawMessage `json:"schema"`
	SHA256     string          `json:"-"`
	SourcePath string          `json:"-"`
}

func (c ContractVersion) Reference() Reference {
	return Reference{Identity: c.Identity, Version: c.Version}
}

type ReleaseManifest struct {
	Identity   string               `json:"identity"`
	Version    int                  `json:"version"`
	Roots      map[string]Reference `json:"roots"`
	SHA256     string               `json:"-"`
	SourcePath string               `json:"-"`
}

func (m ReleaseManifest) Reference() Reference {
	return Reference{Identity: m.Identity, Version: m.Version}
}

type Catalog struct {
	definitions          map[Reference]Definition
	modelPolicies        map[Reference]ModelPolicy
	providerCapabilities map[Reference]ProviderModelCapability
	modelContextPolicies map[Reference]ModelContextPolicy
	contextByInvocation  map[Reference]Reference
	contracts            map[Reference]ContractVersion
	releases             map[Reference]ReleaseManifest
}

func LoadEmbedded() (Catalog, error) {
	return LoadFS(embeddedFiles)
}

func MustLoadEmbedded() Catalog {
	catalog, err := LoadEmbedded()
	if err != nil {
		panic(err)
	}
	return catalog
}

func LoadFS(source fs.FS) (Catalog, error) {
	catalog := Catalog{
		definitions:          make(map[Reference]Definition),
		modelPolicies:        make(map[Reference]ModelPolicy),
		providerCapabilities: make(map[Reference]ProviderModelCapability),
		modelContextPolicies: make(map[Reference]ModelContextPolicy),
		contextByInvocation:  make(map[Reference]Reference),
		contracts:            make(map[Reference]ContractVersion),
		releases:             make(map[Reference]ReleaseManifest),
	}
	for _, pattern := range []string{"definitions/*.json", "archive/definitions/*.json"} {
		if err := loadKind(source, pattern, func(filePath string, payload []byte) error {
			var value Definition
			if err := decodeStrict(payload, &value); err != nil {
				return fmt.Errorf("definition %s: %w", filePath, err)
			}
			return catalog.addDefinition(filePath, value)
		}); err != nil {
			return Catalog{}, err
		}
	}
	if err := loadKind(source, "model-policies/*.json", func(filePath string, payload []byte) error {
		var value ModelPolicy
		if err := decodeStrict(payload, &value); err != nil {
			return fmt.Errorf("model policy %s: %w", filePath, err)
		}
		return catalog.addModelPolicy(filePath, value)
	}); err != nil {
		return Catalog{}, err
	}
	if err := loadKind(source, "provider-capabilities/*.json", func(filePath string, payload []byte) error {
		var value ProviderModelCapability
		if err := decodeStrict(payload, &value); err != nil {
			return fmt.Errorf("provider capability %s: %w", filePath, err)
		}
		return catalog.addProviderCapability(filePath, value)
	}); err != nil {
		return Catalog{}, err
	}
	if err := loadKind(source, "model-context-policies/*.json", func(filePath string, payload []byte) error {
		var value ModelContextPolicy
		if err := decodeStrict(payload, &value); err != nil {
			return fmt.Errorf("model context policy %s: %w", filePath, err)
		}
		return catalog.addModelContextPolicy(filePath, value)
	}); err != nil {
		return Catalog{}, err
	}
	if err := loadKind(source, "contracts/*.schema.json", func(filePath string, payload []byte) error {
		var value ContractVersion
		if err := decodeStrict(payload, &value); err != nil {
			return fmt.Errorf("contract %s: %w", filePath, err)
		}
		return catalog.addContract(filePath, value)
	}); err != nil {
		return Catalog{}, err
	}
	if err := loadKind(source, "releases/*.json", func(filePath string, payload []byte) error {
		var value ReleaseManifest
		if err := decodeStrict(payload, &value); err != nil {
			return fmt.Errorf("release %s: %w", filePath, err)
		}
		return catalog.addRelease(filePath, value)
	}); err != nil {
		return Catalog{}, err
	}
	if len(catalog.definitions) == 0 || len(catalog.releases) == 0 {
		return Catalog{}, errors.New("agent catalog is incomplete")
	}
	if err := catalog.validateReferences(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (c Catalog) Definitions() []Definition {
	values := make([]Definition, 0, len(c.definitions))
	for _, value := range c.definitions {
		values = append(values, cloneDefinition(value))
	}
	sort.Slice(values, func(i, j int) bool { return lessReference(values[i].Reference(), values[j].Reference()) })
	return values
}

func (c Catalog) ModelPolicies() []ModelPolicy {
	values := make([]ModelPolicy, 0, len(c.modelPolicies))
	for _, value := range c.modelPolicies {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return lessReference(values[i].Reference(), values[j].Reference()) })
	return values
}

func (c Catalog) ProviderCapabilities() []ProviderModelCapability {
	values := make([]ProviderModelCapability, 0, len(c.providerCapabilities))
	for _, value := range c.providerCapabilities {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return lessReference(values[i].Reference(), values[j].Reference()) })
	return values
}

func (c Catalog) ModelContextPolicies() []ModelContextPolicy {
	values := make([]ModelContextPolicy, 0, len(c.modelContextPolicies))
	for _, value := range c.modelContextPolicies {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return lessReference(values[i].Reference(), values[j].Reference()) })
	return values
}

func (c Catalog) Contracts() []ContractVersion {
	values := make([]ContractVersion, 0, len(c.contracts))
	for _, value := range c.contracts {
		value.Schema = append(json.RawMessage(nil), value.Schema...)
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return lessReference(values[i].Reference(), values[j].Reference()) })
	return values
}

func (c Catalog) Releases() []ReleaseManifest {
	values := make([]ReleaseManifest, 0, len(c.releases))
	for _, value := range c.releases {
		value.Roots = cloneMap(value.Roots)
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return lessReference(values[i].Reference(), values[j].Reference()) })
	return values
}

func (c Catalog) ResolveDefinition(reference Reference) (Definition, bool) {
	value, ok := c.definitions[reference]
	return cloneDefinition(value), ok
}

func (c Catalog) ResolveModelPolicy(reference Reference) (ModelPolicy, bool) {
	value, ok := c.modelPolicies[reference]
	return value, ok
}

func (c Catalog) ResolveProviderCapability(reference Reference) (ProviderModelCapability, bool) {
	value, ok := c.providerCapabilities[reference]
	return value, ok
}

func (c Catalog) ResolveModelContextPolicy(invocationPolicy Reference) (ResolvedModelContextPolicy, error) {
	contextReference, ok := c.contextByInvocation[invocationPolicy]
	if !ok {
		return ResolvedModelContextPolicy{}, fmt.Errorf("Model Policy %s has no Model Context Policy", invocationPolicy)
	}
	policy := c.modelContextPolicies[contextReference]
	capability, ok := c.providerCapabilities[policy.ProviderCapability]
	if !ok {
		return ResolvedModelContextPolicy{}, fmt.Errorf("Model Context Policy %s has no Provider Capability", contextReference)
	}
	budgets, err := deriveModelContextBudgets(policy, capability)
	if err != nil {
		return ResolvedModelContextPolicy{}, err
	}
	return ResolvedModelContextPolicy{Policy: policy, Capability: capability, Budgets: budgets}, nil
}

func (c Catalog) ResolveContract(reference Reference) (ContractVersion, bool) {
	value, ok := c.contracts[reference]
	value.Schema = append(json.RawMessage(nil), value.Schema...)
	return value, ok
}

func (c Catalog) ResolveRelease(reference Reference) (ReleaseManifest, bool) {
	value, ok := c.releases[reference]
	value.Roots = cloneMap(value.Roots)
	return value, ok
}

func loadKind(source fs.FS, pattern string, add func(string, []byte) error) error {
	paths, err := fs.Glob(source, pattern)
	if err != nil {
		return err
	}
	for _, filePath := range paths {
		payload, err := fs.ReadFile(source, filePath)
		if err != nil {
			return err
		}
		if err := add(filePath, payload); err != nil {
			return err
		}
	}
	return nil
}

func decodeStrict(payload []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func (c *Catalog) addDefinition(filePath string, value Definition) error {
	if err := validateDefinition(value); err != nil {
		return fmt.Errorf("definition %s: %w", filePath, err)
	}
	if err := validatePath(filePath, value.Reference(), ".json"); err != nil {
		return err
	}
	value.SourcePath = strings.TrimPrefix(filePath, "archive/")
	value.Archived = strings.HasPrefix(filePath, "archive/")
	value.SHA256 = canonicalHash(value)
	return addUnique(c.definitions, value.Reference(), value, "definition")
}

func (c *Catalog) addModelPolicy(filePath string, value ModelPolicy) error {
	if !validIdentifier(value.Identity) || value.Version < 1 || strings.TrimSpace(value.ProviderModel) == "" || value.MaxOutputTokens < 1 || value.TimeoutMS < 1 || value.Temperature < 0 {
		return fmt.Errorf("model policy %s is invalid", filePath)
	}
	if err := validatePath(filePath, value.Reference(), ".json"); err != nil {
		return err
	}
	value.SourcePath = filePath
	value.SHA256 = canonicalHash(value)
	return addUnique(c.modelPolicies, value.Reference(), value, "model policy")
}

func (c *Catalog) addProviderCapability(filePath string, value ProviderModelCapability) error {
	if !validIdentifier(value.Identity) || value.Version < 1 || strings.TrimSpace(value.ProviderModel) == "" ||
		strings.TrimSpace(value.ResolvedModel) == "" || value.ContextWindowTokens < 1 || value.MaxInputTokens < 1 ||
		value.MaxOutputTokens < 1 || value.MaxInputTokens > value.ContextWindowTokens ||
		strings.TrimSpace(value.TokenizerIdentity) == "" || strings.TrimSpace(value.TokenizerVersion) == "" ||
		strings.TrimSpace(value.InvocationMode) == "" {
		return fmt.Errorf("provider capability %s is invalid", filePath)
	}
	if err := validatePath(filePath, value.Reference(), ".json"); err != nil {
		return err
	}
	value.SourcePath = filePath
	value.SHA256 = canonicalHash(value)
	return addUnique(c.providerCapabilities, value.Reference(), value, "provider capability")
}

func (c *Catalog) addModelContextPolicy(filePath string, value ModelContextPolicy) error {
	if !validIdentifier(value.Identity) || value.Version < 1 || validateReference(value.InvocationModelPolicy) != nil ||
		validateReference(value.ProviderCapability) != nil || value.PinnedMaxOutputTokens < 1 ||
		value.SoftInputLimitTokens < 1 || value.EstimationSafetyTokens < 1 || value.KeepRecentTokens < 1 ||
		value.SummaryMaxOutputTokens < 1 || value.OverflowRetryLimit < 1 {
		return fmt.Errorf("model context policy %s is invalid", filePath)
	}
	if err := validatePath(filePath, value.Reference(), ".json"); err != nil {
		return err
	}
	value.SourcePath = filePath
	value.SHA256 = canonicalHash(value)
	return addUnique(c.modelContextPolicies, value.Reference(), value, "model context policy")
}

func (c *Catalog) addContract(filePath string, value ContractVersion) error {
	if !validIdentifier(value.Identity) || value.Version < 1 || len(value.Schema) == 0 || !json.Valid(value.Schema) {
		return fmt.Errorf("contract %s is invalid", filePath)
	}
	var schema map[string]any
	if err := json.Unmarshal(value.Schema, &schema); err != nil || schema == nil {
		return fmt.Errorf("contract %s schema must be an object", filePath)
	}
	if err := validatePath(filePath, value.Reference(), ".schema.json"); err != nil {
		return err
	}
	value.SourcePath = filePath
	value.SHA256 = canonicalHash(value)
	return addUnique(c.contracts, value.Reference(), value, "contract")
}

func (c *Catalog) addRelease(filePath string, value ReleaseManifest) error {
	if !validIdentifier(value.Identity) || value.Version < 1 || len(value.Roots) == 0 {
		return fmt.Errorf("release %s is invalid", filePath)
	}
	for purpose, reference := range value.Roots {
		if !validName(purpose) || validateReference(reference) != nil {
			return fmt.Errorf("release %s has invalid root", filePath)
		}
	}
	if err := validatePath(filePath, value.Reference(), ".json"); err != nil {
		return err
	}
	value.SourcePath = filePath
	value.SHA256 = canonicalHash(value)
	return addUnique(c.releases, value.Reference(), value, "release")
}

func (c Catalog) validateReferences() error {
	for reference, policy := range c.modelContextPolicies {
		invocation, ok := c.modelPolicies[policy.InvocationModelPolicy]
		if !ok {
			return fmt.Errorf("model context policy %s references missing Model Policy %s", reference, policy.InvocationModelPolicy)
		}
		capability, ok := c.providerCapabilities[policy.ProviderCapability]
		if !ok {
			return fmt.Errorf("model context policy %s references missing Provider Capability %s", reference, policy.ProviderCapability)
		}
		if invocation.ProviderModel != capability.ProviderModel || invocation.MaxOutputTokens != policy.PinnedMaxOutputTokens {
			return fmt.Errorf("model context policy %s contradicts invocation model or output limit", reference)
		}
		if invocation.ThinkingEnabled() != (capability.InvocationMode == "thinking") {
			return fmt.Errorf("model context policy %s contradicts invocation mode", reference)
		}
		if _, err := deriveModelContextBudgets(policy, capability); err != nil {
			return fmt.Errorf("model context policy %s: %w", reference, err)
		}
		if prior, duplicate := c.contextByInvocation[policy.InvocationModelPolicy]; duplicate {
			return fmt.Errorf("Model Policy %s has multiple Context Policies %s and %s", policy.InvocationModelPolicy, prior, reference)
		}
		c.contextByInvocation[policy.InvocationModelPolicy] = reference
	}
	for reference := range c.modelPolicies {
		if _, ok := c.contextByInvocation[reference]; !ok {
			return fmt.Errorf("Model Policy %s has no Model Context Policy", reference)
		}
	}
	for reference, definition := range c.definitions {
		if _, ok := c.modelPolicies[definition.ModelPolicy]; !ok {
			return fmt.Errorf("definition %s references missing model policy %s", reference, definition.ModelPolicy)
		}
		for _, contract := range []Reference{definition.Contracts.Input, definition.Contracts.Result} {
			if _, ok := c.contracts[contract]; !ok {
				return fmt.Errorf("definition %s references missing contract %s", reference, contract)
			}
		}
		for _, child := range definition.Children {
			childDefinition, ok := c.definitions[child]
			if !ok {
				return fmt.Errorf("definition %s references missing child %s", reference, child)
			}
			if childDefinition.Delegation == nil || strings.TrimSpace(childDefinition.Delegation.Description) == "" {
				return fmt.Errorf("child definition %s has no delegation metadata", child)
			}
		}
	}
	for reference := range c.definitions {
		if err := c.validateTopology(reference, nil, 0); err != nil {
			return err
		}
	}
	for release, manifest := range c.releases {
		for purpose, root := range manifest.Roots {
			if _, ok := c.definitions[root]; !ok {
				return fmt.Errorf("release %s root %s references missing definition %s", release, purpose, root)
			}
		}
	}
	return nil
}

func deriveModelContextBudgets(policy ModelContextPolicy, capability ProviderModelCapability) (ModelContextBudgets, error) {
	if policy.PinnedMaxOutputTokens > capability.MaxOutputTokens || policy.SummaryMaxOutputTokens > capability.MaxOutputTokens {
		return ModelContextBudgets{}, errors.New("output limit exceeds Provider Capability")
	}
	hard := capability.MaxInputTokens
	if windowBudget := capability.ContextWindowTokens - policy.PinnedMaxOutputTokens; windowBudget < hard {
		hard = windowBudget
	}
	safe := hard - policy.EstimationSafetyTokens
	if hard < 1 || safe < 1 || policy.SoftInputLimitTokens > safe || policy.KeepRecentTokens >= policy.SoftInputLimitTokens {
		return ModelContextBudgets{}, errors.New("input, safety, soft, or suffix limits are contradictory")
	}
	trigger := safe
	if policy.SoftInputLimitTokens < trigger {
		trigger = policy.SoftInputLimitTokens
	}
	return ModelContextBudgets{HardInputTokens: hard, SafeInputTokens: safe, CompactionTriggerTokens: trigger}, nil
}

func (c Catalog) validateTopology(reference Reference, ancestors map[Reference]bool, depth int) error {
	if ancestors == nil {
		ancestors = make(map[Reference]bool)
	}
	if ancestors[reference] {
		return fmt.Errorf("agent definition cycle at %s", reference)
	}
	if depth > 1 {
		return fmt.Errorf("agent definition depth exceeds one child level at %s", reference)
	}
	ancestors[reference] = true
	defer delete(ancestors, reference)
	for _, child := range c.definitions[reference].Children {
		if err := c.validateTopology(child, ancestors, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateDefinition(value Definition) error {
	if !validIdentifier(value.Identity) || value.Version < 1 || !validName(value.Executor) || validateReference(value.ModelPolicy) != nil {
		return errors.New("invalid identity, version, executor, or model policy")
	}
	if len(value.Prompts) == 0 || len(value.Tools) == 0 || len(value.Children) > 1 {
		return errors.New("definition must bind prompts and tools and at most one child")
	}
	for purpose, reference := range value.Prompts {
		if !validName(purpose) || validateReference(reference) != nil {
			return errors.New("definition has invalid prompt binding")
		}
	}
	if validateReference(value.Contracts.Input) != nil || validateReference(value.Contracts.Result) != nil {
		return errors.New("definition has invalid contract binding")
	}
	seenSkills := make(map[Reference]bool)
	for _, skill := range value.Skills {
		if validateReference(skill) != nil || seenSkills[skill] {
			return errors.New("definition has invalid or duplicate skill")
		}
		seenSkills[skill] = true
	}
	seenTools := make(map[string]bool)
	for _, tool := range value.Tools {
		if !validToolName(tool) || seenTools[tool] {
			return errors.New("definition has invalid or duplicate tool")
		}
		seenTools[tool] = true
	}
	seenChildren := make(map[Reference]bool)
	for _, child := range value.Children {
		if validateReference(child) != nil || seenChildren[child] {
			return errors.New("definition has invalid or duplicate child")
		}
		seenChildren[child] = true
	}
	if err := validatePositiveLimits(value.Limits); err != nil {
		return err
	}
	if value.Delegation != nil && strings.TrimSpace(value.Delegation.Description) == "" {
		return errors.New("delegation description is empty")
	}
	return nil
}

func validatePositiveLimits(limits Limits) error {
	if limits.ModelCalls < 1 || limits.ActionDecisions < 0 || limits.Actions < 1 || limits.LegacyPlanMutations < 0 || limits.ActionBatch < 1 || limits.ContextBytes < 1 || limits.ResultBytes < 1 || limits.Attempts < 1 {
		return errors.New("all definition limits must be positive")
	}
	if limits.ActionBatch > limits.Actions {
		return errors.New("action_batch cannot exceed actions")
	}
	if limits.ActionDecisions >= limits.ModelCalls {
		return errors.New("action_decisions must leave one final model call")
	}
	return nil
}

func validateReference(reference Reference) error {
	parsed, err := ParseReference(reference.String())
	if err != nil || parsed != reference {
		return errors.New("invalid immutable reference")
	}
	return nil
}

func validatePath(filePath string, reference Reference, suffix string) error {
	want := reference.Identity + ".v" + strconv.Itoa(reference.Version) + suffix
	if path.Base(filePath) != want {
		return fmt.Errorf("catalog path %s conflicts with identity %s", filePath, reference)
	}
	return nil
}

func canonicalHash(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func addUnique[T any](target map[Reference]T, reference Reference, value T, kind string) error {
	if _, exists := target[reference]; exists {
		return fmt.Errorf("duplicate %s %s", kind, reference)
	}
	target[reference] = value
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || !strings.Contains(value, ".") {
		return false
	}
	for _, character := range value {
		if unicode.IsLower(character) || unicode.IsDigit(character) || character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsLower(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validToolName(value string) bool {
	if value == "" || len(value) > maxMCPToolNameLength {
		return false
	}
	for _, character := range value {
		if unicode.IsLower(character) || unicode.IsDigit(character) || character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func lessReference(left, right Reference) bool {
	if left.Identity == right.Identity {
		return left.Version < right.Version
	}
	return left.Identity < right.Identity
}

func cloneDefinition(value Definition) Definition {
	value.Prompts = cloneMap(value.Prompts)
	value.Skills = cloneSlice(value.Skills)
	value.Tools = cloneSlice(value.Tools)
	value.Children = cloneSlice(value.Children)
	if value.Delegation != nil {
		delegation := *value.Delegation
		value.Delegation = &delegation
	}
	return value
}

func cloneSlice[T any](source []T) []T {
	if source == nil {
		return nil
	}
	return append(make([]T, 0, len(source)), source...)
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	cloned := make(map[K]V, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
