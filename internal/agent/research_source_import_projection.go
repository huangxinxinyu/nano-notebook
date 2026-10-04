package agent

import (
	"context"
	"fmt"
	"strings"
)

type researchSourceImportState struct {
	SourceID       string
	ActionID       string
	SourceState    string
	JobStatus      string
	ErrorCode      string
	Searchable     bool
	RetrievalError string
}

func (r *ResearchRuntime) loadResearchSourceImportProjection(ctx context.Context, runID string) (string, error) {
	rows, err := r.pool.Query(ctx, `
		select coalesce(imported.source_id,''),imported.action_id,
			coalesce(source.state,''),coalesce(job.status,''),coalesce(job.last_error_code,''),
			exists(
				select 1 from agent_run_evidence_set evidence
				join source_evidence_revisions revision on revision.id=evidence.evidence_revision_id
					and revision.source_id=evidence.source_id and revision.status='active'
				join retrieval_source_index_builds build on build.revision_id=revision.id
					and build.source_id=evidence.source_id and build.index_version_id=evidence.index_version_id and build.status='verified'
				where evidence.run_id=$1 and evidence.source_id=imported.source_id
			),coalesce(imported.retrieval_error_code,'')
		from research_source_imports imported
		left join source_sources source on source.id=imported.source_id
		left join source_processing_jobs job on job.id=imported.processing_job_id
		where nano_research_root_run(imported.run_id)=nano_research_root_run($1)
		order by imported.created_at,imported.action_id
	`, runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	states := make([]researchSourceImportState, 0)
	for rows.Next() {
		var state researchSourceImportState
		if err := rows.Scan(&state.SourceID, &state.ActionID, &state.SourceState, &state.JobStatus, &state.ErrorCode, &state.Searchable, &state.RetrievalError); err != nil {
			return "", err
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return formatResearchSourceImportProjection(states), nil
}

func formatResearchSourceImportProjection(states []researchSourceImportState) string {
	if len(states) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Pending Source Imports:\n")
	for _, item := range states {
		identity := strings.TrimSpace(item.SourceID)
		if identity == "" {
			identity = "import:" + item.ActionID
		}
		state, searchable, reason := projectedResearchSourceImportState(item)
		fmt.Fprintf(&builder, "- %s: %s", identity, state)
		if reason != "" {
			fmt.Fprintf(&builder, ", %s", reason)
		}
		if searchable {
			builder.WriteString(", searchable\n")
		} else {
			builder.WriteString(", not searchable\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func projectedResearchSourceImportState(item researchSourceImportState) (state string, searchable bool, reason string) {
	switch {
	case item.SourceState == "ready":
		if item.Searchable {
			return "ready", true, ""
		}
		reason = strings.TrimSpace(item.RetrievalError)
		if reason == "" {
			reason = "source_not_in_run_scope"
		}
		return "ready", false, reason
	case item.SourceState == "failed":
		reason = strings.TrimSpace(item.ErrorCode)
		if reason == "" {
			reason = "source_processing_failed"
		}
		return "failed", false, reason
	case item.SourceState == "qualifying" && item.JobStatus == "succeeded":
		return "review_required", false, ""
	case item.SourceID == "":
		return "failed", false, "source_deleted"
	case item.JobStatus == "":
		return "failed", false, "source_job_deleted"
	default:
		return "processing", false, ""
	}
}
