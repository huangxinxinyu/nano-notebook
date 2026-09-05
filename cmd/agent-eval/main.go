// Command agent-eval turns real production Agent-decision failures into
// checked-in regression cases. See internal/agenteval for the package doc.
package main

import (
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

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agenteval"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/websearch"
)

const defaultAgentRelease = "nano.default@26"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		if err := runSuite(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runCandidates(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func runCandidates(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("agent-eval candidates", flag.ContinueOnError)
	databaseURL := flags.String("database-url", "", "PostgreSQL URL")
	limit := flags.Int("limit", 50, "maximum candidates per discovery query")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*databaseURL) == "" {
		return errors.New("-database-url is required")
	}
	ctx := context.Background()
	db, err := app.OpenDB(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	actionFailures, err := agenteval.DiscoverActionResultFailures(ctx, db.Pool(), *limit)
	if err != nil {
		return fmt.Errorf("discover Action-level failures: %w", err)
	}
	runFailures, err := agenteval.DiscoverTerminalRunFailures(ctx, db.Pool(), *limit)
	if err != nil {
		return fmt.Errorf("discover terminal Run failures: %w", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(struct {
		ActionResultFailures []agenteval.ActionFailureCandidate `json:"action_result_failures"`
		TerminalRunFailures  []agenteval.TerminalRunFailure     `json:"terminal_run_failures"`
	}{ActionResultFailures: actionFailures, TerminalRunFailures: runFailures})
}

func runSuite(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("agent-eval run", flag.ContinueOnError)
	databaseURL := flags.String("database-url", "", "PostgreSQL URL")
	bifrostURL := flags.String("bifrost-url", "http://127.0.0.1:56666", "Bifrost model gateway URL")
	agentReleaseFlag := flags.String("agent-release", defaultAgentRelease, "Agent Catalog release identity@version")
	suitePath := flags.String("suite", "", "path to a Decision Suite JSON file")
	qdrantURL := flags.String("qdrant-url", "http://127.0.0.1:56333", "Qdrant base URL")
	qdrantAPIKey := flags.String("qdrant-api-key", "", "Qdrant API key")
	qdrantCollection := flags.String("qdrant-collection", "", "Qdrant collection name")
	qdrantDenseDimensions := flags.Int("qdrant-dense-dimensions", 768, "Qdrant dense vector dimensions")
	braveSearchAPIKey := flags.String("brave-search-api-key", "", "Brave Search API key (omit to match a search-not-configured deployment)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*databaseURL) == "" || strings.TrimSpace(*suitePath) == "" {
		return errors.New("-database-url and -suite are required")
	}
	agentRelease, err := agentcatalog.ParseReference(*agentReleaseFlag)
	if err != nil {
		return fmt.Errorf("parse -agent-release: %w", err)
	}
	var suite agenteval.DecisionSuite
	if err := decodeStrictFile(*suitePath, &suite); err != nil {
		return fmt.Errorf("load Decision Suite: %w", err)
	}
	if err := suite.Validate(); err != nil {
		return fmt.Errorf("Decision Suite invalid: %w", err)
	}

	ctx := context.Background()
	db, err := app.OpenDB(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	definitionCatalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("load Agent Catalog: %w", err)
	}
	activeRelease, err := app.VerifyAgentCatalogReady(ctx, db, definitionCatalog, agentRelease)
	if err != nil {
		return fmt.Errorf("Agent Catalog readiness: %w", err)
	}
	chatDefinition, ok := activeRelease.Roots["chat"]
	if !ok {
		return errors.New("active Agent Catalog release has no chat root")
	}

	modelClient := models.NewBifrostClient(*bifrostURL, &http.Client{}, 2048)

	var searchProvider websearch.Provider = notConfiguredWebSearchProvider{}
	if strings.TrimSpace(*braveSearchAPIKey) != "" {
		searchProvider, err = websearch.NewBraveProvider(websearch.BraveConfig{
			APIKey:     *braveSearchAPIKey,
			HTTPClient: &http.Client{Timeout: 10 * time.Second},
		})
		if err != nil {
			return fmt.Errorf("Brave Web Search provider: %w", err)
		}
	}

	qdrant, err := qdrantstore.New(qdrantstore.Config{
		BaseURL: *qdrantURL, APIKey: *qdrantAPIKey, Collection: *qdrantCollection,
		DenseDimensions: *qdrantDenseDimensions, RequestTimeout: 10 * time.Second,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	})
	if err != nil {
		return fmt.Errorf("Qdrant projection Store: %w", err)
	}

	runtime := agent.NewPostgresRuntime(db.Pool(), agent.BareSystemPrompt, nil)
	evidenceSearch := agent.NewEvidenceSearchService(db.Pool(), qdrant, modelClient)
	calculateTool := agent.NewCalculateAction()
	currentTimeTool := agent.NewCurrentTimeAction(nil)
	searchEvidenceTool := agent.NewSearchEvidenceAction(evidenceSearch)
	discoverSourcesTool := agent.NewDiscoverSourcesAction(
		agent.NewPostgresDiscoverSourcesBackend(db.Pool(), searchProvider),
		agent.ResearchAvailabilityFrom(searchProvider),
	)
	webSearchTool := agent.NewWebSearchAction(searchProvider)
	mcpToolRegistrations := []agent.MCPToolRegistration{
		{Action: calculateTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		{Action: currentTimeTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		{Action: discoverSourcesTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		{Action: searchEvidenceTool, Scheduling: agentcatalog.ToolParallel, CrashReplaySafe: true},
		{Action: webSearchTool, Scheduling: agentcatalog.ToolOrderedSync, CrashReplaySafe: true},
	}
	configuredDelegationTools, err := agent.NewConfiguredDelegationToolRegistrations(
		definitionCatalog, db.Pool(), agent.ResearchAvailabilityFrom(searchProvider))
	if err != nil {
		return fmt.Errorf("configured Delegation Tools: %w", err)
	}
	mcpToolRegistrations = append(mcpToolRegistrations, configuredDelegationTools...)
	mcpToolRegistry, err := agent.NewMCPToolRegistry(mcpToolRegistrations...)
	if err != nil {
		return fmt.Errorf("MCP Tool Registry: %w", err)
	}
	mcpToolHost, err := agent.NewMCPToolHost(definitionCatalog, mcpToolRegistry, runtime)
	if err != nil {
		return fmt.Errorf("MCP Tool Host: %w", err)
	}

	executor := agenteval.NewDecisionReplayExecutor(db.Pool(), runtime, mcpToolHost, chatDefinition, modelClient)

	failed := false
	for _, evalCase := range suite.Cases {
		observation, err := executor.ExecuteCase(ctx, evalCase)
		if err != nil {
			failed = true
			fmt.Fprintf(output, "ERROR %s: %v\n", evalCase.ID, err)
			continue
		}
		switch {
		case observation.Skipped:
			fmt.Fprintf(output, "SKIP  %s: %s\n", observation.CaseID, observation.Reason)
		case observation.Pass:
			fmt.Fprintf(output, "PASS  %s\n", observation.CaseID)
		default:
			failed = true
			fmt.Fprintf(output, "FAIL  %s: %s (actual action=%q input=%s)\n",
				observation.CaseID, observation.Reason, observation.ActualActionName, observation.ActualActionInput)
		}
	}
	if failed {
		return errors.New("one or more Decision Cases failed")
	}
	return nil
}

func decodeStrictFile(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("file has trailing JSON")
	}
	return nil
}

type notConfiguredWebSearchProvider struct{}

func (notConfiguredWebSearchProvider) ResearchAvailable() bool { return false }

func (notConfiguredWebSearchProvider) Search(context.Context, websearch.Request) ([]websearch.Candidate, error) {
	return nil, websearch.ErrNotConfigured
}
