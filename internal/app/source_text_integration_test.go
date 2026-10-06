package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

type sourceTextIntegrationOutput struct {
	TotalMatches int  `json:"total_matches"`
	Truncated    bool `json:"truncated"`
	Matches      []struct {
		SourceID string `json:"source_id"`
		UnitID   string `json:"unit_id"`
		Page     int    `json:"page"`
		Offset   *int   `json:"offset"`
		Snippet  string `json:"snippet"`
	} `json:"matches"`
	Sources []struct {
		SourceID           string `json:"source_id"`
		EvidenceRevisionID string `json:"evidence_revision_id"`
		Matches            int    `json:"matches"`
	} `json:"sources"`
	Source struct {
		SourceID           string `json:"source_id"`
		EvidenceRevisionID string `json:"evidence_revision_id"`
	} `json:"source"`
	Units []struct {
		UnitID string `json:"unit_id"`
		Page   int    `json:"page"`
		Text   string `json:"text"`
	} `json:"units"`
	Complete bool            `json:"complete"`
	Next     json.RawMessage `json:"next"`
}

// TestSourceTextToolsRunAgainstAPinnedRunEndToEnd admits a real chat turn
// with two selected Sources, claims its job, and drives search_text and
// read_source through the production SourceTextService under that live
// lease, including a read_url result externalized to the real Redis cache.
func TestSourceTextToolsRunAgainstAPinnedRunEndToEnd(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "source-text@example.com")
	notebookID, chatID := createNotebookAndChatForEvidenceSet(t, api, sessionCookie, csrfCookie)
	installReadyEvidenceSetFixture(t, api, notebookID, "src_text_a", "evr_text_a", "src_text_b", "evr_text_b")
	insertUnit := func(id, revisionID, sourceID string, ordinal int, text string, page int) {
		t.Helper()
		coordinate := any(nil)
		if page > 0 {
			coordinate = fmt.Sprintf(`{"kind":"pdf_region","page":%d,"x":72,"y":700,"width":180,"height":14}`, page)
		}
		if _, err := api.db.Pool().Exec(ctx, `
			insert into source_evidence_units(
				id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune,coordinate_json
			) values($1,$2,$3,$4,$5,'paragraph',$6,0,char_length($6),$7::jsonb)
		`, id, revisionID, sourceID, notebookID, ordinal, text, coordinate); err != nil {
			t.Fatal(err)
		}
	}
	for page := 1; page <= 4; page++ {
		text := fmt.Sprintf("Page %d discusses the method in general terms.", page)
		if page == 3 {
			text = "Table 3 reports 41.2 BLEU, as noted in [23]."
		}
		insertUnit(fmt.Sprintf("unit_text_a_%d", page), "evr_text_a", "src_text_a", page-1, text, page)
	}
	insertUnit("unit_text_b_0", "evr_text_b", "src_text_b", 0, "An unrelated note that also cites [23].", 0)
	for _, statement := range []string{
		`insert into source_sources(id,notebook_id,input_kind,format,title,media_type,byte_size,content_sha256,original_object_key,state)
			values('src_text_unpinned',$1,'file','txt','Unselected','text/plain',4,repeat('f',64),'sources/src_text_unpinned/original','ready')`,
		`insert into source_evidence_revisions(id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,artifact_object_key,artifact_sha256,status,activated_at)
			values('evr_text_unpinned','src_text_unpinned',$1,1,'extract-v1','nano.normalized-source.v1','sources/src_text_unpinned/evidence/evr_text_unpinned',repeat('c',64),'active',now())`,
		`insert into retrieval_source_index_builds(revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at)
			values('evr_text_unpinned','riv_pin_active','src_text_unpinned',$1,1,repeat('d',64),'verified',now())`,
	} {
		if _, err := api.db.Pool().Exec(ctx, statement, notebookID); err != nil {
			t.Fatal(err)
		}
	}
	insertUnit("unit_text_unpinned", "evr_text_unpinned", "src_text_unpinned", 0, "Unselected Table 3 text.", 1)

	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c0a1", "content": "What does Table 3 report?",
		"source_ids": []string{"src_text_a", "src_text_b"},
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status=%d body=%s", response.Code, response.Body.String())
	}
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	var userID string
	if err := api.db.Pool().QueryRow(ctx, `select creator_user_id from chat_chats where id=$1`, chatID).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	var toolResults *agent.ToolResultReader
	var redisStore *agent.RedisToolResultStore
	if redisURL := strings.TrimSpace(os.Getenv("NANO_TEST_REDIS_URL")); redisURL != "" {
		redisStore, err = agent.NewRedisToolResultStore(agent.RedisToolResultStoreConfig{
			URL: redisURL, KeyPrefix: "nano:test-source-text:", OperationTimeout: 2 * time.Second, MaximumValueBytes: 1 << 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer redisStore.Close()
		toolResults = &agent.ToolResultReader{Store: redisStore, MaximumPageBytes: 4096, MaximumOutputBytes: 4096}
	}
	service := agent.NewSourceTextService(api.db.Pool(), objectstore.NewMemoryStore(), toolResults)
	searchText := agent.NewSearchTextAction(service)
	readSource := agent.NewReadSourceAction(service)
	execute := func(action agent.Action, definition, input string) agent.ActionResult {
		t.Helper()
		if err := action.ValidateInput(json.RawMessage(input)); err != nil {
			t.Fatalf("validate %s: %v", input, err)
		}
		result, err := action.Execute(ctx, agent.ActionRequest{
			ActionID: "decision:1/action:0", UserID: userID, ChatID: chatID, Input: json.RawMessage(input),
			Attempt: attemptFromClaim(claimed), Definition: agentcatalog.MustParseReference(definition),
		})
		if err != nil {
			t.Fatalf("execute %s: %v", input, err)
		}
		return result
	}
	decode := func(result agent.ActionResult) sourceTextIntegrationOutput {
		t.Helper()
		var output sourceTextIntegrationOutput
		if result.Status != agent.ActionSucceeded || json.Unmarshal(result.Output, &output) != nil {
			t.Fatalf("result=%+v output=%s", result, result.Output)
		}
		return output
	}

	everywhere := decode(execute(searchText, "chat.leader@7", `{"pattern":"[23]"}`))
	if everywhere.TotalMatches != 2 || len(everywhere.Sources) != 2 {
		t.Fatalf("all-Source search=%+v", everywhere)
	}
	table := decode(execute(searchText, "chat.leader@7", `{"pattern":"table 3"}`))
	if table.TotalMatches != 1 || table.Matches[0].UnitID != "unit_text_a_3" || table.Matches[0].Page != 3 ||
		table.Sources[0].EvidenceRevisionID != "evr_text_a" || !strings.Contains(table.Matches[0].Snippet, "«Table 3»") {
		t.Fatalf("pinned search must ignore the unselected Source: %+v", table)
	}

	around := decode(execute(readSource, "chat.leader@7", `{"source_id":"src_text_a","unit_id":"unit_text_a_3","before":1,"after":0}`))
	if around.Source.EvidenceRevisionID != "evr_text_a" || len(around.Units) != 2 || around.Units[1].Text != "Table 3 reports 41.2 BLEU, as noted in [23]." || !around.Complete {
		t.Fatalf("read around hit=%+v", around)
	}
	pages := decode(execute(readSource, "research.executor@39", `{"source_id":"src_text_a","page_start":2,"page_end":4}`))
	if len(pages.Units) != 3 || pages.Units[0].Page != 2 || pages.Units[2].UnitID != "unit_text_a_4" {
		t.Fatalf("page range=%+v", pages)
	}
	for _, input := range []string{
		`{"source_id":"src_text_unpinned","page_start":1,"page_end":1}`,
		`{"source_id":"src_text_a","unit_id":"unit_text_unpinned"}`,
		`{"source_id":"src_text_a","entry_id":"entry_without_map"}`,
	} {
		if result := execute(readSource, "chat.leader@7", input); result.Status != agent.ActionDomainError || result.ErrorCode != "evidence_scope_unavailable" {
			t.Fatalf("%s result=%+v", input, result)
		}
	}
	if result := execute(searchText, "chat.leader@7", `{"pattern":"Table","source_id":"src_text_unpinned"}`); result.Status != agent.ActionDomainError || result.ErrorCode != "evidence_scope_unavailable" {
		t.Fatalf("unpinned search result=%+v", result)
	}
	if result := execute(readSource, "chat.leader@7", `{"source_id":"src_text_b","page_start":1,"page_end":1}`); result.Status != agent.ActionDomainError || result.ErrorCode != "page_range_unsupported" {
		t.Fatalf("non-PDF page range result=%+v", result)
	}

	if redisStore == nil {
		t.Log("NANO_TEST_REDIS_URL is unset; skipping the externalized read_url leg")
		return
	}
	markdown := strings.Repeat("Background paragraph about the system design.\n", 120) + "Table 7 lists the measured latency.\n" + strings.Repeat("Closing remark.\n", 40)
	readURLOutput, err := json.Marshal(map[string]any{"title": "Long Page", "final_url": "https://example.com/long", "markdown": markdown, "engine": "fixture", "word_count": 900, "truncated": false})
	if err != nil {
		t.Fatal(err)
	}
	externalizer := &agent.ToolResultExternalizer{Store: redisStore, Policy: agent.ToolResultCachePolicy{TTL: 10 * time.Minute, MaximumValueBytes: 1 << 20}}
	externalized, state := externalizer.Externalize(ctx, agent.ToolResultScope{
		UserID: userID, ChatID: chatID, RunID: claimed.RunID, ActionID: "decision:0/action:0", ToolName: "read_url",
	}, agent.ActionResult{Status: agent.ActionSucceeded, Output: readURLOutput}, 1024)
	var projection agent.ToolResultProjection
	if state.Err != nil || json.Unmarshal(externalized.Output, &projection) != nil || projection.ResultRef == "" {
		t.Fatalf("externalized=%s state=%+v", externalized.Output, state)
	}
	defer redisStore.Delete(ctx, projection.ResultRef)
	hits := decode(execute(searchText, "research.executor@39", `{"pattern":"Table 7","result_ref":"`+projection.ResultRef+`"}`))
	if hits.TotalMatches != 1 || hits.Matches[0].Offset == nil {
		t.Fatalf("result_ref search=%+v", hits)
	}
	page, err := toolResults.Read(ctx, agent.ToolResultScope{UserID: userID, ChatID: chatID, RunID: claimed.RunID}, projection.ResultRef, *hits.Matches[0].Offset, 256)
	if err != nil || !strings.HasPrefix(page.Content, "Table 7 lists the measured latency.") {
		t.Fatalf("read_tool_result at offset %d = %q err=%v", *hits.Matches[0].Offset, page.Content, err)
	}
	foreign := agent.ActionRequest{
		ActionID: "decision:1/action:0", UserID: userID, ChatID: chatID, Attempt: agent.Attempt{RunID: "run_other"},
		Input: json.RawMessage(`{"pattern":"Table 7","result_ref":"` + projection.ResultRef + `"}`), Definition: agentcatalog.MustParseReference("research.executor@39"),
	}
	if result, err := searchText.Execute(ctx, foreign); err != nil || result.Status != agent.ActionDomainError || result.ErrorCode != "tool_result_unauthorized" {
		t.Fatalf("foreign Run result=%+v err=%v", result, err)
	}
}
