package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

type fakeResearchClaimBackend struct {
	tree    researchClaimTree
	sources map[string]researchClaimSourceText
}

func (b fakeResearchClaimBackend) ClaimTree(context.Context, string) (researchClaimTree, error) {
	return b.tree, nil
}

func (b fakeResearchClaimBackend) SourceText(_ context.Context, sourceID, _ string) (researchClaimSourceText, bool, error) {
	source, ok := b.sources[sourceID]
	return source, ok, nil
}

func claimTestAction(t *testing.T, decision, index int, name string, input, output any) AcceptedAction {
	t.Helper()
	action := AcceptedAction{ActionID: claimTestActionID(decision, index), Index: index, Name: name, Input: mustClaimJSON(t, input)}
	if output != nil {
		action.Result = &ActionResult{Status: ActionSucceeded, Output: mustClaimJSON(t, output)}
	}
	return action
}

func claimTestActionID(decision, index int) string {
	return "decision:" + strconv.Itoa(decision) + "/action:" + strconv.Itoa(index)
}

func mustClaimJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestMatchClaimQuoteIgnoresFormattingButNotWording(t *testing.T) {
	source := "## Results\n\nOn **HotpotQA**, ReAct outperforms\nvanilla action generation, and the [patch](https://example.com/x) attack reduces success by up to 100%.\n\n实验表明，该方法在 ALFWorld 上提升了 34 个百分点。"
	cases := []struct {
		name, quote, status string
	}{
		{"whitespace and markdown", "On HotpotQA, ReAct outperforms vanilla action generation", claimStatusVerified},
		{"ellipsis", "ReAct outperforms ... reduces success by up to 100%", claimStatusVerified},
		{"full width and chinese punctuation", "该方法在ALFWorld上提升了３４个百分点", claimStatusVerified},
		{"one word changed", "On HotpotQA, ReAct underperforms vanilla action generation", claimStatusNearMatch},
		{"fabricated", "The method eliminates hallucination on every benchmark", claimStatusNotFound},
		{"too short", "ReAct", claimStatusNotFound},
		{"ellipsis out of order", "reduces success by up to 100% ... ReAct outperforms", claimStatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match := matchClaimQuote(source, tc.quote)
			if match.Status != tc.status {
				t.Fatalf("status=%s want=%s (nearest=%q)", match.Status, tc.status, match.Nearest)
			}
		})
	}
	if match := matchClaimQuote(source, "On HotpotQA, ReAct underperforms vanilla action generation"); !strings.Contains(match.Nearest, "outperforms") {
		t.Fatalf("near match should return the real wording, got %q", match.Nearest)
	}
}

func TestResearchClaimIDCountsProposalsInDecisionOrder(t *testing.T) {
	run := researchClaimRun{RunID: "run_root", Prefix: CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{
			{ActionID: "decision:1/action:0", Name: "read_url"},
			{ActionID: "decision:1/action:1", Name: recordClaimActionName},
		}},
		{DecisionNo: 2, Actions: []AcceptedAction{
			{ActionID: "decision:2/action:0", Name: recordClaimActionName},
			{ActionID: "decision:2/action:1", Name: recordClaimActionName},
		}},
	}}}
	for actionID, want := range map[string]string{
		"decision:1/action:1": "c1", "decision:2/action:0": "c2", "decision:2/action:1": "c3",
	} {
		if got := researchClaimID(run, actionID); got != want {
			t.Fatalf("%s id=%s want=%s", actionID, got, want)
		}
	}
	child := run
	child.RunID, child.Namespace = "run_child", researchClaimNamespace("run_child")
	if got := researchClaimID(child, "decision:2/action:1"); got != child.Namespace+"-c3" || !researchClaimIDPattern.MatchString(got) {
		t.Fatalf("child id=%s", got)
	}
}

