package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func ledgerFixture(discovered int) []researchLedgerEntry {
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	entries := []researchLedgerEntry{
		{URL: "https://arxiv.org/abs/2310.11511", FinalURL: "https://arxiv.org/html/2310.11511", Title: "Self-RAG", Status: "read"},
		{URL: "https://example.com/down", Title: "Down", Status: "failed"},
	}
	for index := 0; index < discovered; index++ {
		entries = append(entries, researchLedgerEntry{URL: fmt.Sprintf("https://example.com/lead-%03d", index), Title: fmt.Sprintf("Lead %03d", index), Status: "discovered"})
	}
	later, earlier := base.Add(2*time.Minute), base
	entries[5].RecommendedAt = &later
	entries[9].RecommendedAt = &earlier
	for index := range entries {
		entries[index].Seen = index
	}
	return entries
}

func TestCompactLedgerBlockShowsCountsReadAndTopUnreadLeadsOnly(t *testing.T) {
	block := renderResearchLedgerBlock(ledgerFixture(400), true, 24)
	for _, required := range []string{
		"Evidence Ledger: 402 unique URLs; 1 successfully read, 1 failed, 400 discovered-only.",
		"Read (do not call `read_url` again for these):\n- [Self-RAG](https://arxiv.org/html/2310.11511)",
		"Failed:\n- [Down](https://example.com/down)",
		"Unread leads (24 of 400 shown, recommended first; the complete list is the workspace file leads.md",
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("block missing %q:\n%s", required, block)
		}
	}
	lines := strings.Split(strings.TrimSpace(block), "\n")
	unread := lines[len(lines)-24:]
	// Recommended leads first in recommendation order, then the newest discoveries.
	if !strings.HasPrefix(unread[0], "- [Lead 007]") || !strings.HasSuffix(unread[0], "— recommended") ||
		!strings.HasPrefix(unread[1], "- [Lead 003]") || !strings.HasPrefix(unread[2], "- [Lead 399]") || !strings.HasPrefix(unread[23], "- [Lead 378]") {
		t.Fatalf("unread order=%q", unread)
	}
	if strings.Count(block, "https://example.com/lead-") != 24 || len(block) > 4000 {
		t.Fatalf("block carries %d leads in %d bytes", strings.Count(block, "https://example.com/lead-"), len(block))
	}
	closed := renderResearchLedgerBlock(ledgerFixture(400), false, 24)
	if strings.Contains(closed, "Unread leads") || !strings.Contains(closed, "400 discovered-only") {
		t.Fatalf("final-only block=%s", closed)
	}
}

func TestLeadsMarkdownListsEveryUnreadLead(t *testing.T) {
	markdown := renderResearchLeadsMarkdown(ledgerFixture(400))
	if !strings.HasPrefix(markdown, "# Unread leads\n\n400 unread of 402 URLs in the Evidence Ledger (1 read, 1 failed).") ||
		strings.Count(markdown, "https://example.com/lead-") != 400 || strings.Count(markdown, "— recommended") != 2 ||
		strings.Contains(markdown, "Self-RAG") {
		t.Fatalf("markdown=%s", markdown[:400])
	}
	if empty := renderResearchLeadsMarkdown(nil); !strings.Contains(empty, "0 unread of 0 URLs") {
		t.Fatalf("empty=%s", empty)
	}
}

func TestLegacyLedgerParagraphBoundDidNotApplyToURLLists(t *testing.T) {
	// Documents the pre-v44 defect the compact block replaces: the whole
	// ledger is one paragraph containing URLs, which the paragraph compactor
	// keeps regardless of its limit.
	var builder strings.Builder
	builder.WriteString("Evidence Ledger:\n")
	for index := 0; index < 400; index++ {
		fmt.Fprintf(&builder, "- [Lead %03d](https://example.com/lead-%03d) — discovered\n", index, index)
	}
	if got := compactMarkdownByParagraph(builder.String(), 4000); len(got) < 20_000 {
		t.Fatalf("expected the legacy compactor to keep the URL paragraph whole, got %d bytes", len(got))
	}
}

func TestCompactLedgerAppliesFromExecutorV44(t *testing.T) {
	if !isCompactLedgerResearchExecution(Execution{AgentConfigID: "research.executor@44"}) ||
		isCompactLedgerResearchExecution(Execution{AgentConfigID: "research.executor@43"}) ||
		isCompactLedgerResearchExecution(Execution{AgentConfigID: "chat.leader@8"}) {
		t.Fatal("compact ledger gating is wrong")
	}
}
