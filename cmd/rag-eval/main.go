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
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprojection"
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
	executorCommand := flags.String("executor-command", "", "bounded product Case Executor command (JSON stdin/stdout)")
	productRunsPath := flags.String("product-runs", "", "manifest mapping Eval Cases to completed durable product Runs")
	liveProductSourcesPath := flags.String("live-product-sources", "", "manifest mapping Eval Cases to ready fixture Sources")
	executorTimeout := flags.Duration("executor-timeout", 5*time.Minute, "per-Case product Executor timeout")
	bifrostURL := flags.String("bifrost-url", "http://127.0.0.1:56666", "Bifrost model gateway URL for live product Eval")
	qdrantURL := flags.String("qdrant-url", "http://127.0.0.1:56333", "Qdrant URL for live product Eval")
	qdrantAPIKey := flags.String("qdrant-api-key", os.Getenv("NANO_QDRANT_API_KEY"), "Qdrant API key for live product Eval")
	qdrantCollection := flags.String("qdrant-collection", "nano-source-evidence", "Qdrant collection for live product Eval")
	databaseURL := flags.String("database-url", "", "PostgreSQL URL used to record and promote a passing candidate")
	evalRunID := flags.String("eval-run-id", "", "durable Eval Run identity")
	versionID := flags.String("index-version-id", "", "candidate Retrieval Index Version identity")
	createCandidate := flags.Bool("create-candidate", false, "create the live Eval candidate from the pinned configuration when it does not exist")
	if err := flags.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, value := range []string{*observationsPath, *executorCommand, *productRunsPath, *liveProductSourcesPath} {
		if strings.TrimSpace(value) != "" {
			modes++
		}
	}
	if modes != 1 {
		return errors.New("exactly one of -observations, -executor-command, -product-runs, or -live-product-sources is required")
	}
	if strings.TrimSpace(*evalRunID) != "" && (strings.TrimSpace(*observationsPath) != "" || strings.TrimSpace(*productRunsPath) != "") {
		return errors.New("only a live or bounded product Executor can authorize Retrieval Index promotion")
	}
	var suite rageval.Suite
	if err := decodeStrictFile(*suitePath, &suite); err != nil {
		return fmt.Errorf("load Eval Suite: %w", err)
	}
	var config rageval.PinnedConfig
	if err := decodeStrictFile(*configPath, &config); err != nil {
		return fmt.Errorf("load pinned configuration: %w", err)
	}
	var executor rageval.Executor
	var pool *pgxpool.Pool
	var err error
	if strings.TrimSpace(*observationsPath) != "" {
		var observations []rageval.Observation
		if err := decodeStrictFile(*observationsPath, &observations); err != nil {
			return fmt.Errorf("load product observations: %w", err)
		}
		executor, err = newObservationExecutor(observations)
	} else if strings.TrimSpace(*executorCommand) != "" {
		executor, err = rageval.NewCommandExecutor(rageval.CommandExecutorConfig{
			Command: *executorCommand, Timeout: *executorTimeout, MaxOutputBytes: 8 << 20,
		})
	} else if strings.TrimSpace(*productRunsPath) != "" {
		if strings.TrimSpace(*databaseURL) == "" || strings.TrimSpace(*versionID) == "" {
			return errors.New("-database-url and -index-version-id are required with -product-runs")
		}
		var manifest rageval.ProductRunManifest
		if err := decodeStrictFile(*productRunsPath, &manifest); err != nil {
			return fmt.Errorf("load product Run manifest: %w", err)
		}
		if manifest.IndexVersionID != *versionID {
			return errors.New("product Run manifest Index Version does not match -index-version-id")
		}
		pool, err = pgxpool.New(context.Background(), *databaseURL)
		if err == nil {
			executor, err = rageval.NewProductRunExecutor(pool, manifest)
		}
	} else {
		if strings.TrimSpace(*databaseURL) == "" || strings.TrimSpace(*versionID) == "" {
			return errors.New("-database-url and -index-version-id are required with -live-product-sources")
		}
		var manifest rageval.ProductSourceManifest
		if err := decodeStrictFile(*liveProductSourcesPath, &manifest); err != nil {
			return fmt.Errorf("load live product Source manifest: %w", err)
		}
		if manifest.IndexVersionID != *versionID {
			return errors.New("live product Source manifest Index Version does not match -index-version-id")
		}
		pool, err = pgxpool.New(context.Background(), *databaseURL)
		if err == nil {
			versionStore := retrieval.NewVersionStore(pool)
			if _, versionErr := versionStore.ByID(context.Background(), *versionID); errors.Is(versionErr, retrieval.ErrVersionNotFound) && *createCandidate {
				_, err = versionStore.CreateCandidate(context.Background(), *versionID, config.Index)
			} else if versionErr != nil {
				err = versionErr
			}
			var vectors *qdrantstore.Client
			if err == nil {
				vectors, err = qdrantstore.New(qdrantstore.Config{
					BaseURL: *qdrantURL, APIKey: *qdrantAPIKey, Collection: *qdrantCollection,
					DenseDimensions: config.Index.EmbeddingDimensions, RequestTimeout: *executorTimeout,
					HTTPClient: &http.Client{Timeout: *executorTimeout},
				})
			}
			if err == nil {
				err = vectors.EnsureCollection(context.Background())
			}
			model := models.NewBifrostClient(*bifrostURL, &http.Client{Timeout: *executorTimeout}, 2048)
			if err == nil {
				_, err = sourceprojection.NewReindexer(pool, vectors, model).ReindexVersion(context.Background(), *versionID)
			}
			if err == nil {
				executor, err = rageval.NewLiveProductExecutor(pool, vectors, model, manifest, config.VerifierModel, config.VerifierPromptVersion)
			}
		}
	}
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		return err
	}
	if pool != nil {
		defer pool.Close()
	}
	ctx := context.Background()
	recording := strings.TrimSpace(*evalRunID) != ""
	var report rageval.Report
	if recording {
		if strings.TrimSpace(*databaseURL) == "" || strings.TrimSpace(*evalRunID) == "" || strings.TrimSpace(*versionID) == "" {
			return errors.New("-database-url, -eval-run-id, and -index-version-id are required together")
		}
		if pool == nil {
			var poolErr error
			pool, poolErr = pgxpool.New(ctx, *databaseURL)
			if poolErr != nil {
				return poolErr
			}
			defer pool.Close()
		}
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
