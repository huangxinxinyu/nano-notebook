package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A long document that arrives while every reader slot is busy returns an
// excerpt to the root and stays queued. The queue is derived from the root's
// own checkpoints: a capacity excerpt whose URL no subagent task names yet.
// wait_agent drains it into slots that finished readers free, so a queued read
// no longer depends on the model remembering to delegate it later.

// researchQueuedReadsSQL lists a root Run's queued long documents in the order
// they were read. $1 is the root Run.
const researchQueuedReadsSQL = `
	select result.action_id,proposed->'input'->>'url',
		coalesce(result.payload->'output'->>'requested_url',''),
		coalesce(result.payload->'output'->>'title',''),
		coalesce(result.payload->'output'->>'final_url','')
	from agent_run_checkpoints result
	join agent_run_checkpoints proposal on proposal.run_id=result.run_id and proposal.kind='action_proposal'
	cross join lateral jsonb_array_elements(proposal.payload->'actions') proposed
	where result.run_id=$1 and result.kind='action_result'
		and proposed->>'action_id'=result.action_id and proposed->>'name'='read_url'
		and result.payload->'output'->>'outcome'='` + researchReaderCapacityOutcome + `'
		and not exists(select 1 from agent_subagents s where s.parent_run_id=$1 and (
			s.action_id='reader:'||result.action_id
			or strpos(s.message,proposed->'input'->>'url')>0
			or (coalesce(result.payload->'output'->>'final_url','')<>'' and strpos(s.message,result.payload->'output'->>'final_url')>0)))
	order by result.sequence_no`

type researchQueuedRead struct {
	ActionID string `json:"-"`
	URL      string `json:"url"`
	Title    string `json:"title,omitempty"`
}

// researchAutoReader is a reader the runtime dispatched from the queue rather
// than one the model spawned.
type researchAutoReader struct {
	AgentID  string `json:"agent_id"`
	TaskName string `json:"task_name"`
	Status   string `json:"status"`
}

func loadResearchQueuedReads(ctx context.Context, tx DBTX, rootRunID string) ([]researchQueuedRead, error) {
	rows, err := tx.Query(ctx, researchQueuedReadsSQL, rootRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]researchQueuedRead, 0)
	for rows.Next() {
		var item researchQueuedRead
		var requested, finalURL string
		if err := rows.Scan(&item.ActionID, &item.URL, &requested, &item.Title, &finalURL); err != nil {
			return nil, err
		}
		if requested != "" {
			item.URL = requested
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// A reader for another URL of the same document, such as an arXiv html
	// page for a queued abs page, already covers it.
	reading, err := researchReaderDocumentKeysInTx(ctx, tx, rootRunID)
	if err != nil {
		return nil, err
	}
	queued := make([]researchQueuedRead, 0, len(candidates))
	for _, item := range candidates {
		if key := researchDocumentKey(item.URL); !reading[key] {
			reading[key] = true
			queued = append(queued, item)
		}
	}
	return queued, nil
}

// dispatchQueuedResearchReadsInTx hands queued long documents to new readers
// while slots are free. Each reader is keyed to the read_url Action that
// queued it, so replaying the dispatching wait_agent finds the same readers.
func dispatchQueuedResearchReadsInTx(ctx context.Context, tx pgx.Tx, request ActionRequest) error {
	queued, err := loadResearchQueuedReads(ctx, tx, request.Attempt.RunID)
	if err != nil || len(queued) == 0 {
		return err
	}
	active, total, err := countRuntimeSubagentsInTx(ctx, tx, request.Attempt.RunID)
	if err != nil {
		return err
	}
	for _, item := range queued {
		if active >= runtimeSubagentMaxActive || total >= runtimeSubagentMaxTotal {
			break
		}
		input := researchReaderSpawnInput(item.URL, item.Title, request.Definition.Version)
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if _, err := createRuntimeSubagentInTx(ctx, tx, request, "reader:"+item.ActionID, input, len(raw)); err != nil {
			return err
		}
		active++
		total++
	}
	return nil
}

func loadResearchAutoReadersInTx(ctx context.Context, tx pgx.Tx, rootRunID string) ([]researchAutoReader, error) {
	rows, err := tx.Query(ctx, `select s.child_run_id,s.task_name,child.status from agent_subagents s
		join agent_runs child on child.id=s.child_run_id
		where s.parent_run_id=$1 and s.action_id like 'reader:%' order by s.created_at,s.child_run_id`, rootRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	readers := make([]researchAutoReader, 0)
	for rows.Next() {
		var reader researchAutoReader
		if err := rows.Scan(&reader.AgentID, &reader.TaskName, &reader.Status); err != nil {
			return nil, err
		}
		readers = append(readers, reader)
	}
	return readers, rows.Err()
}

// waitAgentResearchQueue drains the reader queue for a waiting root Researcher
// and reports every reader the runtime has dispatched, so the model can wait
// on them too. Older executor versions keep the plain wait_agent result.
func waitAgentResearchQueue(ctx context.Context, tx pgx.Tx, request ActionRequest) ([]researchAutoReader, error) {
	if request.Definition.Identity != "research.executor" || request.Definition.Version < researchQueuedReaderVersion {
		return nil, nil
	}
	if err := dispatchQueuedResearchReadsInTx(ctx, tx, request); err != nil {
		return nil, err
	}
	return loadResearchAutoReadersInTx(ctx, tx, request.Attempt.RunID)
}

// QueuedResearchReads lists the root Run's long documents still waiting for a
// reader slot.
func (b postgresResearchClaimBackend) QueuedResearchReads(ctx context.Context, runID string) ([]researchQueuedRead, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return loadResearchQueuedReads(ctx, tx, runID)
}

type researchQueuedReadLister interface {
	QueuedResearchReads(ctx context.Context, runID string) ([]researchQueuedRead, error)
}

func researchQueuedReadsGuidance(queued []researchQueuedRead) string {
	if len(queued) == 0 {
		return ""
	}
	return fmt.Sprintf("Queued long documents: %d (listed in queued_reads) are still waiting for a reader slot. Call wait_agent on your readers; each wait that finds a finished reader dispatches queued documents to new readers. Use their cards before Final.", len(queued))
}
