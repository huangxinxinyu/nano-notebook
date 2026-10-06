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
	researchReaderElidedRunes    = 400
	researchReaderElisionNote    = "[earlier page text omitted to save context: its claim cards are in claims.md; call read_tool_result with this result_ref and offset to read it again]"
)

func isResearchReaderContextExecution(execution Execution) bool {
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	return err == nil && execution.ParentRunID != "" && isResearchReaderTask(execution.SubagentTask) &&
		reference.Identity == "research.executor" && reference.Version >= researchReaderContextVersion
}

// elideEarlierReaderPages returns messages with every document page but the
// latest reduced to its short fields.
func elideEarlierReaderPages(messages []models.ModelMessage) []models.ModelMessage {
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
	if len(pages) < 2 {
		return messages
	}
	out := append([]models.ModelMessage(nil), messages...)
	for _, index := range pages[:len(pages)-1] {
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
