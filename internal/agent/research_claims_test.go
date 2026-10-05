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
	child.RunID, child.Namespace = "run_child", researchClaimNamespace(1)
	if got := researchClaimID(child, "decision:2/action:1"); got != "b3" || !researchClaimIDPattern.MatchString(got) {
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

func TestCitationNumberCheckFlagsNumbersMissingFromCitedQuotes(t *testing.T) {
	claims := []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "c2"}, Quote: "Real-world queries vary widely: some need keyword matching, others need semantic search."},
		{recordClaimOutput: recordClaimOutput{ID: "c4"}, Quote: "compared with IRCoT, PAR2-RAG achieves up to 23.5% higher accuracy, with retrieval gains of up to 10.5% in NDCG."},
		{recordClaimOutput: recordClaimOutput{ID: "c9"}, Quote: "smaller chunks delivered 8ms faster retrieval and 4.2% higher relevance across 1,024 queries"},
	}
	report := strings.Join([]string{
		"# 迭代检索升级决策指南",
		"迭代检索升级的工程标准是：ROI > 1.2 且 P95 延迟增幅 < 150ms。",
		"| 维度 | 触发条件 | 来源 |",
		"|---|---|---|",
		"| 查询改写 | 模糊实体召回率 < 0.4 | [c2] |",
		"| PAR2-RAG | 相比 IRCoT 准确率最高提升 23.5%，NDCG 提升 10.5% | [c4] |",
		"小分块检索快 8 ms、相关性高 4.2%，共 1024 次查询。[c9] 2026 年 GPT-5 与 top-3 召回不计入核对。",
		"三种方法各有取舍 [c2]。",
	}, "\n")
	check := checkResearchCitationNumbers(report, claims)
	if len(check.UnsupportedNumbers) != 1 || check.UnsupportedNumbers[0].Numbers[0] != "0.4" || check.UnsupportedNumbers[0].Cards[0] != "c2" {
		t.Fatalf("unsupported=%+v", check.UnsupportedNumbers)
	}
	if len(check.UncitedNumbers) != 1 || strings.Join(check.UncitedNumbers[0].Numbers, ",") != "1.2,150" {
		t.Fatalf("uncited=%+v", check.UncitedNumbers)
	}
	if check.CheckedStatements != 4 {
		t.Fatalf("checked=%d", check.CheckedStatements)
	}
	if !strings.Contains(researchCitationCheckGuidance(check), "1 statement(s)") {
		t.Fatalf("guidance=%q", researchCitationCheckGuidance(check))
	}
	if guidance := researchCitationCheckGuidance(checkResearchCitationNumbers("相比 IRCoT 提升 23.5% [c4]。", claims)); guidance != "" {
		t.Fatalf("supported statement produced guidance %q", guidance)
	}
}

func TestResearchCitationStatementsKeepTrailingCitationsWithSentence(t *testing.T) {
	got := researchCitationStatements("第一句提升 12%。[c1] 第二句没有引用。Third sentence [c2, c3]. Last")
	want := []string{"第一句提升 12%。[c1]", " 第二句没有引用。", "Third sentence [c2, c3].", " Last"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestSourceCoverageCountsCardAndLinkCitations(t *testing.T) {
	claims := []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "c1", Source: "https://arxiv.org/abs/2310.11511", URL: "https://ar5iv.labs.arxiv.org/html/2310.11511"}},
	}
	sources := []researchReadSource{
		{URL: "https://arxiv.org/abs/2310.11511", FinalURL: "https://ar5iv.labs.arxiv.org/html/2310.11511", Title: "Self-RAG"},
		{URL: "https://example.com/blog/", Title: "Blog"},
		{URL: "https://example.com/unused", Title: "Unused"},
	}
	report := "Self-RAG critiques its own output [c1]. A blog agrees ([Blog](https://example.com/blog))."
	coverage := checkResearchSourceCoverage(report, claims, sources)
	if coverage.ReadSources != 3 || coverage.CitedSources != 2 || len(coverage.UncitedSample) != 1 || coverage.UncitedSample[0].Title != "Unused" {
		t.Fatalf("coverage=%+v", coverage)
	}
	if guidance := researchSourceCoverageGuidance(coverage, true); !strings.Contains(guidance, "2 of the 3") || !strings.Contains(guidance, "only 3 sources have been read") {
		t.Fatalf("guidance=%q", guidance)
	}
	coverage.UnreadLeads = []researchReadSource{{URL: "https://example.com/lead", Title: "Lead"}}
	if guidance := researchSourceCoverageGuidance(coverage, true); !strings.Contains(guidance, "unread_leads") {
		t.Fatalf("guidance without leads pointer=%q", guidance)
	}
	if guidance := researchSourceCoverageGuidance(researchSourceCoverage{ReadSources: 2, CitedSources: 2}, true); strings.Contains(guidance, "Source coverage") || !strings.Contains(guidance, "Source breadth") {
		t.Fatalf("fully cited narrow report guidance=%q", guidance)
	}
	if guidance := researchSourceCoverageGuidance(researchSourceCoverage{ReadSources: 2, CitedSources: 2}, false); guidance != "" {
		t.Fatalf("question-judged breadth still applied a source count: %q", guidance)
	}
	if guidance := researchSourceCoverageGuidance(researchSourceCoverage{ReadSources: 9, CitedSources: 9}, true); guidance != "" {
		t.Fatalf("broad, fully cited report produced guidance %q", guidance)
	}
}

