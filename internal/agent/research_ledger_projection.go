package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
)

// The Evidence Ledger block is appended after the trajectory on every
// Research decision, so it is never served from the provider's prefix cache.
// Before research.executor@44 it listed every discovered URL while tools were
// open; the measured Runs had 233 to 427 of them, and the paragraph bound did
// not apply because the whole list is one paragraph containing URLs. From
// v44 the block carries the counts, the read and failed sources, and only the
// strongest unread leads; the complete unread list is the virtual workspace
// file leads.md.

const (
	researchLeadsPath              = "leads.md"
	researchMemoryUnreadLeadLimit  = 24
	researchMemoryFailedLeadLimit  = 20
	researchCompactLedgerVersion   = 44
	researchLedgerReadURLDirective = 80
)

type researchLedgerEntry struct {
	URL           string
	FinalURL      string
	Title         string
	Status        string
	RecommendedAt *time.Time
	// Seen is the entry's position in first-seen order.
	Seen int
}

func (e researchLedgerEntry) link() string {
	url := e.FinalURL
	if url == "" {
		url = e.URL
	}
	title := strings.TrimSpace(e.Title)
	if title == "" {
		title = url
	}
	return fmt.Sprintf("[%s](%s)", title, url)
}

func (e researchLedgerEntry) recommended() bool { return e.RecommendedAt != nil }

func isCompactLedgerResearchExecution(execution Execution) bool {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	return err == nil && reference.Identity == "research.executor" && reference.Version >= researchCompactLedgerVersion
}

// loadResearchLedgerEntries returns a Session's ledger in first-seen order.
func loadResearchLedgerEntries(ctx context.Context, db DBTX, sessionID string) ([]researchLedgerEntry, error) {
	rows, err := db.Query(ctx, `select url,coalesce(final_url,''),title,status,recommended_at from research_evidence_ledger where session_id=$1 order by first_seen_at,url`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]researchLedgerEntry, 0)
	for rows.Next() {
		var entry researchLedgerEntry
		if err := rows.Scan(&entry.URL, &entry.FinalURL, &entry.Title, &entry.Status, &entry.RecommendedAt); err != nil {
			return nil, err
		}
		entry.Seen = len(entries)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

type researchLedgerCounts struct {
	Discovered, Read, Failed int
}

func (c researchLedgerCounts) total() int { return c.Discovered + c.Read + c.Failed }

func countResearchLedger(entries []researchLedgerEntry) researchLedgerCounts {
	var counts researchLedgerCounts
	for _, entry := range entries {
		switch entry.Status {
		case "read":
			counts.Read++
		case "failed":
			counts.Failed++
		default:
			counts.Discovered++
		}
	}
	return counts
}

// sortResearchUnreadLeads returns the discovered-only entries, recommended
// leads first in recommendation order, then the newest discoveries first.
func sortResearchUnreadLeads(entries []researchLedgerEntry) []researchLedgerEntry {
	unread := make([]researchLedgerEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Status != "read" && entry.Status != "failed" {
			unread = append(unread, entry)
		}
	}
	sort.SliceStable(unread, func(i, j int) bool {
		left, right := unread[i], unread[j]
		if left.recommended() != right.recommended() {
			return left.recommended()
		}
		if left.recommended() {
			if !left.RecommendedAt.Equal(*right.RecommendedAt) {
				return left.RecommendedAt.Before(*right.RecommendedAt)
			}
			return left.Seen < right.Seen
		}
		return left.Seen > right.Seen
	})
	return unread
}

// renderResearchLedgerBlock is the per-decision ledger block from v44.
func renderResearchLedgerBlock(entries []researchLedgerEntry, includeDiscovered bool, unreadLimit int) string {
	counts := countResearchLedger(entries)
	var builder strings.Builder
	fmt.Fprintf(&builder, "Evidence Ledger: %d unique URLs; %d successfully read, %d failed, %d discovered-only. Only successfully read material is evidence.\n", counts.total(), counts.Read, counts.Failed, counts.Discovered)
	if counts.Read > 0 {
		builder.WriteString("Read (do not call `read_url` again for these):\n")
		for _, entry := range entries {
			if entry.Status == "read" {
				fmt.Fprintf(&builder, "- %s\n", entry.link())
			}
		}
	}
	if counts.Failed > 0 {
		builder.WriteString("Failed:\n")
		shown := 0
		for _, entry := range entries {
			if entry.Status != "failed" {
				continue
			}
			if shown == researchMemoryFailedLeadLimit {
				fmt.Fprintf(&builder, "- … %d more failed URLs\n", counts.Failed-shown)
				break
			}
			fmt.Fprintf(&builder, "- %s\n", entry.link())
			shown++
		}
	}
	if includeDiscovered && counts.Discovered > 0 {
		unread := sortResearchUnreadLeads(entries)
		shown := unread
		if unreadLimit > 0 && len(shown) > unreadLimit {
			shown = shown[:unreadLimit]
		}
		fmt.Fprintf(&builder, "Unread leads (%d of %d shown, recommended first; the complete list is the workspace file %s, read it with `read_research_file`):\n", len(shown), len(unread), researchLeadsPath)
		for _, entry := range shown {
			builder.WriteString("- " + entry.link())
			if entry.recommended() {
				builder.WriteString(" — recommended")
			}
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

// renderResearchLeadsMarkdown is the virtual leads.md workspace file.
func renderResearchLeadsMarkdown(entries []researchLedgerEntry) string {
	counts := countResearchLedger(entries)
	unread := sortResearchUnreadLeads(entries)
	var builder strings.Builder
	builder.WriteString("# Unread leads\n\n")
	fmt.Fprintf(&builder, "%d unread of %d URLs in the Evidence Ledger (%d read, %d failed). Recommended leads come first, in the order scouts and readers recommended them, then the newest discoveries. Search titles and descriptions are leads, not evidence: read a lead before citing it.\n", len(unread), counts.total(), counts.Read, counts.Failed)
	if len(unread) == 0 {
		return builder.String()
	}
	builder.WriteByte('\n')
	for _, entry := range unread {
		builder.WriteString("- " + entry.link())
		if entry.recommended() {
			builder.WriteString(" — recommended")
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}
