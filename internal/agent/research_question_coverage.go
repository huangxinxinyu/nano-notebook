package agent

import (
	"context"
	"fmt"
	"strings"
)

// Claim cards may name the accepted plan's research question they answer.
// Assembly then reports, per question, how many usable cards and distinct
// sources back it and how many of those the report cites, so the model can
// see a question that rests on one source or none. Like the other assembly
// checks it only informs revision.

const researchQuestionTextRunes = 120

type researchQuestionCoverage struct {
	Question     int    `json:"question"`
	Text         string `json:"text"`
	Cards        int    `json:"cards"`
	CitedCards   int    `json:"cited_cards"`
	Sources      int    `json:"sources"`
	CitedSources int    `json:"cited_sources"`
}

type researchQuestionCoverageReport struct {
	Questions     []researchQuestionCoverage `json:"questions"`
	UntaggedCards int                        `json:"untagged_cards"`
}

type researchPlanQuestionLister interface {
	ResearchPlanQuestions(ctx context.Context, runID string) ([]string, error)
}

// ResearchPlanQuestions returns the accepted plan's research questions for the
// Run's Research tree.
func (b postgresResearchClaimBackend) ResearchPlanQuestions(ctx context.Context, runID string) ([]string, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return loadResearchPlanQuestions(ctx, tx, runID)
}

// checkResearchQuestionCoverage is empty until at least one usable card names
// a question, so runs that never tag cards see no change.
func checkResearchQuestionCoverage(report string, claims []researchClaim, questions []string) (researchQuestionCoverageReport, bool) {
	if len(questions) == 0 {
		return researchQuestionCoverageReport{}, false
	}
	cited := citedResearchClaimIDs(report, claims)
	coverage := make([]researchQuestionCoverage, len(questions))
	sources := make([]map[string]bool, len(questions))
	citedSources := make([]map[string]bool, len(questions))
	for index, text := range questions {
		if runes := []rune(strings.TrimSpace(text)); len(runes) > researchQuestionTextRunes {
			text = string(runes[:researchQuestionTextRunes]) + "…"
		}
		coverage[index] = researchQuestionCoverage{Question: index + 1, Text: strings.TrimSpace(text)}
		sources[index], citedSources[index] = map[string]bool{}, map[string]bool{}
	}
	result := researchQuestionCoverageReport{}
	tagged := false
	for _, claim := range claims {
		if claim.Status != claimStatusVerified && claim.Status != claimStatusNearMatch {
			continue
		}
		index := claim.Question - 1
		if index < 0 || index >= len(questions) {
			result.UntaggedCards++
			continue
		}
		tagged = true
		source := normalizeClaimURL(firstNonEmpty(claim.URL, claim.Source))
		coverage[index].Cards++
		sources[index][source] = true
		if cited[claim.ID] {
			coverage[index].CitedCards++
			citedSources[index][source] = true
		}
	}
	if !tagged {
		return researchQuestionCoverageReport{}, false
	}
	for index := range coverage {
		coverage[index].Sources, coverage[index].CitedSources = len(sources[index]), len(citedSources[index])
	}
	result.Questions = coverage
	return result, true
}

func citedResearchClaimIDs(report string, claims []researchClaim) map[string]bool {
	known := make(map[string]bool, len(claims))
	for _, claim := range claims {
		known[claim.ID] = true
	}
	cited := map[string]bool{}
	for _, match := range researchClaimCitationPattern.FindAllStringSubmatch(report, -1) {
		for _, id := range citedClaimIDs(match[1]) {
			if known[id] {
				cited[id] = true
			}
		}
	}
	return cited
}

func researchQuestionCoverageGuidance(report researchQuestionCoverageReport) string {
	var empty, uncited, single []string
	for _, question := range report.Questions {
		label := fmt.Sprintf("Q%d", question.Question)
		switch {
		case question.Cards == 0:
			empty = append(empty, label)
		case question.CitedCards == 0:
			uncited = append(uncited, label)
		case question.Sources < 2:
			single = append(single, label)
		}
	}
	parts := make([]string, 0, 3)
	if len(empty) > 0 {
		parts = append(parts, fmt.Sprintf("%s have no claim cards yet: discover and read sources for them, or state plainly in the report that the evidence was not found.", strings.Join(empty, ", ")))
	}
	if len(uncited) > 0 {
		parts = append(parts, fmt.Sprintf("%s have cards but the report cites none of them.", strings.Join(uncited, ", ")))
	}
	if len(single) > 0 {
		parts = append(parts, fmt.Sprintf("%s rest on a single source: look for independent evaluations, replications, or critiques before treating their answers as settled.", strings.Join(single, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Question coverage (question_coverage): " + strings.Join(parts, " ")
}