func TestRunningSubagentsGuidance(t *testing.T) {
	if guidance := researchRunningSubagentsGuidance(nil); guidance != "" {
		t.Fatalf("guidance=%q", guidance)
	}
	if guidance := researchRunningSubagentsGuidance([]researchRunningSubagent{{AgentID: "run_a", TaskName: "Read: Paper"}}); !strings.Contains(guidance, "wait_agent") {
		t.Fatalf("guidance=%q", guidance)
	}
}

func TestCitationsResolveModelVariantsOfChildCardIDs(t *testing.T) {
	claims := []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "16d1-c3", URL: "https://arxiv.org/html/2305.06983"}},
		{recordClaimOutput: recordClaimOutput{ID: "433c-c4", URL: "https://example.com/mega-rag"}},
		{recordClaimOutput: recordClaimOutput{ID: "c12", URL: "https://example.com/root"}},
	}
	report := "FLARE +11.6 [c16d1-c3]. MEGA [433cc4] and [16d1-c3, c433c-c4]. Root [c12]. Invented [cRaR-1]."
	rendered, stats := renderResearchClaimCitations(report, claims)
	want := "FLARE +11.6 [1](https://arxiv.org/html/2305.06983). MEGA [2](https://example.com/mega-rag) and [1](https://arxiv.org/html/2305.06983)[2](https://example.com/mega-rag). Root [3](https://example.com/root). Invented [cRaR-1]."
	if rendered != want || stats.Cited != 5 {
		t.Fatalf("rendered=%q\nwant    =%q stats=%+v", rendered, want, stats)
	}
	if canonicalClaimID("c1234") != "c1234" {
		t.Fatal("root card id was rewritten")
	}
}

func TestChildCardLettersCiteAndRender(t *testing.T) {
	for index, want := range map[int]string{0: "a", 2: "d", 15: "q", 24: "z", 25: "aa", 26: "ab", 31: "ah", 49: "az", 50: "ba"} {
		if got := researchClaimNamespace(index); got != want {
			t.Fatalf("namespace(%d)=%q, want %q", index, got, want)
		}
	}
	seen := map[string]bool{"c": true}
	for index := 0; index < runtimeSubagentMaxTotal; index++ {
		namespace := researchClaimNamespace(index)
		if seen[namespace] || !researchClaimIDPattern.MatchString(namespace+"1") {
			t.Fatalf("namespace %d=%q collides or is not a card id", index, namespace)
		}
		seen[namespace] = true
	}
	claims := []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "a2", URL: "https://arxiv.org/html/2606.13905"}},
		{recordClaimOutput: recordClaimOutput{ID: "c1", URL: "https://example.com/root"}},
	}
	claims = append(claims, researchClaim{recordClaimOutput: recordClaimOutput{ID: "ab4", URL: "https://example.com/late"}})
	if late, _ := renderResearchClaimCitations("Late reader [ab4].", claims); late != "Late reader [1](https://example.com/late)." {
		t.Fatalf("two-letter card rendered %q", late)
	}
	rendered, stats := renderResearchClaimCitations("ADORE grades relevance [a2]. Both [a2, c1].", claims)
	if rendered != "ADORE grades relevance [1](https://arxiv.org/html/2606.13905). Both [1](https://arxiv.org/html/2606.13905)[2](https://example.com/root)." || stats.Cited != 3 {
		t.Fatalf("rendered=%q stats=%+v", rendered, stats)
	}
	if !strings.Contains(renderResearchClaimsMarkdown(claims), "never renumber") {
		t.Fatal("claims.md does not tell the model to cite exact ids")
	}
}

