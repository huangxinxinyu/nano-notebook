package rageval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

var (
	ErrSuiteInvalid    = errors.New("RAG Eval Suite is invalid")
	ErrSuiteIncomplete = errors.New("RAG Eval Suite is missing a critical Source family or language path")
	ErrConfigInvalid   = errors.New("RAG Eval pinned configuration is invalid")
)

type SourceFamily string

const (
	FamilyTXT      SourceFamily = "txt"
	FamilyMarkdown SourceFamily = "markdown"
	FamilyPDF      SourceFamily = "pdf"
	FamilyDOCX     SourceFamily = "docx"
	FamilyPPTX     SourceFamily = "pptx"
	FamilyHTML     SourceFamily = "html"
	FamilyYouTube  SourceFamily = "youtube"
	FamilyMP3      SourceFamily = "mp3"
	FamilyWAV      SourceFamily = "wav"
	FamilyM4A      SourceFamily = "m4a"
	FamilyPNG      SourceFamily = "png"
	FamilyJPEG     SourceFamily = "jpeg"
	FamilyWebP     SourceFamily = "webp"
)

var requiredFamilies = []SourceFamily{FamilyTXT, FamilyMarkdown, FamilyPDF, FamilyDOCX, FamilyPPTX, FamilyHTML, FamilyYouTube, FamilyMP3, FamilyWAV, FamilyM4A, FamilyPNG, FamilyJPEG, FamilyWebP}

type LanguagePath string

const (
	LanguageEnglish LanguagePath = "en"
	LanguageChinese LanguagePath = "zh"
	LanguageMixed   LanguagePath = "mixed"
)

var requiredLanguages = []LanguagePath{LanguageEnglish, LanguageChinese, LanguageMixed}

type Case struct {
	ID                   string         `json:"id"`
	Critical             bool           `json:"critical"`
	Families             []SourceFamily `json:"source_families"`
	Language             LanguagePath   `json:"language"`
	Question             string         `json:"question"`
	ExpectedEvidenceSets [][]string     `json:"expected_evidence_sets"`
	RequiredFacts        []string       `json:"required_facts"`
	ForbiddenClaims      []string       `json:"forbidden_claims,omitempty"`
	Rubric               []string       `json:"rubric,omitempty"`
	Fixtures             []Fixture      `json:"fixtures"`
}

type Fixture struct {
	ID     string       `json:"id"`
	Family SourceFamily `json:"source_family"`
	URI    string       `json:"uri"`
	SHA256 string       `json:"sha256"`
}

type Thresholds struct {
	MinRecall                 float64 `json:"min_recall"`
	MinMRR                    float64 `json:"min_mrr"`
	MinCitationPrecision      float64 `json:"min_citation_precision"`
	MinClaimCoverage          float64 `json:"min_claim_coverage"`
	MaxUnsupportedClaimRate   float64 `json:"max_unsupported_claim_rate"`
	MaxP95LatencyMilliseconds int64   `json:"max_p95_latency_milliseconds"`
	MaxMeanCostUSD            float64 `json:"max_mean_cost_usd"`
}

type Suite struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	Thresholds    Thresholds `json:"thresholds"`
	Cases         []Case     `json:"cases"`
}