func TestRecordClaimVerifiesAgainstInlineAndPagedReadURLResults(t *testing.T) {
	inline := readURLOutput{Title: "ReAct", FinalURL: "https://arxiv.org/abs/2210.03629", Markdown: "ReAct prompts LLMs to generate both reasoning traces and task-specific actions in an interleaved manner."}
	pagedBody, _ := json.Marshal(readURLOutput{Title: "Long page", FinalURL: "https://example.com/long", Markdown: "Intro text.\n\nThe patch attack is applied at inference time to the camera view, not during fine-tuning."})
	split := len(pagedBody) / 2
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{
			claimTestAction(t, 1, 0, "read_url", readURLInput{URL: "https://arxiv.org/abs/2210.03629/"}, inline),
			claimTestAction(t, 1, 1, "read_url", readURLInput{URL: "https://example.com/long"}, ToolResultProjection{
				ContentState: ToolResultExternalized, Preview: string(pagedBody[:split]), ResultRef: "tr_abcdefghijklmnop", ResultBytes: len(pagedBody), NextOffset: split,
			}),
		}},
		{DecisionNo: 2, Actions: []AcceptedAction{
			claimTestAction(t, 2, 0, "read_tool_result", readToolResultInput{ResultRef: "tr_abcdefghijklmnop", Offset: split}, ToolResultPage{
				ResultRef: "tr_abcdefghijklmnop", Offset: split, Content: string(pagedBody[split:]), Complete: true,
			}),
		}},
		{DecisionNo: 3, Actions: []AcceptedAction{
			{ActionID: "decision:3/action:0", Index: 0, Name: recordClaimActionName},
			{ActionID: "decision:3/action:1", Index: 1, Name: recordClaimActionName},
			{ActionID: "decision:3/action:2", Index: 2, Name: recordClaimActionName},
		}},
	}}
	action := &recordClaimAction{backend: fakeResearchClaimBackend{tree: researchClaimTree{Runs: []researchClaimRun{{RunID: "run_root", Prefix: prefix}}}}}
	cases := []struct {
		actionID string
		input    recordClaimInput
		id       string
		status   string
		url      string
	}{
		{"decision:3/action:0", recordClaimInput{Source: "https://arxiv.org/abs/2210.03629", Quote: "generate both reasoning traces and task-specific actions in an interleaved manner", Claim: "ReAct 交错生成推理与行动"}, "c1", claimStatusVerified, "https://arxiv.org/abs/2210.03629"},
		{"decision:3/action:1", recordClaimInput{Source: "https://example.com/long", Quote: "applied at inference time to the camera view, not during fine-tuning", Claim: "补丁攻击发生在推理时"}, "c2", claimStatusVerified, "https://example.com/long"},
		{"decision:3/action:2", recordClaimInput{Source: "https://example.com/unread", Quote: "anything at all that was never read", Claim: "x"}, "c3", claimStatusSourceUnavailable, "https://example.com/unread"},
	}
	for _, tc := range cases {
		result, err := action.Execute(context.Background(), ActionRequest{ActionID: tc.actionID, Input: mustClaimJSON(t, tc.input), Attempt: Attempt{RunID: "run_root"}})
		if err != nil || result.Status != ActionSucceeded {
			t.Fatalf("%s result=%+v err=%v", tc.actionID, result, err)
		}
		var output recordClaimOutput
		if err := json.Unmarshal(result.Output, &output); err != nil {
			t.Fatal(err)
		}
		if output.ID != tc.id || output.Status != tc.status || output.URL != tc.url {
			t.Fatalf("%s output=%+v", tc.actionID, output)
		}
	}
}

func TestRecordClaimVerifiesNotebookSourcesOnlyAfterSearchEvidence(t *testing.T) {
	searched := searchEvidenceResult{ResultVersion: SearchEvidenceResultVersion, Evidence: []searchEvidenceReference{{ChunkID: "chunk_1", SourceID: "src_paper", EvidenceRevisionID: "rev_1"}}}
	backend := fakeResearchClaimBackend{sources: map[string]researchClaimSourceText{
		"src_paper": {Title: "Reflexion", Text: "Reflexion does not update model weights; it stores verbal feedback in an episodic memory buffer."},
	}}
	input := recordClaimInput{Source: "src_paper", Quote: "does not update model weights", Claim: "Reflexion 不更新权重"}
	request := ActionRequest{ActionID: "decision:1/action:0", Input: mustClaimJSON(t, input), Attempt: Attempt{RunID: "run_root"}}

	backend.tree = researchClaimTree{Runs: []researchClaimRun{{RunID: "run_root"}}}
	result, err := (&recordClaimAction{backend: backend}).Execute(context.Background(), request)
	if err != nil || !strings.Contains(string(result.Output), claimStatusSourceUnavailable) {
		t.Fatalf("unsearched source result=%s err=%v", result.Output, err)
	}

	search := AcceptedAction{ActionID: "decision:0/action:0", Name: "search_evidence", Result: &ActionResult{Status: ActionSucceeded, Output: mustClaimJSON(t, searched)}}
	backend.tree = researchClaimTree{Runs: []researchClaimRun{{RunID: "run_root", Prefix: CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{search}}}}}}}
	if _, err := decodeSearchEvidenceResult(search.Result.Output); err != nil {
		t.Skipf("fixture is not a valid search_evidence manifest: %v", err)
	}
	result, err = (&recordClaimAction{backend: backend}).Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var output recordClaimOutput
	_ = json.Unmarshal(result.Output, &output)
	if output.Status != claimStatusVerified || output.Title != "Reflexion" {
		t.Fatalf("searched source output=%+v", output)
	}
}

