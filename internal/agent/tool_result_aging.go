package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

// Tool Result aging is the deterministic context layer between an inline
// Result and an LLM Compaction. A Result smaller than the inline byte limit
// used to stay in every later request until the request crossed the
// Compaction trigger, which the measured Research Runs (about 60k tokens per
// decision against a 512k trigger) never reached. Aging replaces the Results
// of older Agent Steps with a short shell that names the durable checkpoint;
// the Tool call and its input stay exact, so the model still sees what it did.
//
// The aged boundary moves in batches so the provider's prefix cache breaks
// once per batch rather than once per decision: with the defaults, a request
// keeps between 6 and 11 recent Steps exact and the aged prefix is
// byte-identical until six more Steps complete.

const (
	agedResultKeepRecentSteps = 6
	agedResultBatchSteps      = 6
	agedResultMinBytes        = 2048
	agedResultPreviewRunes    = 200
	agedResultContentState    = "aged"

	// Research Runs from research.executor@44 and Chat Runs from
	// chat.leader@8 age their Results. Earlier Definitions keep their
	// projection unchanged so historical replays are unaffected.
	researchAgedResultsVersion = 44
	chatAgedResultsVersion     = 8
)

// agedResultExemptTools keeps instructions and child findings exact: a Skill
// is read once for the whole Run, and a child's Final is integrated over many
// drafting Steps.
var agedResultExemptTools = map[string]bool{"read_skill": true, "wait_agent": true}

type toolResultAgingPolicy struct {
	KeepRecentSteps int
	BatchSteps      int
	MinBytes        int
	// Rehydratable says the Run can read an aged Result back with
	// read_tool_result. Research Runs project only their own Run, whose
	// checkpoints the compacted reader serves; a Chat lane spans earlier
	// Runs and the Chat Leader has no read tool.
	Rehydratable bool
}

type toolResultAgingStats struct {
	AgedResults int
	AgedBytes   int
}

func defaultToolResultAgingPolicy(rehydratable bool) toolResultAgingPolicy {
	return toolResultAgingPolicy{
		KeepRecentSteps: agedResultKeepRecentSteps, BatchSteps: agedResultBatchSteps,
		MinBytes: agedResultMinBytes, Rehydratable: rehydratable,
	}
}

// toolResultAgingFor returns the aging policy pinned by the Run's Definition,
// or false when that Definition predates aging.
func toolResultAgingFor(execution Execution) (toolResultAgingPolicy, bool) {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	if err != nil {
		return toolResultAgingPolicy{}, false
	}
	switch reference.Identity {
	case "research.executor":
		if reference.Version >= researchFrozenPrefixVersion && execution.ParentRunID != "" && isResearchReaderTask(execution.SubagentTask) {
			return toolResultAgingPolicy{
				KeepRecentSteps: researchReaderAgedKeepSteps, BatchSteps: researchReaderAgedBatchSteps,
				MinBytes: agedResultMinBytes, Rehydratable: true,
			}, true
		}
		if reference.Version >= researchAgedResultsVersion {
			return defaultToolResultAgingPolicy(true), true
		}
	case "chat.leader":
		if reference.Version >= chatAgedResultsVersion {
			return defaultToolResultAgingPolicy(false), true
		}
	}
	return toolResultAgingPolicy{}, false
}

// agedRequestUnits applies the Run's aging policy, if any, to the units a
// request will carry.
func agedRequestUnits(execution Execution, units []ContextUnit) ([]ContextUnit, toolResultAgingStats) {
	policy, ok := toolResultAgingFor(execution)
	if !ok {
		return units, toolResultAgingStats{}
	}
	return ageOldToolResults(units, policy)
}