func (s Suite) Validate() error {
	if s.SchemaVersion != 1 || strings.TrimSpace(s.ID) == "" || len(s.Cases) == 0 || !s.Thresholds.valid() {
		return ErrSuiteInvalid
	}
	ids := make(map[string]struct{}, len(s.Cases))
	criticalFamilies := make(map[SourceFamily]bool)
	criticalLanguages := make(map[LanguagePath]bool)
	for _, evalCase := range s.Cases {
		if strings.TrimSpace(evalCase.ID) == "" || strings.TrimSpace(evalCase.Question) == "" || len(evalCase.Families) == 0 ||
			!validLanguage(evalCase.Language) || len(evalCase.ExpectedEvidenceSets) == 0 || len(evalCase.RequiredFacts) == 0 || len(evalCase.Fixtures) == 0 {
			return ErrSuiteInvalid
		}
		if _, duplicate := ids[evalCase.ID]; duplicate {
			return ErrSuiteInvalid
		}
		ids[evalCase.ID] = struct{}{}
		for _, family := range evalCase.Families {
			if !validFamily(family) {
				return ErrSuiteInvalid
			}
			if evalCase.Critical {
				criticalFamilies[family] = true
			}
		}
		fixtureIDs := make(map[string]struct{}, len(evalCase.Fixtures))
		for _, fixture := range evalCase.Fixtures {
			if strings.TrimSpace(fixture.ID) == "" || strings.TrimSpace(fixture.URI) == "" || len(fixture.SHA256) != 64 || !validHex(fixture.SHA256) || !validFamily(fixture.Family) || !containsFamily(evalCase.Families, fixture.Family) {
				return ErrSuiteInvalid
			}
			if _, duplicate := fixtureIDs[fixture.ID]; duplicate {
				return ErrSuiteInvalid
			}
			fixtureIDs[fixture.ID] = struct{}{}
		}
		if evalCase.Critical {
			criticalLanguages[evalCase.Language] = true
		}
		for _, set := range evalCase.ExpectedEvidenceSets {
			if len(set) == 0 || hasBlankOrDuplicate(set) {
				return ErrSuiteInvalid
			}
		}
		if hasBlankOrDuplicate(evalCase.RequiredFacts) || hasBlankOrDuplicate(evalCase.ForbiddenClaims) || hasBlankOrDuplicate(evalCase.Rubric) {
			return ErrSuiteInvalid
		}
	}
	for _, family := range requiredFamilies {
		if !criticalFamilies[family] {
			return ErrSuiteIncomplete
		}
	}
	for _, language := range requiredLanguages {
		if !criticalLanguages[language] {
			return ErrSuiteIncomplete
		}
	}
	return nil
}

func (t Thresholds) valid() bool {
	unit := func(value float64) bool { return value >= 0 && value <= 1 && !math.IsNaN(value) }
	return unit(t.MinRecall) && unit(t.MinMRR) && unit(t.MinCitationPrecision) && unit(t.MinClaimCoverage) &&
		unit(t.MaxUnsupportedClaimRate) && t.MaxP95LatencyMilliseconds > 0 && t.MaxMeanCostUSD >= 0 && !math.IsNaN(t.MaxMeanCostUSD) && !math.IsInf(t.MaxMeanCostUSD, 0)
}

func (s Suite) SHA256() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type PinnedConfig struct {
	ExtractionConfigID    string                `json:"extraction_config_id"`
	EvidenceSchemaVersion int                   `json:"evidence_schema_version"`
	Index                 retrieval.IndexConfig `json:"index"`
	ComposerModel         string                `json:"composer_model"`
	VerifierModel         string                `json:"verifier_model"`
	VerifierPromptVersion string                `json:"verifier_prompt_version"`
	PromptVersion         string                `json:"prompt_version"`
	AgentConfigID         string                `json:"agent_config_id"`
}

func (c PinnedConfig) Validate() error {
	if strings.TrimSpace(c.ExtractionConfigID) == "" || c.EvidenceSchemaVersion < 1 || strings.TrimSpace(c.Index.AnalyzerID) == "" ||
		c.Index.Chunk.MaxRunes <= 0 || c.Index.DenseCandidates <= 0 || c.Index.SparseCandidates <= 0 || c.Index.RRFK <= 0 || c.Index.RerankCandidates <= 0 ||
		strings.TrimSpace(c.Index.EmbeddingModel) == "" || c.Index.EmbeddingDimensions <= 0 || strings.TrimSpace(c.Index.RerankerID) == "" ||
		strings.TrimSpace(c.ComposerModel) == "" || strings.TrimSpace(c.VerifierModel) == "" || strings.TrimSpace(c.VerifierPromptVersion) == "" || strings.TrimSpace(c.PromptVersion) == "" || strings.TrimSpace(c.AgentConfigID) == "" {
		return ErrConfigInvalid
	}
	return nil
}

