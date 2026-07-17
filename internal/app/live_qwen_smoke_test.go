package app_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestLiveQwenThroughBifrostUsesBothSprint3ActionsAndPublishesOnce(t *testing.T) {
	if os.Getenv("NANO_QWEN_SMOKE") != "1" {
		t.Skip("set NANO_QWEN_SMOKE=1 through scripts/test-sprint3-qwen")
	}
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "live-qwen-sprint3@example.com")
	const inputMessageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c086"
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": inputMessageID,
		"content": "You must use both available Actions before answering. First call current_time for UTC and Asia/Shanghai. " +
			"Then call calculate to divide 28800 by 3600. Finally answer in one concise sentence.",
		"time_zone": "Asia/Shanghai",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admission status=%d", admitted.Code)
	}
	var admittedBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admitted, &admittedBody)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim unavailable: ok=%t err=%v", ok, err)
	}
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction(), agent.NewCurrentTimeAction(nil))
	if err != nil {
		t.Fatal(err)
	}
	bifrostURL := os.Getenv("NANO_BIFROST_URL")
	if bifrostURL == "" {
		bifrostURL = "http://127.0.0.1:56666"
	}
	model := models.NewBifrostClient(bifrostURL, &http.Client{Timeout: 90 * time.Second}, 2048)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_live_qwen_sprint3" })
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatalf("live Controller failed safely: %v", err)
	}

	rows, err := api.db.Pool().Query(ctx, `
		select action->>'name'
		from agent_run_checkpoints c,
			jsonb_array_elements(c.payload->'actions') with ordinality as item(action, action_order)
		where c.run_id = $1 and c.kind = 'action_proposal'
		order by c.sequence_no, action_order`, admittedBody.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actionNames := make([]string, 0, 8)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		actionNames = append(actionNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	currentTimeIndex, calculateIndex := -1, -1
	for index, name := range actionNames {
		if name == "current_time" && currentTimeIndex < 0 {
			currentTimeIndex = index
		}
		if name == "calculate" && calculateIndex < 0 {
			calculateIndex = index
		}
	}
	if currentTimeIndex < 0 || calculateIndex <= currentTimeIndex {
		t.Fatalf("live model did not accept both Actions in required order; accepted names=%v", actionNames)
	}

	var runStatus, jobStatus string
	var assistants, finalDrafts int
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status
		from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1`, admittedBody.RunID).Scan(&runStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1 and role = 'assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1 and kind = 'final_draft'`, admittedBody.RunID).Scan(&finalDrafts); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || jobStatus != "succeeded" || assistants != 1 || finalDrafts != 1 {
		t.Fatalf("live terminal state=%s/%s assistants=%d final_drafts=%d", runStatus, jobStatus, assistants, finalDrafts)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), admittedBody.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var modelStarts, actionStarts, rootEnds int
	for _, record := range trace.Records {
		if record.Kind == agentobs.RecordSpanStarted && record.Name == semconv.ModelCall {
			modelStarts++
		}
		if record.Kind == agentobs.RecordSpanStarted && record.Name == semconv.AgentAction {
			actionStarts++
		}
		if record.Kind == agentobs.RecordSpanEnded && record.SpanID == trace.RootSpanID && record.Status == agentobs.StatusOK {
			rootEnds++
		}
	}
	if modelStarts < 2 || actionStarts < 2 || rootEnds != 1 {
		t.Fatalf("live Durable Trace model/action/root-end = %d/%d/%d", modelStarts, actionStarts, rootEnds)
	}
}
