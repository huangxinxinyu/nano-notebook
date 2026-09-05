package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const searchQueryRuneLimit = 2000

// preserveCurrentSearchQuery is used by acceptContextualizedSearch (see
// controller.go) for runtimes that implement QueryContextRuntime — today,
// only Studio Output generation. It guards against a model-authored query
// silently dropping the deterministic fallback query's terms.
func preserveCurrentSearchQuery(proposal, fallback models.ActionProposal) (models.ActionProposal, error) {
	var current searchEvidenceInput
	if err := json.Unmarshal(fallback.Input, &current); err != nil {
		return models.ActionProposal{}, err
	}
	var contextualized searchEvidenceInput
	if err := json.Unmarshal(proposal.Input, &contextualized); err != nil {
		return models.ActionProposal{}, err
	}
	if !strings.Contains(contextualized.Query, current.Query) {
		contextualized.Query = truncateRunes(strings.TrimSpace(current.Query+" "+contextualized.Query), searchQueryRuneLimit)
	}
	contextualized.SourceID = current.SourceID
	contextualized.EntryID = current.EntryID
	input, err := json.Marshal(contextualized)
	if err != nil {
		return models.ActionProposal{}, err
	}
	proposal.Input = input
	return proposal, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
