package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
)

func TestRunEvaluatesFrozenSuiteFromProductObservations(t *testing.T) {
	suitePath := filepath.Join("..", "..", "evals", "rag", "sprint6-v1.json")
	configPath := filepath.Join("..", "..", "evals", "rag", "pinned-config-v1.json")
	var suite rageval.Suite
	decodeTestJSON(t, suitePath, &suite)
	var config rageval.PinnedConfig
	decodeTestJSON(t, configPath, &config)
	if config.Index.EmbeddingModel != "gemini/gemini-embedding-2" || config.Index.EmbeddingDimensions != 768 ||
		config.Index.EmbeddingProfileID != "gemini-retrieval-v1" {
		t.Fatalf("pinned embedding config=%+v", config.Index)
	}
	observations := make([]rageval.Observation, 0, len(suite.Cases))
	for _, evalCase := range suite.Cases {
		retrieved := make([]string, 0, len(evalCase.ExpectedEvidenceSets))
		for _, set := range evalCase.ExpectedEvidenceSets {
			retrieved = append(retrieved, set[0])
		}
		fixtures := make(map[string]string, len(evalCase.Fixtures))
		sources := make([]string, 0, len(evalCase.Fixtures))
		for _, fixture := range evalCase.Fixtures {
			fixtures[fixture.ID] = fixture.SHA256
			sources = append(sources, fixture.ID)
		}
		observations = append(observations, rageval.Observation{
			CaseID: evalCase.ID, FixtureSHA256: fixtures, CoveragePassed: true,
			RetrievedEvidenceIDs: retrieved, CitationSourceIDs: sources, RequiredFactsFound: evalCase.RequiredFacts,
			LatencyMilliseconds: 100, EstimatedCostUSD: .01,
		})
	}
	payload, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	observationPath := filepath.Join(t.TempDir(), "observations.json")
	if err := os.WriteFile(observationPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"-suite", suitePath, "-config", configPath, "-observations", observationPath}, &output); err != nil {
		t.Fatal(err)
	}
	var report rageval.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || report.FixtureSuiteSHA256 != "67668d9c0e938f8a5572573dcc3c840d14cf7a24a2b99ecba83cabdd09e8fa1f" {
		t.Fatalf("report = %+v", report)
	}
	if err := run([]string{
		"-suite", suitePath, "-config", configPath, "-observations", observationPath,
		"-database-url", "postgres://unused", "-eval-run-id", "eval_spoofed", "-index-version-id", "riv_spoofed",
	}, &bytes.Buffer{}); err == nil || err.Error() != "only a live or bounded product Executor can authorize Retrieval Index promotion" {
		t.Fatalf("precomputed promotion error = %v", err)
	}
}

func decodeTestJSON(t *testing.T, path string, target any) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatal(err)
	}
}
