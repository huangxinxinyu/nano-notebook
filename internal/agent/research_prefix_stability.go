package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
)

// A provider reuses its prompt cache only up to the first byte that differs
// from an earlier request. From research.executor@45 a Research request keeps
// its leading parts fixed for the whole Run:
//
//   - the system prompt holds only Run-constant text; budget, import,
//     duplicate-recovery, pause, and final-only notices are one runtime
//     notice after the trajectory;
//   - the tool list is not narrowed when repeated calls pause a tool; the
//     pause is enforced when the call executes, from the Run's checkpoints;
//   - readers no longer rewrite earlier pages on each new page; Tool Result
//     aging, in batches, bounds their context instead.

const (
	researchFrozenPrefixVersion = 45
	researchToolPausedCode      = "research_tool_paused"

	// Readers turn over pages faster than a root turns over Steps, so they
	// keep fewer recent Steps exact and age in smaller batches.
	researchReaderAgedKeepSteps  = 4
	researchReaderAgedBatchSteps = 4
)

func isFrozenPrefixResearchExecution(execution Execution) bool {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	return err == nil && isFrozenPrefixResearchDefinition(reference)
}

func isFrozenPrefixResearchDefinition(reference agentcatalog.Reference) bool {
	return reference.Identity == "research.executor" && reference.Version >= researchFrozenPrefixVersion
}

func renderResearchRuntimeNotices(notices []string) string {
	var builder strings.Builder
	builder.WriteString("<runtime_notices>\n")
	for index, notice := range notices {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(strings.TrimSpace(notice))
	}
	builder.WriteString("\n</runtime_notices>")
	return builder.String()
}

// researchPausedToolNames lists the tools paused for the decision after
// prefix: those repeated in its trailing run of all-repeat decisions, once
// that run reaches researchRepeatPauseSteps.
func researchPausedToolNames(prefix CheckpointPrefix) []string {
	steps := consecutiveResearchDuplicateSteps(prefix)
	if steps < researchRepeatPauseSteps {
		return nil
	}
	repeated := map[string]bool{}
	for index := len(prefix.Proposals) - 1; index >= 0 && index >= len(prefix.Proposals)-steps; index-- {
		for _, action := range prefix.Proposals[index].Actions {
			repeated[action.Name] = true
		}
	}
	names := make([]string, 0, len(repeated))
	for name := range repeated {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func researchPausedToolsNotice(paused []string, steps int) string {
	return fmt.Sprintf("Repeat circuit breaker: %s is paused for this decision because your last %d decisions only repeated completed inputs; a call to it now returns %s without running. Use the results you already have: read an unread lead, record claim cards, or draft and assemble the report.", strings.Join(paused, ", "), steps, researchToolPausedCode)
}

func researchToolPausedError(name string) *ActionError {
	return &ActionError{
		Kind: "domain", Code: researchToolPausedCode,
		Message:    fmt.Sprintf("%s is paused for this decision: the previous decisions only repeated completed inputs. The call did not run.", name),
		Suggestion: "Read an unread lead from the Evidence Ledger, record claim cards from what you have read, or draft and assemble the report.",
	}
}

func isResearchToolPausedResult(result ActionResult) bool {
	return result.ErrorCode == researchToolPausedCode || (result.Error != nil && result.Error.Code == researchToolPausedCode)
}

// researchPausedForAction reports whether the tool in decision decisionNo is
// paused, given the Run's checkpoints. Only the decisions before decisionNo
// count, so the answer is the same on every Attempt that executes the call.
func researchPausedForAction(ctx context.Context, db DBTX, runID, actionID, name string) (bool, error) {
	var decisionNo, actionIndex int
	if _, err := fmt.Sscanf(actionID, "decision:%d/action:%d", &decisionNo, &actionIndex); err != nil || decisionNo < 1 {
		return false, nil
	}
	checkpoints, err := loadRunCheckpoints(ctx, db, runID)
	if err != nil {
		return false, err
	}
	earlier := make([]Checkpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if checkpoint.DecisionNo < decisionNo {
			earlier = append(earlier, checkpoint)
		}
	}
	prefix, err := LoadCheckpointPrefix(ctx, earlier)
	if err != nil {
		return false, err
	}
	for _, paused := range researchPausedToolNames(prefix) {
		if paused == name {
			return true, nil
		}
	}
	return false, nil
}