// ageOldToolResults replaces the large Results of every Agent Step older than
// the retained recent Steps, in whole batches, with their checkpoint shells.
// It never mutates its input.
func ageOldToolResults(units []ContextUnit, policy toolResultAgingPolicy) ([]ContextUnit, toolResultAgingStats) {
	if policy.KeepRecentSteps < 0 || policy.BatchSteps < 1 || policy.MinBytes < 1 {
		return units, toolResultAgingStats{}
	}
	steps := make([]int, 0, len(units))
	for index, unit := range units {
		if unit.Kind == ContextUnitAgentStep && unitHasActionResults(unit) {
			steps = append(steps, index)
		}
	}
	old := len(steps) - policy.KeepRecentSteps
	if old < policy.BatchSteps {
		return units, toolResultAgingStats{}
	}
	aged := old - old%policy.BatchSteps
	agedIndex := make(map[int]bool, aged)
	for _, index := range steps[:aged] {
		agedIndex[index] = true
	}
	out := make([]ContextUnit, 0, len(units))
	stats := toolResultAgingStats{}
	for index, unit := range units {
		if !agedIndex[index] {
			out = append(out, unit)
			continue
		}
		cloned := cloneResearchContextUnit(unit)
		names := map[string]string{}
		for _, message := range cloned.Messages {
			for _, call := range message.ActionCalls {
				names[call.ID] = call.Name
			}
		}
		for messageIndex, message := range cloned.Messages {
			if message.Role != models.RoleAction || len(message.Content) < policy.MinBytes || agedResultExemptTools[names[message.ActionCallID]] {
				continue
			}
			shell, ok := agedToolResultShell(unit.RunID, message.Content, policy.Rehydratable)
			if !ok {
				continue
			}
			stats.AgedResults++
			stats.AgedBytes += len(message.Content) - len(shell)
			cloned.Messages[messageIndex].Content = shell
		}
		out = append(out, cloned)
	}
	return out, stats
}

func unitHasActionResults(unit ContextUnit) bool {
	for _, message := range unit.Messages {
		if message.Role == models.RoleAction {
			return true
		}
	}
	return false
}

type agedToolResultShellPayload struct {
	ActionID     string             `json:"action_id"`
	Status       ActionResultStatus `json:"status"`
	ErrorCode    string             `json:"error_code,omitempty"`
	ContentState string             `json:"content_state"`
	ResultRef    string             `json:"result_ref"`
	ReadTool     string             `json:"read_tool,omitempty"`
	Rehydratable bool               `json:"rehydratable"`
	ResultBytes  int                `json:"result_bytes"`
	Preview      string             `json:"preview,omitempty"`
	Note         string             `json:"note"`
}

// agedToolResultShell renders the model-visible shell of one accepted Result.
// It reports false for content that is not an accepted Result checkpoint, so
// a message that is already a shell, a capsule, or elided text stays as is.
func agedToolResultShell(runID, content string, rehydratable bool) (string, bool) {
	var accepted actionResultCheckpointPayload
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&accepted); err != nil || accepted.ActionID == "" || strings.TrimSpace(runID) == "" {
		return "", false
	}
	if accepted.Status != ActionSucceeded && accepted.Status != ActionDomainError {
		return "", false
	}
	errorCode := accepted.ErrorCode
	if errorCode == "" && accepted.Error != nil {
		errorCode = accepted.Error.Code
	}
	shell := agedToolResultShellPayload{
		ActionID: accepted.ActionID, Status: accepted.Status, ErrorCode: errorCode,
		ContentState: agedResultContentState,
		ResultRef:    fmt.Sprintf("run:%s/checkpoint:%s", runID, accepted.ActionID),
		ResultBytes:  len(accepted.Output),
	}
	if accepted.Status == ActionSucceeded && rehydratable {
		shell.Rehydratable = true
		shell.ReadTool = ToolResultReadTool
		shell.Note = "This earlier Tool Result was aged out of context to save tokens. Its claim cards, if any, are in claims.md; call read_tool_result with result_ref to read the full result again."
	} else {
		shell.Note = "This earlier Tool Result was aged out of context to save tokens. Run the Tool again if you need its content."
	}
	if len(accepted.Output) > 0 {
		shell.Preview = truncateRunesWithEllipsis(string(accepted.Output), agedResultPreviewRunes)
	}
	encoded, err := json.Marshal(shell)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func truncateRunesWithEllipsis(value string, limit int) string {
	if limit < 1 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}
