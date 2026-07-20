package rageval_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestSuiteRequiresEverySourceFamilyAndLanguagePathAsCriticalCases(t *testing.T) {
	suite := completeSuite()
	if err := suite.Validate(); err != nil {
		t.Fatal(err)
	}
	suite.Cases = suite.Cases[:len(suite.Cases)-1]
	if err := suite.Validate(); !errors.Is(err, rageval.ErrSuiteIncomplete) {
		t.Fatalf("Validate = %v, want incomplete suite", err)
	}
}

func TestEvaluatorAppliesFrozenGatesAndNeverLetsJudgeOverrideGoldenTruth(t *testing.T) {
	suite := completeSuite()
	executor := &executorStub{observations: make(map[string]rageval.Observation)}
	for _, evalCase := range suite.Cases {
		executor.observations[evalCase.ID] = passingObservation(evalCase)
	}
	broken := suite.Cases[0]
	observation := passingObservation(broken)
	observation.InvariantFailures = []string{"citation_identity_mismatch"}
	perfectJudge := 1.0
	observation.JudgeScore = &perfectJudge
	executor.observations[broken.ID] = observation

	report, err := rageval.Evaluate(context.Background(), suite, pinnedConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != retrieval.EvalFailed || report.InvariantFailures != 1 || report.CriticalFailures != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Metrics.MeanJudgeScore == nil || *report.Metrics.MeanJudgeScore != 1 {
		t.Fatalf("judge score was not recorded: %+v", report.Metrics)
	}
	if executor.calls != len(suite.Cases) {
		t.Fatalf("executor calls = %d", executor.calls)
	}
}

func TestEvaluatorPassesOnlyWhenCriticalInvariantQualityLatencyAndCostGatesPass(t *testing.T) {
	suite := completeSuite()
	executor := &executorStub{observations: make(map[string]rageval.Observation)}
	for _, evalCase := range suite.Cases {
		executor.observations[evalCase.ID] = passingObservation(evalCase)
	}
	report, err := rageval.Evaluate(context.Background(), suite, pinnedConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != retrieval.EvalPassed || len(report.GateFailures) != 0 || report.FixtureSuiteSHA256 == "" {
		t.Fatalf("report = %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("report JSON = %s, %v", encoded, err)
	}

	overBudget := executor.observations[suite.Cases[0].ID]
	overBudget.EstimatedCostUSD = suite.Thresholds.MaxMeanCostUSD * float64(len(suite.Cases)+1)
	executor.observations[suite.Cases[0].ID] = overBudget
	report, err = rageval.Evaluate(context.Background(), suite, pinnedConfig(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != retrieval.EvalFailed || !contains(report.GateFailures, "mean_cost") {
		t.Fatalf("cost report = %+v", report)
	}
}

func TestPromotionRecordsExactReportBeforePromotingCandidate(t *testing.T) {
	suite := completeSuite()
	executor := &executorStub{observations: make(map[string]rageval.Observation)}
	for _, evalCase := range suite.Cases {
		executor.observations[evalCase.ID] = passingObservation(evalCase)
	}
	store := &promotionStoreStub{}
	report, err := rageval.EvaluateRecordAndPromote(context.Background(), "eval-sprint6", "riv-sprint6", suite, pinnedConfig(), executor, store)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != retrieval.EvalPassed || len(store.runs) != 1 || store.promotedVersion != "riv-sprint6" || store.promotedEval != "eval-sprint6" {
		t.Fatalf("report/store = %+v/%+v", report, store)
	}
	if store.runs[0].FixtureSuiteSHA256 != report.FixtureSuiteSHA256 || store.runs[0].Status != retrieval.EvalPassed {
		t.Fatalf("recorded run = %+v", store.runs[0])
	}
}

func completeSuite() rageval.Suite {
	families := []rageval.SourceFamily{rageval.FamilyTXT, rageval.FamilyMarkdown, rageval.FamilyPDF, rageval.FamilyDOCX, rageval.FamilyPPTX, rageval.FamilyHTML, rageval.FamilyYouTube, rageval.FamilyMP3, rageval.FamilyWAV, rageval.FamilyM4A, rageval.FamilyPNG, rageval.FamilyJPEG, rageval.FamilyWebP}
	cases := make([]rageval.Case, 0, len(families)+3)
	for _, family := range families {
		cases = append(cases, rageval.Case{ID: "critical-" + string(family), Critical: true, Families: []rageval.SourceFamily{family}, Language: rageval.LanguageEnglish, Question: "Find the launch fact.", ExpectedEvidenceSets: [][]string{{"evidence-" + string(family)}}, RequiredFacts: []string{"launch"}, ForbiddenClaims: []string{"cancelled"}, Fixtures: []rageval.Fixture{{ID: "fixture-" + string(family), Family: family, URI: "fixture://sprint6/" + string(family), SHA256: strings.Repeat("a", 64)}}})
	}
	for _, language := range []rageval.LanguagePath{rageval.LanguageChinese, rageval.LanguageMixed} {
		cases = append(cases, rageval.Case{ID: "critical-language-" + string(language), Critical: true, Families: []rageval.SourceFamily{rageval.FamilyTXT}, Language: language, Question: "查找 launch 信息", ExpectedEvidenceSets: [][]string{{"evidence-" + string(language)}}, RequiredFacts: []string{"launch"}, Fixtures: []rageval.Fixture{{ID: "fixture-" + string(language), Family: rageval.FamilyTXT, URI: "fixture://sprint6/" + string(language), SHA256: strings.Repeat("b", 64)}}})
	}
	return rageval.Suite{SchemaVersion: 1, ID: "sprint6-v1", Cases: cases, Thresholds: rageval.Thresholds{
		MinRecall: 1, MinMRR: .9, MinCitationPrecision: 1, MinClaimCoverage: 1, MaxUnsupportedClaimRate: 0,
		MaxP95LatencyMilliseconds: 500, MaxMeanCostUSD: .02,
	}}
}

func pinnedConfig() rageval.PinnedConfig {
	return rageval.PinnedConfig{
		ExtractionConfigID: "extract-v1", EvidenceSchemaVersion: 1,
		Index:         retrieval.IndexConfig{Chunk: retrieval.ChunkConfig{MaxRunes: 800, OverlapRunes: 120}, AnalyzerID: "nano-mixed-v1", BM25K1: 1.2, BM25B: .75, BM25AverageDocumentLength: 240, EmbeddingModel: "embedding-v1", EmbeddingDimensions: 1024, DenseCandidates: 40, SparseCandidates: 40, RRFK: 60, RerankerID: "rerank-v1", RerankCandidates: 20, DegradationPolicyID: "hybrid-v1"},
		ComposerModel: "composer-v1", VerifierModel: "verifier-v1", PromptVersion: "prompt-v1", AgentConfigID: "agent-v1",
	}
}

func passingObservation(evalCase rageval.Case) rageval.Observation {
	retrieved := make([]string, 0, len(evalCase.ExpectedEvidenceSets))
	for _, set := range evalCase.ExpectedEvidenceSets {
		retrieved = append(retrieved, set[0])
	}
	fixtures := make(map[string]string, len(evalCase.Fixtures))
	for _, fixture := range evalCase.Fixtures {
		fixtures[fixture.ID] = fixture.SHA256
	}
	return rageval.Observation{CaseID: evalCase.ID, FixtureSHA256: fixtures, CoveragePassed: true, RetrievedEvidenceIDs: retrieved, CitationEvidenceIDs: retrieved, MaterialClaimCount: 1, CitedClaimCount: 1, RequiredFactsFound: append([]string(nil), evalCase.RequiredFacts...), LatencyMilliseconds: 100, EstimatedCostUSD: .01}
}

type executorStub struct {
	observations map[string]rageval.Observation
	calls        int
}

func (s *executorStub) ExecuteCase(_ context.Context, evalCase rageval.Case, _ rageval.PinnedConfig) (rageval.Observation, error) {
	s.calls++
	return s.observations[evalCase.ID], nil
}

type promotionStoreStub struct {
	runs            []retrieval.EvalRun
	promotedVersion string
	promotedEval    string
}

func (s *promotionStoreStub) RecordEval(_ context.Context, run retrieval.EvalRun) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *promotionStoreStub) Promote(_ context.Context, versionID, evalRunID string) (retrieval.IndexVersion, error) {
	s.promotedVersion, s.promotedEval = versionID, evalRunID
	return retrieval.IndexVersion{ID: versionID, Status: retrieval.VersionActive, PromotedByEvalRunID: evalRunID}, nil
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
