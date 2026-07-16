package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type sprint3WireMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func TestSprint3FourLocationJourneyStaysWithinPinnedBudgetsAndReloadsOneAnswer(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "sprint3-journey@example.com")
	const inputMessageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c081"
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":        inputMessageID,
		"content":   "Compare the current time in UTC, Shanghai, London, and New York, including each offset from UTC.",
		"time_zone": "Asia/Shanghai",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admission status=%d body=%s", admitted.Code, admitted.Body.String())
	}
	var admittedBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admitted, &admittedBody)

	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || claimed.RunID != admittedBody.RunID {
		t.Fatalf("claim=%+v ok=%t err=%v", claimed, ok, err)
	}

	modelCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []sprint3WireMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch modelCalls {
		case 1:
			if len(request.Tools) != 2 || request.Tools[0].Function.Name != "calculate" || request.Tools[1].Function.Name != "current_time" || len(request.Messages) != 2 {
				t.Errorf("first request tools/messages=%+v/%+v", request.Tools, request.Messages)
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"provider-time-utc","type":"function","function":{"name":"current_time","arguments":"{\"time_zone\":\"UTC\"}"}},
				{"id":"provider-time-shanghai","type":"function","function":{"name":"current_time","arguments":"{\"time_zone\":\"Asia/Shanghai\"}"}},
				{"id":"provider-time-london","type":"function","function":{"name":"current_time","arguments":"{\"time_zone\":\"Europe/London\"}"}},
				{"id":"provider-time-new-york","type":"function","function":{"name":"current_time","arguments":"{\"time_zone\":\"America/New_York\"}"}}
			]},"finish_reason":"tool_calls"}]}`))
		case 2:
			assertWireActionRound(t, request.Messages, 2, 3, "decision:1/action:", 4)
			for _, zone := range []string{"UTC", "Asia/Shanghai", "Europe/London", "America/New_York"} {
				found := false
				for _, message := range request.Messages[3:7] {
					if strings.Contains(message.Content, `"time_zone":"`+zone+`"`) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("second request omitted accepted zone %q: %+v", zone, request.Messages)
					return
				}
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"provider-offset-utc","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"divide\",\"operands\":[\"0\",\"3600\"]}"}},
				{"id":"provider-offset-shanghai","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"divide\",\"operands\":[\"28800\",\"3600\"]}"}},
				{"id":"provider-offset-london","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"divide\",\"operands\":[\"3600\",\"3600\"]}"}},
				{"id":"provider-offset-new-york","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"divide\",\"operands\":[\"-14400\",\"3600\"]}"}}
			]},"finish_reason":"tool_calls"}]}`))
		case 3:
			if len(request.Tools) != 0 {
				t.Errorf("reserved final request still exposed Actions: %+v", request.Tools)
				return
			}
			assertWireActionRound(t, request.Messages, 7, 8, "decision:2/action:", 4)
			for _, value := range []string{"0", "8", "1", "-4"} {
				found := false
				for _, message := range request.Messages[8:12] {
					if strings.Contains(message.Content, `"value":"`+value+`"`) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("third request omitted calculated offset %q: %+v", value, request.Messages)
					return
				}
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"At the observed instant, Shanghai is UTC+8, London UTC+1, New York UTC-4, and UTC remains UTC+0."},"finish_reason":"stop"}]}`))
		default:
			t.Errorf("unexpected model call %d", modelCalls)
		}
	}))
	defer upstream.Close()

	fixedNow := time.Date(2026, time.July, 16, 12, 34, 56, 0, time.UTC)
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction(), agent.NewCurrentTimeAction(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_sprint3_journey" })
	controller := agent.NewController(runtime, models.NewBifrostClient(upstream.URL, upstream.Client(), 2048), registry)
	if err := controller.Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 3 {
		t.Fatalf("model calls=%d, want two Action decisions plus one reserved final decision", modelCalls)
	}

	var runStatus, jobStatus, outputID, answer string
	var attemptNo, actionDecisionLimit, finalDecisionLimit, actionLimit, actionBatchLimit int
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, r.output_message_id, m.content, j.attempt_no,
			r.action_decision_limit, r.final_decision_limit, r.action_limit, r.action_batch_limit
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		join chat_messages m on m.id = r.output_message_id
		where r.id = $1`, admittedBody.RunID).Scan(
		&runStatus, &jobStatus, &outputID, &answer, &attemptNo,
		&actionDecisionLimit, &finalDecisionLimit, &actionLimit, &actionBatchLimit,
	); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || jobStatus != "succeeded" || outputID != "msg_sprint3_journey" || attemptNo != 1 {
		t.Fatalf("terminal Run/Job=%s/%s output=%q attempt=%d", runStatus, jobStatus, outputID, attemptNo)
	}
	if actionDecisionLimit != 4 || finalDecisionLimit != 1 || actionLimit != 8 || actionBatchLimit != 4 {
		t.Fatalf("pinned budgets=%d+%d/%d/%d", actionDecisionLimit, finalDecisionLimit, actionLimit, actionBatchLimit)
	}
	if answer != "At the observed instant, Shanghai is UTC+8, London UTC+1, New York UTC-4, and UTC remains UTC+0." {
		t.Fatalf("answer=%q", answer)
	}

	rows, err := api.db.Pool().Query(ctx, `select kind, payload from agent_run_checkpoints where run_id = $1 order by sequence_no`, admittedBody.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := make([]string, 0, 11)
	proposalWidths := make([]int, 0, 2)
	for rows.Next() {
		var kind string
		var payload []byte
		if err := rows.Scan(&kind, &payload); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
		if kind == string(agent.CheckpointActionProposal) {
			var proposal struct {
				Actions []json.RawMessage `json:"actions"`
			}
			if err := json.Unmarshal(payload, &proposal); err != nil {
				t.Fatal(err)
			}
			proposalWidths = append(proposalWidths, len(proposal.Actions))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{
		"action_proposal", "action_result", "action_result", "action_result", "action_result",
		"action_proposal", "action_result", "action_result", "action_result", "action_result",
		"final_draft",
	}
	if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") || len(proposalWidths) != 2 || proposalWidths[0] != 4 || proposalWidths[1] != 4 {
		t.Fatalf("checkpoint kinds/widths=%v/%v", kinds, proposalWidths)
	}

	snapshot := api.getWithCookie(t, "/api/v1/chats/"+chatID, sessionCookie)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", snapshot.Code, snapshot.Body.String())
	}
	var snapshotBody struct {
		Messages []struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	decodeBody(t, snapshot, &snapshotBody)
	if len(snapshotBody.Messages) != 2 || snapshotBody.Messages[1].ID != outputID || snapshotBody.Messages[1].Role != "assistant" || snapshotBody.Messages[1].Content != answer || len(snapshotBody.Runs) != 1 || snapshotBody.Runs[0].Status != "completed" {
		t.Fatalf("reloaded snapshot=%+v", snapshotBody)
	}
	for _, internal := range []string{"answer_mode", "action_proposal", "action_result", "final_draft", "decision:1/action:0"} {
		if strings.Contains(snapshot.Body.String(), internal) {
			t.Fatalf("snapshot exposed internal %q: %s", internal, snapshot.Body.String())
		}
	}
}

func assertWireActionRound(t *testing.T, messages []sprint3WireMessage, proposalIndex, resultStart int, idPrefix string, width int) {
	t.Helper()
	if len(messages) != resultStart+width || len(messages[proposalIndex].ToolCalls) != width {
		t.Errorf("model messages have wrong round shape: %+v", messages)
		return
	}
	for index := 0; index < width; index++ {
		wantID := idPrefix + string(rune('0'+index))
		if messages[proposalIndex].ToolCalls[index].ID != wantID || messages[resultStart+index].Role != "tool" || messages[resultStart+index].ToolCallID != wantID {
			t.Errorf("Action %d stable ID mapping=%+v/%+v, want %q", index, messages[proposalIndex].ToolCalls[index], messages[resultStart+index], wantID)
			return
		}
	}
}
