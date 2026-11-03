package agent

import (
	"strings"
	"testing"
)

func TestQuestionCoverageCountsCardsAndSourcesPerQuestion(t *testing.T) {
	card := func(id, url string, question int) researchClaim {
		return researchClaim{recordClaimOutput: recordClaimOutput{ID: id, URL: url, Source: url, Status: claimStatusVerified}, Question: question}
	}
	questions := []string{"When does iteration pay off?", "Which mechanism contributes most?", "When does it fail?", "What does it cost?"}
	claims := []researchClaim{
		card("a1", "https://example.com/p1", 1), card("b1", "https://example.com/p2", 1),
		card("a2", "https://example.com/p1", 2), card("a3", "https://example.com/p1", 2),
		card("b2", "https://example.com/p2", 3),
		card("c1", "https://example.com/p3", 0),
		{recordClaimOutput: recordClaimOutput{ID: "c2", URL: "https://example.com/p3", Status: claimStatusNotFound}, Question: 4},
	}
	result, ok := checkResearchQuestionCoverage("Pays off [a1, b1]. Mechanism [a2].", claims, questions)
	if !ok || len(result.Questions) != 4 || result.UntaggedCards != 1 {
		t.Fatalf("ok=%v result=%+v", ok, result)
	}
	want := []researchQuestionCoverage{
		{Question: 1, Text: questions[0], Cards: 2, CitedCards: 2, Sources: 2, CitedSources: 2},
		{Question: 2, Text: questions[1], Cards: 2, CitedCards: 1, Sources: 1, CitedSources: 1},
		{Question: 3, Text: questions[2], Cards: 1, CitedCards: 0, Sources: 1, CitedSources: 0},
		{Question: 4, Text: questions[3]},
	}
	for index := range want {
		if result.Questions[index] != want[index] {
			t.Fatalf("Q%d=%+v want %+v", index+1, result.Questions[index], want[index])
		}
	}
	guidance := researchQuestionCoverageGuidance(result)
	for _, fragment := range []string{"Q4 have no claim cards", "Q3 have cards but the report cites none", "Q2 rest on a single source"} {
		if !strings.Contains(guidance, fragment) {
			t.Fatalf("guidance %q lacks %q", guidance, fragment)
		}
	}
	if _, ok := checkResearchQuestionCoverage("x", []researchClaim{card("c1", "https://example.com/p", 0)}, questions); ok {
		t.Fatal("untagged cards produced question coverage")
	}
}

func TestRecordClaimQuestionIsOptionalAndBounded(t *testing.T) {
	if input, err := decodeRecordClaimInput([]byte(`{"source":"https://example.com","quote":"a verbatim quote","claim":"c","question":2}`)); err != nil || input.Question != 2 {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	if _, err := decodeRecordClaimInput([]byte(`{"source":"https://example.com","quote":"a verbatim quote","claim":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRecordClaimInput([]byte(`{"source":"https://example.com","quote":"a verbatim quote","claim":"c","question":31}`)); err == nil || !strings.Contains(err.Error(), "question") {
		t.Fatalf("err=%v", err)
	}
	markdown := renderResearchClaimsMarkdown([]researchClaim{{recordClaimOutput: recordClaimOutput{ID: "a1", Status: claimStatusVerified, URL: "https://example.com"}, Claim: "claim", Question: 2}})
	if !strings.Contains(markdown, "[a1] (verified Q2) claim") {
		t.Fatalf("markdown=%s", markdown)
	}
}

func TestQuestionCoverageFlagsQuestionsWithoutDissentOnlyWhenStanceIsRecorded(t *testing.T) {
	card := func(id, url string, question int, stance string) researchClaim {
		return researchClaim{recordClaimOutput: recordClaimOutput{ID: id, URL: url, Source: url, Status: claimStatusVerified}, Question: question, Stance: stance}
	}
	questions := []string{"Q1", "Q2"}
	unstanced := []researchClaim{card("a1", "https://x/1", 1, ""), card("b1", "https://x/2", 1, "")}
	result, ok := checkResearchQuestionCoverage("[a1, b1]", unstanced, questions)
	if !ok || result.StanceRecorded || strings.Contains(researchQuestionCoverageGuidance(result), "dissenting") {
		t.Fatalf("unstanced run was flagged: %+v %q", result, researchQuestionCoverageGuidance(result))
	}
	stanced := []researchClaim{
		card("a1", "https://x/1", 1, claimStanceSupports), card("b1", "https://x/2", 1, claimStanceSupports),
		card("a2", "https://x/1", 2, claimStanceSupports), card("b2", "https://x/2", 2, claimStanceQualifies),
	}
	result, ok = checkResearchQuestionCoverage("[a1, b1] [a2, b2]", stanced, questions)
	if !ok || !result.StanceRecorded || result.Questions[0].Dissenting != 0 || result.Questions[1].Dissenting != 1 {
		t.Fatalf("result=%+v", result)
	}
	guidance := researchQuestionCoverageGuidance(result)
	if !strings.Contains(guidance, "Q1 have cards that all support one answer") || strings.Contains(guidance, "Q2 have cards that all") {
		t.Fatalf("guidance=%q", guidance)
	}
}
