package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// A reader that reads a long document in full also sees what the document
// builds on. From executor v30 it ends its Final with a Leads section naming
// the cited or follow-up works that matter most; when the reader completes,
// the runtime records each lead's arXiv id or URL as an unread lead, so
// following citations no longer depends on the root copying them out.

const (
	researchReaderLeadsVersion = 30
	// From executor v32 scouts' ranked candidates become recommended leads
	// too, and assembly always lists unread recommended leads.
	researchRecommendedLeadsVersion = 32
	researchScoutMaxCandidates      = 30
	researchReaderMaxLeads          = 8
	researchReaderLeadTitle         = 300
)

var (
	researchLeadsHeadingPattern = regexp.MustCompile(`(?im)^[ \t#*_]*(?:leads|线索)[ \t*_]*[:：]?[ \t*_]*$`)
	researchLeadArxivPattern    = regexp.MustCompile(`(?i)(?:arxiv[:./\s]*(?:abs/|pdf/|html/)?)?\b(\d{4}\.\d{4,5})(?:v\d+)?\b`)
	researchLeadURLPattern      = regexp.MustCompile(`https?://[^\s<>()\[\]"'，。；]+`)
	researchLeadMarkerPattern   = regexp.MustCompile(`^\s*(?:[-*+•]|\d+[.)、]?)\s+`)
)

type researchReaderLead struct {
	URL   string
	Title string
}

const researchReaderLeadsInstruction = " End your Final with a section headed Leads: 3 to 5 works that this document cites or that build on it and matter most for the plan, one per line, each with its title, its arXiv id or URL exactly as the document gives it, and why it matters. Take them from the document's text and reference list; do not search for them."

// parseResearchReaderLeads reads the Leads section of a reader's Final. A
// lead needs an arXiv id or a URL; a title alone cannot be followed.
func parseResearchReaderLeads(final string) []researchReaderLead {
	location := researchLeadsHeadingPattern.FindStringIndex(final)
	if location == nil {
		return nil
	}
	return parseResearchLeadLines(final[location[1]:], true, researchReaderMaxLeads)
}

// parseResearchScoutCandidates reads a scout's ranked candidates: every line
// of its Final that names an arXiv id or URL, in the scout's order.
func parseResearchScoutCandidates(final string) []researchReaderLead {
	return parseResearchLeadLines(final, false, researchScoutMaxCandidates)
}

func parseResearchLeadLines(text string, stopAtHeading bool, limit int) []researchReaderLead {
	leads := make([]researchReaderLead, 0)
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if stopAtHeading && strings.HasPrefix(line, "#") && len(leads) > 0 {
				break
			}
			continue
		}
		url := ""
		if match := researchLeadArxivPattern.FindStringSubmatch(line); match != nil {
			url = "https://arxiv.org/abs/" + match[1]
		} else if match := researchLeadURLPattern.FindString(line); match != "" {
			url = strings.TrimRight(match, ".,;:|*")
		}
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		title := researchLeadURLPattern.ReplaceAllString(strings.ReplaceAll(line, "|", " "), " ")
		title = strings.Join(strings.Fields(researchLeadMarkerPattern.ReplaceAllString(title, "")), " ")
		if utf8.RuneCountInString(title) > researchReaderLeadTitle {
			title = string([]rune(title)[:researchReaderLeadTitle]) + "…"
		}
		leads = append(leads, researchReaderLead{URL: url, Title: title})
		if len(leads) == limit {
			break
		}
	}
	return leads
}

// recordResearchSubagentLeadsInTx stores the sources a completed reader or
// scout recommends as recommended unread leads in the Research Session's
// ledger. A lead never downgrades a read or failed URL.
func recordResearchSubagentLeadsInTx(ctx context.Context, tx pgx.Tx, runID, final string) error {
	var version int
	var task string
	err := tx.QueryRow(ctx, `select run.definition_version,s.message from agent_runs run
		join agent_subagents s on s.child_run_id=run.id
		where run.id=$1 and run.definition_identity='research.executor'`, runID).Scan(&version, &task)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var leads []researchReaderLead
	switch {
	case isResearchReaderTask(task) && version >= researchReaderLeadsVersion:
		leads = parseResearchReaderLeads(final)
	case strings.HasPrefix(task, researchScoutTaskPrefix) && version >= researchRecommendedLeadsVersion:
		leads = parseResearchScoutCandidates(final)
	}
	// Each lead's recommendation time carries its rank, so the unread list
	// keeps the order the scout or reader gave.
	for rank, lead := range leads {
		if _, err := tx.Exec(ctx, `
			insert into research_evidence_ledger(session_id,url,title,status,recommended_at)
			select session.id,$2,$3,'discovered',now()+$4*interval '1 millisecond' from research_sessions session
			where session.execution_run_id=nano_research_root_run($1)
			on conflict(session_id,url) do update set last_seen_at=now(),
				recommended_at=coalesce(research_evidence_ledger.recommended_at,excluded.recommended_at),
				title=case when research_evidence_ledger.title='' then excluded.title else research_evidence_ledger.title end
		`, runID, lead.URL, lead.Title, rank); err != nil {
			return err
		}
	}
	return nil
}

// researchRecommendedLeadsGuidance asks for a second reading round over the
// sources scouts and readers recommended but the Run has not read.
func researchRecommendedLeadsGuidance(leads []researchReadSource) string {
	recommended := 0
	for _, lead := range leads {
		if lead.Recommended {
			recommended++
		}
	}
	if recommended == 0 {
		return ""
	}
	return fmt.Sprintf("Recommended but unread: %d sources that scouts or readers recommended are still unread (marked recommended in source_coverage.unread_leads). Read those that could change, qualify, or contradict a conclusion, especially independent evaluations, replications, and critiques, then revise and assemble again. Do not describe a source in the report that you have not read.", recommended)
}