func TestSourceCoverageListsSourcesWithUncitedCards(t *testing.T) {
	card := func(id, url, status string) researchClaim {
		return researchClaim{recordClaimOutput: recordClaimOutput{ID: id, Source: url, URL: url, Title: url, Status: status}}
	}
	claims := []researchClaim{
		card("a1", "https://example.com/core", claimStatusVerified), card("a2", "https://example.com/core", claimStatusVerified),
		card("b1", "https://example.com/found", claimStatusVerified), card("b2", "https://example.com/found", claimStatusNearMatch),
		card("b3", "https://example.com/found", claimStatusVerified), card("b4", "https://example.com/found", claimStatusVerified),
		card("d1", "https://example.com/minor", claimStatusVerified), card("d2", "https://example.com/minor", claimStatusVerified),
		card("d3", "https://example.com/minor", claimStatusNotFound), card("d4", "https://example.com/minor", claimStatusNotFound),
	}
	coverage := checkResearchSourceCoverage("Core result [a1, a2]. One finding [b1].", claims, nil)
	if coverage.Cards != 8 || coverage.CitedCards != 3 {
		t.Fatalf("cards=%d cited=%d", coverage.Cards, coverage.CitedCards)
	}
	if len(coverage.UnusedCards) != 1 || coverage.UnusedCards[0].URL != "https://example.com/found" || coverage.UnusedCards[0].Cards != 4 || coverage.UnusedCards[0].Uncited != 3 {
		t.Fatalf("unused=%+v", coverage.UnusedCards)
	}
	coverage.ReadSources, coverage.CitedSources = researchBreadthTarget, researchBreadthTarget
	if guidance := researchSourceCoverageGuidance(coverage, true); !strings.Contains(guidance, "cites 3 of 8 usable claim cards") || !strings.Contains(guidance, "not a limit") {
		t.Fatalf("guidance=%q", guidance)
	}
}

func TestCardRangesCiteEveryCardBetweenTheirEnds(t *testing.T) {
	claims := []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "g2", URL: "https://example.com/a"}},
		{recordClaimOutput: recordClaimOutput{ID: "g3", URL: "https://example.com/a"}},
		{recordClaimOutput: recordClaimOutput{ID: "g4", URL: "https://example.com/b"}},
		{recordClaimOutput: recordClaimOutput{ID: "c1", URL: "https://example.com/c"}},
		{recordClaimOutput: recordClaimOutput{ID: "c2", URL: "https://example.com/d"}},
	}
	rendered, stats := renderResearchClaimCitations("Tokens [g2-g4]. Root [c1–2]. Mixed [g3, c1~c2]. Reversed [g4-g2].", claims)
	want := "Tokens [1](https://example.com/a)[2](https://example.com/b). Root [3](https://example.com/c)[4](https://example.com/d). Mixed [1](https://example.com/a)[3](https://example.com/c)[4](https://example.com/d). Reversed ."
	if rendered != want || stats.Cited != 8 {
		t.Fatalf("rendered=%q\nwant    =%q stats=%+v", rendered, want, stats)
	}
	if got := strings.Join(citedClaimIDs("aa1-3"), ","); got != "aa1,aa2,aa3" {
		t.Fatalf("two-letter range=%q", got)
	}
}

func TestClaimQuotesMatchArxivTextWithDuplicatedMath(t *testing.T) {
	source := `### 6.2 Why The Margin Is Necessary Under the canonical prompt, 96.5%96.5\% of verbalized confidence values are 55, yielding an entropy of 0.1820.182 nats (Figure 3). By construction the rule cannot fire at r\=1r{=}1, so the minimum cost is two LLM calls per question. We create supervised data by prompting GPT-4 to generate reflection tokens and then distill their knowledge into an in-house 𝒞\mathcal{C}. For each group of reflection tokens, we sample 55 instances.`
	for _, quote := range []string{
		"Under the canonical prompt, 96.5% of verbalized confidence values are 5, yielding an entropy of 0.182 nats",
		"By construction the rule cannot fire at r=1, so the minimum cost is two LLM calls per question.",
		"distill their knowledge into an in-house C.",
		"we sample 55 instances",
	} {
		if got := matchClaimQuote(source, quote); got.Status != claimStatusVerified {
			t.Fatalf("quote %q status=%s nearest=%q", quote, got.Status, got.Nearest)
		}
	}
	if got := matchClaimQuote(source, "Under the canonical prompt, 12.5% of verbalized confidence values are 3"); got.Status == claimStatusVerified {
		t.Fatalf("changed numbers verified: %+v", got)
	}
}
