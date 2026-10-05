package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

func TestResearchRunStartsOneScoutPerPlanQuestionOnce(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	claimed, _, _, _ := admitResearchExecutionForRelease(t, api, "scouts@example.com", "nano.default@42")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFromClaim(claimed)
	execution, err := runtime.Load(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < 2; start++ {
		if err := runtime.StartRun(ctx, attempt, execution); err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
	}
	var count int
	var actionID, taskName, message, childID string
	if err := api.db.Pool().QueryRow(ctx, `select count(*) over (),action_id,task_name,message,child_run_id from agent_subagents where parent_run_id=$1`, claimed.RunID).
		Scan(&count, &actionID, &taskName, &message, &childID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || actionID != "scout:1" || taskName != "Scout: Q1" || !strings.Contains(message, "- Q1: How do the loops differ?") {
		t.Fatalf("count=%d action=%q task=%q message=%q", count, actionID, taskName, message)
	}
	prefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	request, err := runtime.BuildDecisionRequest(ctx, execution, prefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	if system := request.Messages[0].Content; !strings.Contains(system, "Scouts: the runtime started") || !strings.Contains(system, "Scout: Q1: "+childID) {
		t.Fatalf("system prompt lacks scouts: %s", system)
	}
}

func TestResearchRunBeforeExecutorV29StartsNoScouts(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	claimed, _, _, _ := admitResearchExecutionForRelease(t, api, "no-scouts@example.com", "nano.default@41")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(ctx, attemptFromClaim(claimed))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.StartRun(ctx, attemptFromClaim(claimed), execution); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_subagents where parent_run_id=$1`, claimed.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("children=%d err=%v", count, err)
	}
}
