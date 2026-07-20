package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errEvalGatesFailed = errors.New("RAG Eval gates failed")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("rag-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	suitePath := flags.String("suite", "evals/rag/sprint6-v1.json", "path to the frozen Eval Suite")
	configPath := flags.String("config", "evals/rag/pinned-config-v1.json", "path to the pinned product configuration")
	observationsPath := flags.String("observations", "", "path to observations emitted by the product Eval Executor")
	databaseURL := flags.String("database-url", "", "PostgreSQL URL used to record and promote a passing candidate")
	evalRunID := flags.String("eval-run-id", "", "durable Eval Run identity")
	versionID := flags.String("index-version-id", "", "candidate Retrieval Index Version identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*observationsPath) == "" {
		return errors.New("-observations is required")
	}
	var suite rageval.Suite
	if err := decodeStrictFile(*suitePath, &suite); err != nil {
		return fmt.Errorf("load Eval Suite: %w", err)
	}
	var config rageval.PinnedConfig
	if err := decodeStrictFile(*configPath, &config); err != nil {
		return fmt.Errorf("load pinned configuration: %w", err)
	}
	var observations []rageval.Observation
	if err := decodeStrictFile(*observationsPath, &observations); err != nil {
		return fmt.Errorf("load product observations: %w", err)
	}
	executor, err := newObservationExecutor(observations)
	if err != nil {
		return err
	}
	ctx := context.Background()
	recording := strings.TrimSpace(*databaseURL) != "" || strings.TrimSpace(*evalRunID) != "" || strings.TrimSpace(*versionID) != ""
	var report rageval.Report
	if recording {
		if strings.TrimSpace(*databaseURL) == "" || strings.TrimSpace(*evalRunID) == "" || strings.TrimSpace(*versionID) == "" {
			return errors.New("-database-url, -eval-run-id, and -index-version-id are required together")
		}
		pool, poolErr := pgxpool.New(ctx, *databaseURL)
		if poolErr != nil {
			return poolErr
		}
		defer pool.Close()
		report, err = rageval.EvaluateRecordAndPromote(ctx, *evalRunID, *versionID, suite, config, executor, retrieval.NewVersionStore(pool))
	} else {
		report, err = rageval.Evaluate(ctx, suite, config, executor)
	}
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, string(encoded)); err != nil {
		return err
	}
	if report.Status != retrieval.EvalPassed {
		return errEvalGatesFailed
	}
	return nil
}

type observationExecutor struct {
	byCase map[string]rageval.Observation
}

func newObservationExecutor(observations []rageval.Observation) (*observationExecutor, error) {
	if len(observations) == 0 {
		return nil, errors.New("product observations are empty")
	}
	executor := &observationExecutor{byCase: make(map[string]rageval.Observation, len(observations))}
	for _, observation := range observations {
		if strings.TrimSpace(observation.CaseID) == "" {
			return nil, errors.New("product observation Case identity is empty")
		}
		if _, duplicate := executor.byCase[observation.CaseID]; duplicate {
			return nil, errors.New("product observation Case identity is duplicated")
		}
		executor.byCase[observation.CaseID] = observation
	}
	return executor, nil
}

func (e *observationExecutor) ExecuteCase(_ context.Context, evalCase rageval.Case, _ rageval.PinnedConfig) (rageval.Observation, error) {
	observation, ok := e.byCase[evalCase.ID]
	if !ok {
		return rageval.Observation{}, fmt.Errorf("product observation for Case %q is missing", evalCase.ID)
	}
	return observation, nil
}

func decodeStrictFile(path string, target any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing content")
	}
	return nil
}
