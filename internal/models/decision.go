package models

import (
	"encoding/json"
	"errors"
	"strings"
)

type FinalDraft struct {
	Text string
}

type ActionProposal struct {
	Name  string
	Input json.RawMessage
}

type ActionProposalBatch struct {
	Actions []ActionProposal
}

type ModelDecision struct {
	Final    *FinalDraft
	Proposal *ActionProposalBatch
}

func (d ModelDecision) Validate() error {
	if (d.Final == nil) == (d.Proposal == nil) {
		return errors.New("model decision must contain exactly one variant")
	}
	if d.Final != nil && strings.TrimSpace(d.Final.Text) == "" {
		return errors.New("final draft is empty")
	}
	if d.Proposal != nil && len(d.Proposal.Actions) == 0 {
		return errors.New("action proposal batch is empty")
	}
	return nil
}