func TestRenderResearchClaimCitationsNumbersSourcesAndDropsUnknownIDs(t *testing.T) {
	claims := []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "c1", URL: "https://a.example/paper", Status: claimStatusVerified}},
		{recordClaimOutput: recordClaimOutput{ID: "c2", URL: "https://b.example/post", Status: claimStatusNearMatch}},
		{recordClaimOutput: recordClaimOutput{ID: "c3", URL: "https://a.example/paper", Status: claimStatusVerified}},
		{recordClaimOutput: recordClaimOutput{ID: "ab12-c1", Source: "src_file", Title: "Uploaded notes", Status: claimStatusVerified}},
	}
	report := "First [c2]. Second [c1, c3]. Third [c9]. Child [ab12-c1]. Mixed [c3，c9]. Keep [c1](https://a.example/paper)."
	rendered, stats := renderResearchClaimCitations(report, claims)
	want := "First [1](https://b.example/post). Second [2](https://a.example/paper). Third . Child [Uploaded notes]. Mixed [2](https://a.example/paper). Keep [c1](https://a.example/paper)."
	if rendered != want {
		t.Fatalf("rendered=%q\nwant    =%q", rendered, want)
	}
	if stats.Cited != 5 || len(stats.Unknown) != 2 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestClaimsMarkdownListsCardsFromRootAndChildren(t *testing.T) {
	root := CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{
		claimTestAction(t, 1, 0, recordClaimActionName, recordClaimInput{Source: "https://a.example", Quote: "q one", Claim: "claim one", Conditions: "LIBERO only"},
			recordClaimOutput{ID: "c1", Status: claimStatusVerified, Source: "https://a.example", Title: "Paper A", URL: "https://a.example"}),
	}}}}
	child := CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{
		claimTestAction(t, 1, 0, recordClaimActionName, recordClaimInput{Source: "https://b.example", Quote: "q two", Claim: "claim two"},
			recordClaimOutput{ID: "ab12-c1", Status: claimStatusNotFound, Source: "https://b.example"}),
	}}}}
	claims := collectResearchClaims(researchClaimTree{Runs: []researchClaimRun{{RunID: "r", Prefix: root}, {RunID: "c", Namespace: "ab12", Prefix: child}}})
	markdown := renderResearchClaimsMarkdown(claims)
	for _, want := range []string{"- [c1] (verified) claim one", "Paper A — https://a.example", "Conditions: LIBERO only", "- [ab12-c1] (not_found) claim two"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("claims.md missing %q:\n%s", want, markdown)
		}
	}
}

func TestUnescapeClaimJSONFragmentToleratesTruncatedEscapes(t *testing.T) {
	if got := unescapeClaimJSONFragment(`a\"b\nc\u4e2d\u00`); got != `a"b c中` {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeRecordClaimInputRejectsEmptyFields(t *testing.T) {
	for _, raw := range []string{`{"source":"","quote":"q","claim":"c"}`, `{"source":"s","quote":" ","claim":"c"}`, `{"source":"s","quote":"q","claim":"c","extra":1}`} {
		if _, err := decodeRecordClaimInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestRecordClaimDefinitionRegisters(t *testing.T) {
	if _, err := NewActionRegistry(&recordClaimAction{}); err != nil {
		t.Fatal(err)
	}
}

