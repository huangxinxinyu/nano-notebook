package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type PublicActivity struct {
	Kind      string    `json:"kind"`
	Detail    string    `json:"detail,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

type publicActivityContext struct {
	sourceTitles   map[string]string
	documentTitles map[string]string
	selectedTitles []string
}

func projectPublicActivity(action AcceptedAction, startedAt time.Time, context publicActivityContext) PublicActivity {
	activity := PublicActivity{Kind: "working", StartedAt: startedAt}
	switch action.Name {
	case "search_evidence":
		var input searchEvidenceInput
		if json.Unmarshal(action.Input, &input) != nil {
			break
		}
		activity.Kind = "searching_sources"
		activity.Detail = safeActivityText(strings.Join(context.selectedTitles, "、"), 160)
	case "discover_sources":
		var input discoverSourcesInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "discovering_sources"
			activity.Detail = safeActivityText(strings.Join(input.Queries, " · "), 160)
		}
	case "inspect_source":
		var input inspectSourceInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "inspecting_source"
			activity.Detail = safeActivityText(context.sourceTitles[input.SourceID], 160)
		}
	case "read_document_pages":
		var input readDocumentPagesInput
		if json.Unmarshal(action.Input, &input) == nil && input.StartPage > 0 && input.EndPage >= input.StartPage {
			activity.Kind = "reading_pdf"
			title := safeActivityText(context.documentTitles[input.DocumentHandle], 120)
			if title != "" {
				activity.Detail = fmt.Sprintf("%s · %d–%d", title, input.StartPage, input.EndPage)
			}
		}
	case "read_url":
		var input readURLInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "reading_webpage"
			activity.Detail = safeActivityURL(input.URL)
		}
	case "save_url_as_source":
		var input saveURLAsSourceInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "saving_source"
			activity.Detail = safeActivityURL(input.URL)
		}
	case "calculate":
		var input calculateInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "calculating"
		}
	case runPythonActionName:
		var input runPythonInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "calculating"
		}
	case "rewrite_todo_list":
		var input rewriteTodoListInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "organizing_steps"
		}
	case "update_todo_status":
		var input updateTodoStatusInput
		if json.Unmarshal(action.Input, &input) == nil {
			activity.Kind = "organizing_steps"
		}
	}
	return activity
}

func safeActivityURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	path := parsed.EscapedPath()
	if path == "/" {
		path = ""
	}
	return safeActivityText(strings.ToLower(parsed.Hostname())+path, 160)
}

func safeActivityText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "…"
	}
	return value
}

func (s *Store) publicActivitiesForRun(ctx context.Context, runID, status string) ([]PublicActivity, error) {
	if status != "running" {
		return []PublicActivity{}, nil
	}
	checkpoints, err := loadPublicRunCheckpoints(ctx, s.db, runID)
	if err != nil {
		return nil, err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return nil, err
	}
	context := publicActivityContext{
		sourceTitles: make(map[string]string), documentTitles: make(map[string]string),
	}
	rows, err := s.db.Query(ctx, `
		select source.id,source.title
		from agent_run_evidence_set evidence
		join source_sources source on source.id=evidence.source_id and source.notebook_id=evidence.notebook_id
		where evidence.run_id=$1 order by evidence.ordinal
	`, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sourceID, title string
		if err := rows.Scan(&sourceID, &title); err != nil {
			rows.Close()
			return nil, err
		}
		title = safeActivityText(title, 120)
		context.sourceTitles[sourceID] = title
		if title != "" && len(context.selectedTitles) < 3 {
			context.selectedTitles = append(context.selectedTitles, title)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name != "read_url" || action.Result == nil || action.Result.Status != ActionSucceeded {
				continue
			}
			var output readURLOutput
			if json.Unmarshal(action.Result.Output, &output) != nil || output.DocumentHandle == "" {
				continue
			}
			title := safeActivityText(output.Title, 120)
			if title == "" {
				title = safeActivityURL(output.FinalURL)
			}
			context.documentTitles[output.DocumentHandle] = title
		}
	}
	proposalTimes := make(map[int]time.Time)
	for _, checkpoint := range checkpoints {
		if checkpoint.Kind == CheckpointActionProposal {
			proposalTimes[checkpoint.DecisionNo] = checkpoint.CreatedAt
		}
	}
	activities := make([]PublicActivity, 0)
	for _, proposal := range prefix.Proposals {
		startedAt := proposalTimes[proposal.DecisionNo]
		for _, action := range proposal.Actions {
			if action.Result == nil {
				activities = append(activities, projectPublicActivity(action, startedAt, context))
			}
		}
	}
	return activities, nil
}

func loadPublicRunCheckpoints(ctx context.Context, db DBTX, runID string) ([]Checkpoint, error) {
	rows, err := db.Query(ctx, `
		select sequence_no,identity_key,kind,decision_no,action_index,action_id,
			payload_version,payload_text,payload_sha256,created_at
		from nano_owned_run_checkpoints($1)`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checkpoints := make([]Checkpoint, 0)
	for rows.Next() {
		checkpoint, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return checkpoints, nil
}