type Observation struct {
	CaseID                string            `json:"case_id"`
	FixtureSHA256         map[string]string `json:"fixture_sha256"`
	CoveragePassed        bool              `json:"coverage_passed"`
	InvariantFailures     []string          `json:"invariant_failures,omitempty"`
	RetrievedEvidenceIDs  []string          `json:"retrieved_evidence_ids"`
	CitationEvidenceIDs   []string          `json:"citation_evidence_ids"`
	MaterialClaimCount    int               `json:"material_claim_count"`
	CitedClaimCount       int               `json:"cited_claim_count"`
	UnsupportedClaimCount int               `json:"unsupported_claim_count"`
	RequiredFactsFound    []string          `json:"required_facts_found"`
	ForbiddenClaimsFound  []string          `json:"forbidden_claims_found,omitempty"`
	LatencyMilliseconds   int64             `json:"latency_milliseconds"`
	InputTokens           int64             `json:"input_tokens"`
	TotalTokens           int64             `json:"total_tokens"`
	EstimatedCostUSD      float64           `json:"estimated_cost_usd"`
	JudgeScore            *float64          `json:"judge_score,omitempty"`
}

type Executor interface {
	// ExecuteCase must compose the production Source, Retrieval, Models, Agent,
	// Citation, and Viewer-facing boundaries. The evaluator owns no RAG path.
	ExecuteCase(context.Context, Case, PinnedConfig) (Observation, error)
}

type Metrics struct {
	Recall                 float64  `json:"recall"`
	MRR                    float64  `json:"mrr"`
	CitationPrecision      float64  `json:"citation_precision"`
	ClaimCoverage          float64  `json:"claim_coverage"`
	UnsupportedClaimRate   float64  `json:"unsupported_claim_rate"`
	P95LatencyMilliseconds int64    `json:"p95_latency_milliseconds"`
	MeanCostUSD            float64  `json:"mean_cost_usd"`
	InputTokens            int64    `json:"input_tokens"`
	TotalTokens            int64    `json:"total_tokens"`
	MeanJudgeScore         *float64 `json:"mean_judge_score,omitempty"`
}

type CaseResult struct {
	CaseID      string      `json:"case_id"`
	Critical    bool        `json:"critical"`
	Passed      bool        `json:"passed"`
	Failures    []string    `json:"failures,omitempty"`
	Observation Observation `json:"observation"`
}

type Report struct {
	SchemaVersion      int                  `json:"schema_version"`
	SuiteID            string               `json:"suite_id"`
	FixtureSuiteSHA256 string               `json:"fixture_suite_sha256"`
	PinnedConfig       PinnedConfig         `json:"pinned_config"`
	Thresholds         Thresholds           `json:"thresholds"`
	Status             retrieval.EvalStatus `json:"status"`
	InvariantFailures  int                  `json:"invariant_failures"`
	CriticalFailures   int                  `json:"critical_failures"`
	GateFailures       []string             `json:"gate_failures,omitempty"`
	Metrics            Metrics              `json:"metrics"`
	Cases              []CaseResult         `json:"cases"`
}

