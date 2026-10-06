package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

// A reader pages through one long document and, from executor v32, records
// each page's cards before reading the next. Re-sending every earlier page on
// every decision made readers the largest share of a Research Run's input
// tokens. From executor v36 a reader's request keeps only the latest page in
// full; earlier pages keep their short fields, such as result_ref and
// next_offset, so the reader can page again when it needs exact wording.

const (
	researchReaderContextVersion = 36
	// From executor v38 readers keep their two latest pages: flash readers
	// that saw only one re-read a quarter of their pages to quote them.
	researchReaderTwoPageVersion = 38
	researchReaderElidedRunes    = 400
	researchReaderElisionNote    = "[earlier page text omitted to save context: its claim cards are in claims.md; call read_tool_result with this result_ref and offset to read it again]"
)

// researchReaderKeptPages is how many latest pages a reader's request keeps
// in full, or 0 when its requests are not elided.
func researchReaderKeptPages(execution Execution) int {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	if err != nil || execution.ParentRunID == "" || !isResearchReaderTask(execution.SubagentTask) ||
		reference.Identity != "research.executor" || reference.Version < researchReaderContextVersion {
		return 0
	}
	if reference.Version >= researchReaderTwoPageVersion {
		return 2
	}
	return 1
}

func isResearchReaderContextExecution(execution Execution) bool {
	return researchReaderKeptPages(execution) > 0
}

// elideEarlierReaderPages returns messages with every document page but the
// latest kept ones reduced to their short fields.
func elideEarlierReaderPages(messages []models.ModelMessage, kept int) []models.ModelMessage {
	names := map[string]string{}
	pages := make([]int, 0)
	for index, message := range messages {
		for _, call := range message.ActionCalls {
			names[call.ID] = call.Name
		}
		if message.ActionCallID == "" {
			continue
		}
		if name := names[message.ActionCallID]; name == "read_url" || name == "read_tool_result" {
			pages = append(pages, index)
		}
	}
	if kept < 1 || len(pages) <= kept {
		return messages
	}
	out := append([]models.ModelMessage(nil), messages...)
	for _, index := range pages[:len(pages)-kept] {
		out[index].Content = elideReaderPage(out[index].Content)
	}
	return out
}

func elideReaderPage(content string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(content), &fields) != nil {
		return truncateReaderPage(content)
	}
	elided := false
	var walk func(value any) any
	walk = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				typed[key] = walk(item)
			}
			return typed
		case string:
			if utf8.RuneCountInString(typed) > researchReaderElidedRunes {
				elided = true
				return string([]rune(typed)[:researchReaderElidedRunes]) + "… " + researchReaderElisionNote
			}
		}
		return value
	}
	walk(fields)
	if !elided {
		return content
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return truncateReaderPage(content)
	}
	return string(encoded)
}

func truncateReaderPage(content string) string {
	if utf8.RuneCountInString(content) <= researchReaderElidedRunes {
		return content
	}
	return strings.TrimSpace(string([]rune(content)[:researchReaderElidedRunes])) + "… " + researchReaderElisionNote
}
