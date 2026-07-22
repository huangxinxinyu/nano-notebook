package rageval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
)

func TestCommandExecutorRunsOneBoundedStrictProductCase(t *testing.T) {
	executor, err := rageval.NewCommandExecutor(rageval.CommandExecutorConfig{
		Command: os.Args[0], Args: []string{"-test.run=TestRAGEvalCommandHelper", "--"},
		Env: []string{"NANO_RAG_EVAL_HELPER=success"}, Timeout: 2 * time.Second, MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	evalCase := rageval.Case{ID: "case-command", Critical: true, Families: []rageval.SourceFamily{rageval.FamilyTXT}, Language: rageval.LanguageEnglish, Question: "Question?", ExpectedEvidenceSets: [][]string{{"unit-a"}}, RequiredFacts: []string{"fact"}, Fixtures: []rageval.Fixture{{ID: "fixture-a", Family: rageval.FamilyTXT, URI: "fixture://sprint6/txt-en-v1", SHA256: "7a779b4f810b901de48889890fc53a025c365b02bd5ccfee3d58d4926f48e81d"}}}
	config := rageval.PinnedConfig{ExtractionConfigID: "extract-v1", EvidenceSchemaVersion: 1, ComposerModel: "compose", PromptVersion: "prompt-v1", AgentConfigID: "agent-v1"}
	observation, err := executor.ExecuteCase(context.Background(), evalCase, config)
	if err != nil {
		t.Fatal(err)
	}
	if observation.CaseID != evalCase.ID || !reflect.DeepEqual(observation.RetrievedEvidenceIDs, []string{"unit-a"}) || !reflect.DeepEqual(observation.CitationSourceIDs, []string{"fixture-a"}) {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestCommandExecutorRejectsTrailingOrOversizedProductOutput(t *testing.T) {
	for name, mode := range map[string]string{"trailing": "trailing", "oversized": "oversized"} {
		t.Run(name, func(t *testing.T) {
			executor, err := rageval.NewCommandExecutor(rageval.CommandExecutorConfig{
				Command: os.Args[0], Args: []string{"-test.run=TestRAGEvalCommandHelper", "--"},
				Env: []string{"NANO_RAG_EVAL_HELPER=" + mode}, Timeout: 2 * time.Second, MaxOutputBytes: 1024,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executor.ExecuteCase(context.Background(), rageval.Case{ID: "case"}, rageval.PinnedConfig{}); err == nil {
				t.Fatal("accepted invalid product executor output")
			}
		})
	}
}

func TestRAGEvalCommandHelper(t *testing.T) {
	mode := os.Getenv("NANO_RAG_EVAL_HELPER")
	if mode == "" {
		return
	}
	var request struct {
		SchemaVersion int                  `json:"schema_version"`
		Case          rageval.Case         `json:"case"`
		PinnedConfig  rageval.PinnedConfig `json:"pinned_config"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	if mode == "oversized" {
		fmt.Print(string(make([]byte, 2048)))
		os.Exit(0)
	}
	observation := rageval.Observation{
		CaseID: request.Case.ID, FixtureSHA256: map[string]string{"fixture-a": request.Case.Fixtures[0].SHA256}, CoveragePassed: true,
		RetrievedEvidenceIDs: []string{"unit-a"}, CitationSourceIDs: []string{"fixture-a"},
		RequiredFactsFound: []string{"fact"}, LatencyMilliseconds: 10,
	}
	_ = json.NewEncoder(os.Stdout).Encode(observation)
	if mode == "trailing" {
		fmt.Print(`{"unexpected":true}`)
	}
	os.Exit(0)
}