func Evaluate(ctx context.Context, suite Suite, config PinnedConfig, executor Executor) (Report, error) {
	if err := suite.Validate(); err != nil {
		return Report{}, err
	}
	if err := config.Validate(); err != nil {
		return Report{}, err
	}
	if executor == nil {
		return Report{}, errors.New("RAG Eval Executor is required")
	}
	digest, err := suite.SHA256()
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: 1, SuiteID: suite.ID, FixtureSuiteSHA256: digest, PinnedConfig: config, Thresholds: suite.Thresholds, Status: retrieval.EvalPassed, Cases: make([]CaseResult, 0, len(suite.Cases))}
	latencies := make([]int64, 0, len(suite.Cases))
	var recall, mrr, citationHits, citationTotal, claims, cited, unsupported, cost, judge float64
	judgeCount := 0
	for _, evalCase := range suite.Cases {
		observation, executeErr := executor.ExecuteCase(ctx, evalCase, config)
		if executeErr != nil {
			return Report{}, executeErr
		}
		if err := validateObservation(evalCase, observation); err != nil {
			return Report{}, err
		}
		if !fixturesMatch(evalCase.Fixtures, observation.FixtureSHA256) {
			observation.InvariantFailures = append(observation.InvariantFailures, "fixture_identity_mismatch")
		}
		caseRecall, caseMRR, allowed := retrievalMetrics(evalCase, observation.RetrievedEvidenceIDs)
		recall += caseRecall
		mrr += caseMRR
		for _, id := range observation.CitationEvidenceIDs {
			citationTotal++
			if allowed[id] {
				citationHits++
			}
		}
		claims += float64(observation.MaterialClaimCount)
		cited += float64(observation.CitedClaimCount)
		unsupported += float64(observation.UnsupportedClaimCount)
		latencies = append(latencies, observation.LatencyMilliseconds)
		cost += observation.EstimatedCostUSD
		report.Metrics.InputTokens += observation.InputTokens
		report.Metrics.TotalTokens += observation.TotalTokens
		if observation.JudgeScore != nil {
			judge += *observation.JudgeScore
			judgeCount++
		}
		failures := caseFailures(evalCase, observation, caseRecall, citationPrecision(observation.CitationEvidenceIDs, allowed))
		report.InvariantFailures += len(observation.InvariantFailures)
		passed := len(failures) == 0
		if evalCase.Critical && !passed {
			report.CriticalFailures++
		}
		report.Cases = append(report.Cases, CaseResult{CaseID: evalCase.ID, Critical: evalCase.Critical, Passed: passed, Failures: failures, Observation: observation})
	}
	count := float64(len(suite.Cases))
	report.Metrics.Recall = recall / count
	report.Metrics.MRR = mrr / count
	if citationTotal > 0 {
		report.Metrics.CitationPrecision = citationHits / citationTotal
	}
	if claims > 0 {
		report.Metrics.ClaimCoverage = cited / claims
		report.Metrics.UnsupportedClaimRate = unsupported / claims
	}
	report.Metrics.P95LatencyMilliseconds = percentile95(latencies)
	report.Metrics.MeanCostUSD = cost / count
	if judgeCount > 0 {
		value := judge / float64(judgeCount)
		report.Metrics.MeanJudgeScore = &value
	}
	report.GateFailures = aggregateFailures(report, suite.Thresholds)
	if len(report.GateFailures) > 0 {
		report.Status = retrieval.EvalFailed
	}
	return report, nil
}

func validateObservation(evalCase Case, observation Observation) error {
	if observation.CaseID != evalCase.ID || observation.MaterialClaimCount < 0 || observation.CitedClaimCount < 0 || observation.CitedClaimCount > observation.MaterialClaimCount ||
		observation.UnsupportedClaimCount < 0 || observation.UnsupportedClaimCount > observation.MaterialClaimCount || observation.LatencyMilliseconds < 0 ||
		observation.InputTokens < 0 || observation.TotalTokens < observation.InputTokens || observation.EstimatedCostUSD < 0 || math.IsNaN(observation.EstimatedCostUSD) || math.IsInf(observation.EstimatedCostUSD, 0) ||
		hasBlankOrDuplicate(observation.RetrievedEvidenceIDs) || hasBlankOrDuplicate(observation.CitationEvidenceIDs) || hasBlankOrDuplicate(observation.InvariantFailures) ||
		hasBlankOrDuplicate(observation.RequiredFactsFound) || hasBlankOrDuplicate(observation.ForbiddenClaimsFound) {
		return errors.New("RAG Eval observation is invalid")
	}
	if observation.JudgeScore != nil && (*observation.JudgeScore < 0 || *observation.JudgeScore > 1 || math.IsNaN(*observation.JudgeScore)) {
		return errors.New("RAG Eval judge score is invalid")
	}
	return nil
}

func fixturesMatch(fixtures []Fixture, observed map[string]string) bool {
	if len(observed) != len(fixtures) {
		return false
	}
	for _, fixture := range fixtures {
		if observed[fixture.ID] != fixture.SHA256 {
			return false
		}
	}
	return true
}

func caseFailures(evalCase Case, observation Observation, recall, precision float64) []string {
	failures := append([]string(nil), observation.InvariantFailures...)
	if !observation.CoveragePassed {
		failures = append(failures, "coverage")
	}
	if recall < 1 {
		failures = append(failures, "expected_evidence")
	}
	if precision < 1 {
		failures = append(failures, "citation_correctness")
	}
	if observation.MaterialClaimCount == 0 || observation.CitedClaimCount != observation.MaterialClaimCount {
		failures = append(failures, "claim_coverage")
	}
	if observation.UnsupportedClaimCount != 0 {
		failures = append(failures, "unsupported_claim")
	}
	if !containsAll(observation.RequiredFactsFound, evalCase.RequiredFacts) {
		failures = append(failures, "required_facts")
	}
	if len(observation.ForbiddenClaimsFound) != 0 {
		failures = append(failures, "forbidden_claims")
	}
	return failures
}

