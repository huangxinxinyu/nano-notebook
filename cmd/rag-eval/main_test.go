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
	observations := make([]rageval.Observation, 0, len(suite.Cases))
	for _, evalCase := range suite.Cases {
		retrieved := make([]string, 0, len(evalCase.ExpectedEvidenceSets))
		for _, set := range evalCase.ExpectedEvidenceSets {
			retrieved = append(retrieved, set[0])
		}
		fixtures := make(map[string]string, len(evalCase.Fixtures))
		for _, fixture := range evalCase.Fixtures {
			fixtures[fixture.ID] = fixture.SHA256
		}
		observations = append(observations, rageval.Observation{
			CaseID: evalCase.ID, FixtureSHA256: fixtures, CoveragePassed: true,
			RetrievedEvidenceIDs: retrieved, CitationEvidenceIDs: retrieved,
			MaterialClaimCount: 1, CitedClaimCount: 1, RequiredFactsFound: evalCase.RequiredFacts,
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
	if report.Status != "passed" || report.FixtureSuiteSHA256 != "bf7f7e3e558ef5bb1bddc516375d0a13b93edf910902bce90881f1d9e8c65b4d" {
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
