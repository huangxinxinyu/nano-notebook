package agent

import (
	"strings"
	"testing"
)

func TestParseResearchReaderLeadsFollowsIDsAndURLs(t *testing.T) {
	final := `## Final
Read the paper in full. Leads to better recall were reported in Table 2 (2305.15294 is this paper).

**Leads**:
1. Self-RAG (arXiv:2310.11511) — the critique tokens the authors compare against.
- FLARE https://arxiv.org/abs/2305.06983v2 — active retrieval baseline.
- IRCoT, https://github.com/StonyBrookNLP/ircot. Interleaved CoT retrieval.
- A survey without any identifier
- Self-RAG again arXiv 2310.11511

## Not captured
- https://example.com/ignored`
	leads := parseResearchReaderLeads(final)
	want := []string{"https://arxiv.org/abs/2310.11511", "https://arxiv.org/abs/2305.06983", "https://github.com/StonyBrookNLP/ircot"}
	if len(leads) != len(want) {
		t.Fatalf("leads=%+v", leads)
	}
	for index, lead := range leads {
		if lead.URL != want[index] {
			t.Fatalf("lead %d=%+v want %s", index, lead, want[index])
		}
	}
	if !strings.HasPrefix(leads[0].Title, "Self-RAG (arXiv:2310.11511)") {
		t.Fatalf("title=%q", leads[0].Title)
	}
	if parseResearchReaderLeads("No section here, but https://example.com/x and 2310.11511.") != nil {
		t.Fatal("leads parsed without a Leads section")
	}
	chinese := parseResearchReaderLeads("总结……\n\n### 线索\n- 《Lost in the Middle》 arXiv:2307.03172 — 长上下文位置效应\n")
	if len(chinese) != 1 || chinese[0].URL != "https://arxiv.org/abs/2307.03172" {
		t.Fatalf("chinese=%+v", chinese)
	}
}

func TestResearchReaderTaskAsksForLeadsFromExecutorV30(t *testing.T) {
	if strings.Contains(researchReaderSpawnInput("https://x", "X", 29).Message, "Leads") ||
		!strings.Contains(researchReaderSpawnInput("https://x", "X", 30).Message, "section headed Leads") {
		t.Fatal("Leads instruction is not gated at executor v30")
	}
}