func aggregateFailures(report Report, thresholds Thresholds) []string {
	failures := make([]string, 0, 9)
	if report.InvariantFailures != 0 {
		failures = append(failures, "invariants")
	}
	if report.CriticalFailures != 0 {
		failures = append(failures, "critical_cases")
	}
	if report.Metrics.Recall < thresholds.MinRecall {
		failures = append(failures, "recall")
	}
	if report.Metrics.MRR < thresholds.MinMRR {
		failures = append(failures, "mrr")
	}
	if report.Metrics.CitationPrecision < thresholds.MinCitationPrecision {
		failures = append(failures, "citation_precision")
	}
	if report.Metrics.ClaimCoverage < thresholds.MinClaimCoverage {
		failures = append(failures, "claim_coverage")
	}
	if report.Metrics.UnsupportedClaimRate > thresholds.MaxUnsupportedClaimRate {
		failures = append(failures, "unsupported_claim_rate")
	}
	if report.Metrics.P95LatencyMilliseconds > thresholds.MaxP95LatencyMilliseconds {
		failures = append(failures, "p95_latency")
	}
	if report.Metrics.MeanCostUSD > thresholds.MaxMeanCostUSD {
		failures = append(failures, "mean_cost")
	}
	return failures
}

type PromotionStore interface {
	RecordEval(context.Context, retrieval.EvalRun) error
	Promote(context.Context, string, string) (retrieval.IndexVersion, error)
}

func EvaluateRecordAndPromote(ctx context.Context, evalRunID, versionID string, suite Suite, config PinnedConfig, executor Executor, store PromotionStore) (Report, error) {
	if strings.TrimSpace(evalRunID) == "" || strings.TrimSpace(versionID) == "" || store == nil {
		return Report{}, errors.New("RAG Eval promotion request is invalid")
	}
	report, err := Evaluate(ctx, suite, config, executor)
	if err != nil {
		return Report{}, err
	}
	metrics, err := json.Marshal(report)
	if err != nil {
		return Report{}, err
	}
	if err := store.RecordEval(ctx, retrieval.EvalRun{ID: evalRunID, IndexVersionID: versionID, FixtureSuiteSHA256: report.FixtureSuiteSHA256, Status: report.Status, MetricsJSON: metrics}); err != nil {
		return Report{}, err
	}
	if report.Status == retrieval.EvalPassed {
		if _, err := store.Promote(ctx, versionID, evalRunID); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func retrievalMetrics(evalCase Case, ids []string) (float64, float64, map[string]bool) {
	allowed := expectedEvidence(evalCase)
	found := 0
	for _, set := range evalCase.ExpectedEvidenceSets {
		matched := false
		for _, id := range ids {
			for _, expected := range set {
				if id == expected {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			found++
		}
	}
	recall := float64(found) / float64(len(evalCase.ExpectedEvidenceSets))
	mrr := 0.0
	for index, id := range ids {
		if allowed[id] {
			mrr = 1 / float64(index+1)
			break
		}
	}
	return recall, mrr, allowed
}

func citationPrecision(ids []string, allowed map[string]bool) float64 {
	if len(ids) == 0 {
		return 0
	}
	hits := 0
	for _, id := range ids {
		if allowed[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(ids))
}

func expectedEvidence(evalCase Case) map[string]bool {
	result := make(map[string]bool)
	for _, set := range evalCase.ExpectedEvidenceSets {
		for _, id := range set {
			result[id] = true
		}
	}
	return result
}

func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(math.Ceil(.95*float64(len(ordered)))) - 1
	return ordered[index]
}

func containsAll(have, required []string) bool {
	set := make(map[string]bool, len(have))
	for _, value := range have {
		set[value] = true
	}
	for _, value := range required {
		if !set[value] {
			return false
		}
	}
	return true
}

func hasBlankOrDuplicate(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return true
		}
		if _, duplicate := seen[value]; duplicate {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validFamily(value SourceFamily) bool {
	for _, family := range requiredFamilies {
		if value == family {
			return true
		}
	}
	return false
}

func validLanguage(value LanguagePath) bool {
	for _, language := range requiredLanguages {
		if value == language {
			return true
		}
	}
	return false
}

func validHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsFamily(values []SourceFamily, wanted SourceFamily) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
