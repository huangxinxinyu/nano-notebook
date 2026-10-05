package agent

import (
	"strings"
	"testing"
)

func TestResearchScoutGroupsShareSlotsRoundRobin(t *testing.T) {
	groups := researchScoutGroups([]string{"q1", " ", "q3", "q4", "q5", "q6", "q7"}, researchScoutMax)
	got := make([]string, len(groups))
	for index, group := range groups {
		for _, question := range group {
			got[index] += "Q" + string(rune('0'+question.Number))
		}
	}
	if strings.Join(got, "|") != "Q1Q5|Q3Q6|Q4Q7" {
		t.Fatalf("groups=%v", got)
	}
	if len(researchScoutGroups([]string{"only"}, researchScoutMax)) != 1 || researchScoutGroups(nil, researchScoutMax) != nil {
		t.Fatal("scout count does not follow the questions")
	}
}

func TestResearchScoutTaskSearchesOpenlyWithoutReading(t *testing.T) {
	input := researchScoutSpawnInput([]researchScoutQuestion{{Number: 2, Text: "When does iteration fail?"}, {Number: 5, Text: "What does it cost?"}}, 32)
	if old := researchScoutSpawnInput([]researchScoutQuestion{{Number: 2, Text: "When does iteration fail?"}}, 31); strings.Contains(old.Message, "TODO") || !strings.Contains(input.Message, "do not use the TODO tools") {
		t.Fatal("scout batching is not gated at executor v32")
	}
	if input.TaskName != "Scout: Q2, Q5" || !strings.HasPrefix(input.Message, researchScoutTaskPrefix) {
		t.Fatalf("input=%+v", input)
	}
	for _, fragment := range []string{"- Q2: When does iteration fail?", "- Q5: What does it cost?", "web_search", "do not read full documents", "critique"} {
		if !strings.Contains(input.Message, fragment) {
			t.Fatalf("message lacks %q: %s", fragment, input.Message)
		}
	}
	if isResearchReaderTask(input.Message) {
		t.Fatal("a scout must not be held to the reader card gate")
	}
}
