package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrActiveRun           = errors.New("active run conflict")
	ErrRunNotFound         = errors.New("agent run not found")
	ErrRunNotCancellable   = errors.New("agent run not cancellable")
	ErrRunNotRetryable     = errors.New("agent run not retryable")
	ErrRetryNotLatest      = errors.New("agent run input is not latest")
	ErrIdempotencyMismatch = errors.New("idempotency mismatch")
	ErrEvidenceSetInvalid  = errors.New("selected Source evidence is not ready and verified")
	ErrCitationNotFound    = errors.New("Citation not found")
	ErrCitationUnavailable = errors.New("Citation evidence unavailable")
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type RunRef struct {
	ID     string
	Status string
}

type RunSnapshot struct {
	ID                 string           `json:"id"`
	InputMessageID     string           `json:"input_message_id"`
	Status             string           `json:"status"`
	ErrorCode          *string          `json:"error_code"`
	DiscoverySessionID *string          `json:"discovery_session_id,omitempty"`
	StartedAt          *time.Time       `json:"started_at,omitempty"`
	FinishedAt         *time.Time       `json:"finished_at,omitempty"`
	Activities         []PublicActivity `json:"activities"`
}

type AssistantMessageSnapshot struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type RunProjection struct {
	Run       RunSnapshot               `json:"run"`
	Message   *AssistantMessageSnapshot `json:"message"`
	Citations []CitationSnapshot        `json:"citations"`
}

type CitationSnapshot struct {
	ID                 string  `json:"id"`
	MessageID          string  `json:"message_id"`
	ReferenceKind      string  `json:"reference_kind"`
	ReferenceOrdinal   *int    `json:"reference_ordinal,omitempty"`
	ClaimOrdinal       *int    `json:"claim_ordinal,omitempty"`
	CitationOrdinal    *int    `json:"citation_ordinal,omitempty"`
	ClaimText          *string `json:"claim_text,omitempty"`
	SourceID           string  `json:"source_id"`
	SourceTitle        *string `json:"source_title,omitempty"`
	EvidenceRevisionID *string `json:"evidence_revision_id,omitempty"`
	UnitID             *string `json:"unit_id,omitempty"`
	StartRune          *int    `json:"start_rune,omitempty"`
	EndRune            *int    `json:"end_rune,omitempty"`
}

type CitationView struct {
	Citation     CitationSnapshot            `json:"citation"`
	SourceTitle  string                      `json:"source_title"`
	SourceFormat source.Format               `json:"source_format"`
	UnitKind     string                      `json:"unit_kind,omitempty"`
	Preview      string                      `json:"preview,omitempty"`
	Coordinate   *normalize.SourceCoordinate `json:"coordinate,omitempty"`
}

func (s *Store) ByInputMessage(ctx context.Context, messageID string) (RunRef, error) {
	var run RunRef
	err := s.db.QueryRow(ctx, `
		select run.id,run.status
		from agent_runs run
		left join chat_runs product on product.root_agent_run_id=run.id
		where coalesce(run.input_message_id,product.input_message_id)=$1
		  and ((run.runtime_kind='legacy_role' and run.agent_role='leader') or run.runtime_kind='configured')
		order by run.created_at,run.id
		limit 1`, messageID).Scan(&run.ID, &run.Status)
	return run, err
}

func (s *Store) ActiveByUser(ctx context.Context, userID string) (RunRef, bool, error) {
	var run RunRef
	err := s.db.QueryRow(ctx, `
		select run.id,run.status
		from agent_runs run
		left join chat_runs product on product.root_agent_run_id=run.id
		where coalesce(run.user_id,product.user_id)=$1
		  and ((run.runtime_kind='legacy_role' and run.agent_role='leader') or run.runtime_kind='configured')
		  and run.status in ('queued','running')`, userID).Scan(&run.ID, &run.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunRef{}, false, nil
	}
	if err != nil {
		return RunRef{}, false, err
	}
	return run, true, nil
}

// ExpireIfOverdue atomically terminalizes every matching active Run whose
// admission-pinned deadline has passed. Empty filters are used by the Worker;
// request-principal callers pass both user and/or Run identity.
func (s *Store) ExpireIfOverdue(ctx context.Context, userID, runID string) (int, error) {
	return s.ExpireIfOverdueWithMetrics(ctx, userID, runID, nil)
}

// ExpireIfOverdueWithMetrics is ExpireIfOverdue plus Sprint 12 Task-terminal
// metrics emission (docs/sprint/SPRINT-12-PRD.md criterion 20: deadline
// exhaustion is an "expired" outcome, attributed to the system, not the
// Member). Metrics are emitted once each overdue Run's transition SQL has
// succeeded, before the caller's enclosing transaction commits — the same
// small gap every write-then-emit call site in this Sprint accepts, since a
// metrics counter cannot itself participate in a Postgres transaction.
func (s *Store) ExpireIfOverdueWithMetrics(ctx context.Context, userID, runID string, metrics *TaskMetricsRecorder) (int, error) {
	rows, err := s.db.Query(ctx, `
		select r.id, j.id, j.attempt_no, j.status, r.definition_identity, r.definition_version, r.created_at
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		left join agent_trees tree on tree.id=r.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		where r.status in ('queued', 'running')
			and j.status in ('queued', 'running', 'waiting')
			and coalesce(r.deadline_at,tree.absolute_deadline) <= now()
			and ($1 = '' or coalesce(r.user_id,product.user_id) = $1)
			and ($2 = '' or r.id = $2 or exists(
				select 1 from agent_run_delegations d where d.parent_run_id=$2 and d.child_run_id=r.id
			) or exists(
				select 1 from agent_subagents d where d.parent_run_id=$2 and d.child_run_id=r.id
			))
		-- Expire children before their root so root cleanup cannot cancel a
		-- child already selected for deadline expiration in this transaction.
		order by exists(select 1 from agent_subagents d where d.child_run_id=r.id) desc,r.id
		for update of r, j`, userID, runID)
	if err != nil {
		return 0, err
	}
	type overdueRun struct {
		runID              string
		jobID              string
		attemptNo          int
		jobStatus          string
		definitionIdentity *string
		definitionVersion  *int
		admittedAt         time.Time
	}
	overdue := make([]overdueRun, 0)
	for rows.Next() {
		var item overdueRun
		if err := rows.Scan(&item.runID, &item.jobID, &item.attemptNo, &item.jobStatus,
			&item.definitionIdentity, &item.definitionVersion, &item.admittedAt); err != nil {
			rows.Close()
			return 0, err
		}
		overdue = append(overdue, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, item := range overdue {
		runTag, err := s.db.Exec(ctx, `
			update agent_runs
			set status = 'failed', error_code = 'run_deadline_exceeded',
				finished_at = now(), updated_at = now()
			where id = $1 and status in ('queued', 'running') and coalesce(
				deadline_at,(select absolute_deadline from agent_trees where agent_trees.id=agent_runs.tree_id)
			) <= now()`, item.runID)
		if err != nil {
			return 0, err
		}
		jobTag, err := s.db.Exec(ctx, `
			update agent_jobs
			set status = 'failed', lease_token = null, lease_expires_at = null,
				finished_at = now(), updated_at = now()
			where id = $1 and status in ('queued', 'running', 'waiting')`, item.jobID)
		if err != nil {
			return 0, err
		}
		if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
			return 0, errors.New("deadline expiry did not transition Run and Job together")
		}
		if _, err := s.db.Exec(ctx, `
			update agent_run_delegations
			set state='failed',error_code='run_deadline_exceeded',completed_at=now(),updated_at=now()
			where child_run_id=$1 and state='waiting'
		`, item.runID); err != nil {
			return 0, err
		}
		tx, ok := s.db.(pgx.Tx)
		if !ok {
			return 0, errors.New("deadline expiry requires a transaction")
		}
		if err := FailResearchPayloadInTx(ctx, tx, item.runID, "research_deadline_exceeded"); err != nil {
			return 0, err
		}
		terminalAttemptNo := item.attemptNo
		if item.jobStatus == "waiting" {
			terminalAttemptNo = 0
		}
		if err := RecordRunTerminalInTx(ctx, tx, item.runID, RunTerminalTrace{
			CauseEvent: TraceEventDeadlineExpired,
			RunStatus:  "failed",
			SpanStatus: agentobs.StatusError,
			ErrorCode:  "run_deadline_exceeded",
			AttemptNo:  terminalAttemptNo,
		}); err != nil {
			return 0, err
		}
		if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, item.runID); err != nil {
			return 0, err
		}
		if metrics != nil {
			identity := ""
			if item.definitionIdentity != nil {
				identity = *item.definitionIdentity
			}
			version := 0
			if item.definitionVersion != nil {
				version = *item.definitionVersion
			}
			taskKind, taskVariant := ClassifyTask(identity, version)
			metrics.RecordAttempt(taskKind, taskVariant, string(AttemptTerminal))
			metrics.RecordTerminal(taskKind, taskVariant, "expired", terminalAttemptNo, item.admittedAt)
			metrics.RecordError(taskKind, "lifecycle", "run_deadline_exceeded")
		}
	}
	return len(overdue), nil
}

func (s *Store) ActiveForChat(ctx context.Context, userID, chatID string) (RunSnapshot, bool, error) {
	var run RunSnapshot
	err := s.db.QueryRow(ctx, `
		select run.id,coalesce(run.input_message_id,product.input_message_id),run.status,run.error_code
		from agent_runs run
		left join chat_runs product on product.root_agent_run_id=run.id
		where coalesce(run.user_id,product.user_id)=$1 and coalesce(run.chat_id,product.chat_id)=$2
		  and ((run.runtime_kind='legacy_role' and run.agent_role='leader') or run.runtime_kind='configured')
		  and run.status in ('queued','running')`, userID, chatID).
		Scan(&run.ID, &run.InputMessageID, &run.Status, &run.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, nil
	}
	if err != nil {
		return RunSnapshot{}, false, err
	}
	return run, true, nil
}

func (s *Store) ProjectionForUser(ctx context.Context, userID, runID string) (RunProjection, error) {
	var projection RunProjection
	var outputMessageID *string
	err := s.db.QueryRow(ctx, `
		select run.id,coalesce(run.input_message_id,product.input_message_id),run.status,run.error_code,
			coalesce(run.output_message_id,product.output_message_id),run.discovery_session_id,
			run.started_at,run.finished_at
		from agent_runs run
		left join chat_runs product on product.root_agent_run_id=run.id
		where run.id=$1 and coalesce(run.user_id,product.user_id)=$2
		  and ((run.runtime_kind='legacy_role' and run.agent_role='leader') or run.runtime_kind='configured')`, runID, userID).
		Scan(&projection.Run.ID, &projection.Run.InputMessageID, &projection.Run.Status, &projection.Run.ErrorCode,
			&outputMessageID, &projection.Run.DiscoverySessionID, &projection.Run.StartedAt, &projection.Run.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunProjection{}, ErrRunNotFound
	}
	if err != nil {
		return RunProjection{}, err
	}
	projection.Run.Activities, err = s.publicActivitiesForRun(ctx, runID, projection.Run.Status)
	if err != nil {
		return RunProjection{}, err
	}
	if outputMessageID == nil {
		return projection, nil
	}
	var message AssistantMessageSnapshot
	err = s.db.QueryRow(ctx, `
		select id, role, content, created_at
		from chat_messages
		where id = $1`, *outputMessageID).
		Scan(&message.ID, &message.Role, &message.Content, &message.CreatedAt)
	if err != nil {
		return RunProjection{}, err
	}
	projection.Message = &message
	projection.Citations, err = s.CitationsForRun(ctx, userID, runID)
	if err != nil {
		return RunProjection{}, err
	}
	return projection, nil
}

func (s *Store) CitationsForRun(ctx context.Context, userID, runID string) ([]CitationSnapshot, error) {
	return s.listCitations(ctx, `
		select c.citation_id,c.message_id,c.reference_kind,c.reference_ordinal,c.claim_ordinal,c.citation_ordinal,c.claim_text,
			c.source_id,src.title,c.evidence_revision_id,c.unit_id,c.start_rune,c.end_rune
		from chat_citations c
		join agent_runs r on r.id=c.run_id
		left join agent_trees tree on tree.id=r.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		join chat_chats chat on chat.id=coalesce(r.chat_id,product.chat_id)
		join notebook_memberships member on member.notebook_id=chat.notebook_id and member.user_id=coalesce(r.user_id,product.user_id)
		left join source_sources src on src.id=c.source_id and src.notebook_id=chat.notebook_id
		where c.run_id=$1 and coalesce(r.user_id,product.user_id)=$2
		order by case when c.reference_kind='source' then 0 else 1 end,c.reference_ordinal,c.claim_ordinal,c.citation_ordinal
	`, runID, userID)
}

func (s *Store) CitationsForChat(ctx context.Context, userID, chatID string) ([]CitationSnapshot, error) {
	return s.listCitations(ctx, `
		select c.citation_id,c.message_id,c.reference_kind,c.reference_ordinal,c.claim_ordinal,c.citation_ordinal,c.claim_text,
			c.source_id,src.title,c.evidence_revision_id,c.unit_id,c.start_rune,c.end_rune
		from chat_citations c
		join agent_runs r on r.id=c.run_id
		left join agent_trees tree on tree.id=r.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		join chat_chats chat on chat.id=coalesce(r.chat_id,product.chat_id)
		join notebook_memberships member on member.notebook_id=chat.notebook_id and member.user_id=coalesce(r.user_id,product.user_id)
		left join source_sources src on src.id=c.source_id and src.notebook_id=chat.notebook_id
		where coalesce(r.chat_id,product.chat_id)=$1 and coalesce(r.user_id,product.user_id)=$2
		order by r.created_at,case when c.reference_kind='source' then 0 else 1 end,c.reference_ordinal,c.claim_ordinal,c.citation_ordinal
	`, chatID, userID)
}

func (s *Store) listCitations(ctx context.Context, query string, args ...any) ([]CitationSnapshot, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CitationSnapshot, 0)
	for rows.Next() {
		var citation CitationSnapshot
		if err := rows.Scan(
			&citation.ID, &citation.MessageID, &citation.ReferenceKind, &citation.ReferenceOrdinal,
			&citation.ClaimOrdinal, &citation.CitationOrdinal, &citation.ClaimText,
			&citation.SourceID, &citation.SourceTitle, &citation.EvidenceRevisionID, &citation.UnitID, &citation.StartRune, &citation.EndRune,
		); err != nil {
			return nil, err
		}
		result = append(result, citation)
	}
	return result, rows.Err()
}

func (s *Store) CitationViewForUser(ctx context.Context, userID, citationID string) (CitationView, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(citationID) == "" {
		return CitationView{}, ErrCitationNotFound
	}
	var view CitationView
	err := s.db.QueryRow(ctx, `
		select c.citation_id,c.message_id,c.reference_kind,c.reference_ordinal,c.claim_ordinal,c.citation_ordinal,c.claim_text,
			c.source_id,src.title,c.evidence_revision_id,c.unit_id,c.start_rune,c.end_rune
		from chat_citations c
		join agent_runs r on r.id=c.run_id
		left join agent_trees tree on tree.id=r.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		join chat_chats chat on chat.id=coalesce(r.chat_id,product.chat_id)
		join notebook_memberships member on member.notebook_id=chat.notebook_id and member.user_id=coalesce(r.user_id,product.user_id)
		left join source_sources src on src.id=c.source_id and src.notebook_id=chat.notebook_id
		where c.citation_id=$1 and coalesce(r.user_id,product.user_id)=$2
	`, citationID, userID).Scan(
		&view.Citation.ID, &view.Citation.MessageID, &view.Citation.ReferenceKind, &view.Citation.ReferenceOrdinal,
		&view.Citation.ClaimOrdinal, &view.Citation.CitationOrdinal,
		&view.Citation.ClaimText, &view.Citation.SourceID, &view.Citation.SourceTitle, &view.Citation.EvidenceRevisionID, &view.Citation.UnitID,
		&view.Citation.StartRune, &view.Citation.EndRune,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CitationView{}, ErrCitationNotFound
	}
	if err != nil {
		return CitationView{}, err
	}
	if view.Citation.ReferenceKind == "source" {
		err = s.db.QueryRow(ctx, `
			select title,format from source_sources where id=$1 and state='ready'
		`, view.Citation.SourceID).Scan(&view.SourceTitle, &view.SourceFormat)
		if errors.Is(err, pgx.ErrNoRows) {
			return CitationView{}, ErrCitationUnavailable
		}
		if err != nil {
			return CitationView{}, err
		}
		return view, nil
	}
	if view.Citation.ReferenceKind != "precise" || view.Citation.EvidenceRevisionID == nil || view.Citation.UnitID == nil ||
		view.Citation.StartRune == nil || view.Citation.EndRune == nil {
		return CitationView{}, ErrCitationUnavailable
	}
	var unitText string
	var coordinateJSON []byte
	err = s.db.QueryRow(ctx, `
		select s.title,s.format,u.kind,u.text_content,u.coordinate_json
		from source_sources s
		join source_evidence_revisions r on r.id=$2 and r.source_id=s.id and r.status='active'
		join source_evidence_units u on u.id=$3 and u.revision_id=r.id and u.source_id=s.id
		where s.id=$1 and s.state='ready'
	`, view.Citation.SourceID, *view.Citation.EvidenceRevisionID, *view.Citation.UnitID).Scan(
		&view.SourceTitle, &view.SourceFormat, &view.UnitKind, &unitText, &coordinateJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CitationView{}, ErrCitationUnavailable
	}
	if err != nil {
		return CitationView{}, err
	}
	runes := []rune(unitText)
	if *view.Citation.StartRune < 0 || *view.Citation.EndRune > len(runes) || *view.Citation.EndRune <= *view.Citation.StartRune {
		return CitationView{}, ErrCitationUnavailable
	}
	view.Preview = string(runes[*view.Citation.StartRune:*view.Citation.EndRune])
	if len(coordinateJSON) > 0 {
		var coordinate normalize.SourceCoordinate
		if json.Unmarshal(coordinateJSON, &coordinate) != nil {
			return CitationView{}, ErrCitationUnavailable
		}
		view.Coordinate = &coordinate
	}
	return view, nil
}

func (s *Store) LatestForChat(ctx context.Context, userID, chatID string) ([]RunSnapshot, error) {
	rows, err := s.db.Query(ctx, `
		select id, input_message_id, status, error_code, discovery_session_id, started_at, finished_at
		from (
			select distinct on (coalesce(r.input_message_id,product.input_message_id))
				r.id,coalesce(r.input_message_id,product.input_message_id) as input_message_id,
				r.status,r.error_code,r.discovery_session_id,r.started_at,r.finished_at,
				m.created_at as input_created_at
			from agent_runs r
			left join chat_runs product on product.root_agent_run_id=r.id
			join chat_messages m on m.id=coalesce(r.input_message_id,product.input_message_id)
			where coalesce(r.user_id,product.user_id)=$1 and coalesce(r.chat_id,product.chat_id)=$2
			  and ((r.runtime_kind='legacy_role' and r.agent_role='leader') or r.runtime_kind='configured')
			order by coalesce(r.input_message_id,product.input_message_id),r.created_at desc,r.id desc
		) latest
		order by input_created_at, input_message_id`, userID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]RunSnapshot, 0)
	for rows.Next() {
		var run RunSnapshot
		if err := rows.Scan(&run.ID, &run.InputMessageID, &run.Status, &run.ErrorCode, &run.DiscoverySessionID, &run.StartedAt, &run.FinishedAt); err != nil {
			return nil, err
		}
		run.Activities, err = s.publicActivitiesForRun(ctx, run.ID, run.Status)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) Cancel(ctx context.Context, userID, runID string) (RunSnapshot, error) {
	return s.CancelWithMetrics(ctx, userID, runID, nil)
}

// CancelWithMetrics is Cancel plus Sprint 12 Task-terminal metrics emission
// for the "cancelled" outcome (PRD criterion 19: excluded from the success
// rate numerator and denominator by the recording rule, but still counted).
func (s *Store) CancelWithMetrics(ctx context.Context, userID, runID string, metrics *TaskMetricsRecorder) (RunSnapshot, error) {
	var run RunSnapshot
	var jobID string
	var attemptNo int
	var jobStatus string
	var definitionIdentity *string
	var definitionVersion *int
	var admittedAt time.Time
	err := s.db.QueryRow(ctx, `
		select r.id,coalesce(r.input_message_id,product.input_message_id),r.status,r.error_code,j.id,j.attempt_no,j.status,
			r.definition_identity, r.definition_version, r.created_at
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		left join chat_runs product on product.root_agent_run_id=r.id
		where r.id=$1 and coalesce(r.user_id,product.user_id)=$2
		  and ((r.runtime_kind='legacy_role' and r.agent_role='leader') or r.runtime_kind='configured')
		for update of r, j`, runID, userID).
		Scan(&run.ID, &run.InputMessageID, &run.Status, &run.ErrorCode, &jobID, &attemptNo, &jobStatus,
			&definitionIdentity, &definitionVersion, &admittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, ErrRunNotFound
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	if run.Status == "cancelled" {
		return run, nil
	}
	if run.Status == "completed" || run.Status == "failed" {
		return RunSnapshot{}, ErrRunNotCancellable
	}
	tx, ok := s.db.(pgx.Tx)
	if !ok {
		return RunSnapshot{}, errors.New("Run cancellation requires a transaction")
	}
	var childRunID string
	var childAttemptNo int
	childErr := s.db.QueryRow(ctx, `
		select child.id,child_job.attempt_no
		from agent_run_delegations delegation
		join agent_runs child on child.id=delegation.child_run_id
		join agent_jobs child_job on child_job.run_id=child.id
		where delegation.parent_run_id=$1 and (
			(child.runtime_kind='legacy_role' and child.agent_role='research')
			or (child.runtime_kind='configured' and child.executor_identity='research')
		)
		for update of child,child_job
	`, runID).Scan(&childRunID, &childAttemptNo)
	if childErr != nil && !errors.Is(childErr, pgx.ErrNoRows) {
		return RunSnapshot{}, childErr
	}
	if childErr == nil {
		if err := (DelegationKernel{}).CancelTreeInTx(ctx, tx, runID, "research_cancelled", time.Now().UTC()); err != nil {
			return RunSnapshot{}, err
		}
		if err := RecordRunTerminalInTx(ctx, tx, childRunID, RunTerminalTrace{
			CauseEvent: TraceEventCancellation, RunStatus: "cancelled", SpanStatus: agentobs.StatusCancelled, AttemptNo: childAttemptNo,
		}); err != nil {
			return RunSnapshot{}, err
		}
		if err := FailResearchPayloadInTx(ctx, tx, childRunID, "research_cancelled"); err != nil {
			return RunSnapshot{}, err
		}
	}
	runTag, err := s.db.Exec(ctx, `
		update agent_runs
		set status = 'cancelled', error_code = null, finished_at = now(), updated_at = now()
		where id = $1 and status in ('queued', 'running')`, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	jobTag, err := s.db.Exec(ctx, `
		update agent_jobs
		set status = 'cancelled', lease_token = null, lease_expires_at = null,
			finished_at = now(), updated_at = now()
		where id = $1 and status in ('queued', 'running', 'waiting')`, jobID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
		return RunSnapshot{}, ErrRunNotCancellable
	}
	terminalAttemptNo := attemptNo
	if jobStatus == "waiting" {
		terminalAttemptNo = 0
	}
	if err := RecordRunTerminalInTx(ctx, tx, runID, RunTerminalTrace{
		CauseEvent: TraceEventCancellation,
		RunStatus:  "cancelled",
		SpanStatus: agentobs.StatusCancelled,
		AttemptNo:  terminalAttemptNo,
	}); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, runID); err != nil {
		return RunSnapshot{}, err
	}
	if metrics != nil {
		identity := ""
		if definitionIdentity != nil {
			identity = *definitionIdentity
		}
		version := 0
		if definitionVersion != nil {
			version = *definitionVersion
		}
		taskKind, taskVariant := ClassifyTask(identity, version)
		metrics.RecordAttempt(taskKind, taskVariant, string(AttemptAbandoned))
		metrics.RecordTerminal(taskKind, taskVariant, "cancelled", terminalAttemptNo, admittedAt)
	}
	run.Status = "cancelled"
	run.ErrorCode = nil
	return run, nil
}

func (s *Store) RetryQueued(ctx context.Context, userID, sourceRunID, key, requestHash, runID, jobID, timeZone string, config RunConfig) (RunSnapshot, bool, error) {
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "admit_agent_run:"+userID); err != nil {
		return RunSnapshot{}, false, err
	}
	var existingHash, existingJSON string
	err := s.db.QueryRow(ctx, `
		select request_hash, response_json::text
		from platform_idempotency_keys
		where principal_id = $1 and action = 'retry_agent_run' and key = $2`, userID, key).
		Scan(&existingHash, &existingJSON)
	if err == nil {
		if existingHash != requestHash {
			return RunSnapshot{}, false, ErrIdempotencyMismatch
		}
		var body struct {
			Run RunSnapshot `json:"run"`
		}
		if err := json.Unmarshal([]byte(existingJSON), &body); err != nil {
			return RunSnapshot{}, false, err
		}
		return body.Run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, err
	}
	if _, err := s.ExpireIfOverdue(ctx, userID, ""); err != nil {
		return RunSnapshot{}, false, err
	}

	var inputMessageID, chatID, model, promptVersion, status string
	err = s.db.QueryRow(ctx, `
		select input_message_id, chat_id, model, prompt_version, status
		from agent_runs
		where id = $1 and user_id = $2 and agent_role='leader'
		for update`, sourceRunID, userID).
		Scan(&inputMessageID, &chatID, &model, &promptVersion, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, ErrRunNotFound
	}
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if status != "failed" && status != "cancelled" {
		return RunSnapshot{}, false, ErrRunNotRetryable
	}
	var latestRunID, latestMessageID string
	var completed bool
	err = s.db.QueryRow(ctx, `
		select
			(select id from agent_runs where input_message_id = $1 and agent_role='leader' order by created_at desc, id desc limit 1),
			(select id from chat_messages where chat_id = $2 order by created_at desc, id desc limit 1),
			exists(select 1 from agent_runs where input_message_id = $1 and agent_role='leader' and status = 'completed')`,
		inputMessageID, chatID).Scan(&latestRunID, &latestMessageID, &completed)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if latestRunID != sourceRunID || latestMessageID != inputMessageID {
		return RunSnapshot{}, false, ErrRetryNotLatest
	}
	if completed {
		return RunSnapshot{}, false, ErrRunNotRetryable
	}
	if _, active, err := s.ActiveByUser(ctx, userID); err != nil {
		return RunSnapshot{}, false, err
	} else if active {
		return RunSnapshot{}, false, ErrActiveRun
	}
	sourceIDs, err := s.sourceIDsForRun(ctx, sourceRunID)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if err := s.CreateQueued(ctx, runID, userID, chatID, inputMessageID, model, promptVersion, timeZone, config); err != nil {
		return RunSnapshot{}, false, err
	}
	if err := s.PinEvidenceSet(ctx, runID, userID, sourceIDs); err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into agent_jobs(id, kind, run_id, status)
		values($1, 'agent_run', $2, 'queued')`, jobID, runID); err != nil {
		return RunSnapshot{}, false, err
	}
	tx, ok := s.db.(pgx.Tx)
	if !ok {
		return RunSnapshot{}, false, errors.New("Run Retry requires a transaction")
	}
	sourceTrace, err := NewOwnedRunTraceRecorder(ctx, tx, sourceRunID)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	retryFrom := sourceTrace.RootSpanContext()
	if err := StartRunTraceInTx(ctx, tx, runID, model, promptVersion, &retryFrom); err != nil {
		return RunSnapshot{}, false, err
	}
	run := RunSnapshot{ID: runID, InputMessageID: inputMessageID, Status: "queued"}
	response, err := json.Marshal(map[string]any{"run": run})
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id, action, key, request_hash, status_code, response_json)
		values($1, 'retry_agent_run', $2, $3, $4, $5::jsonb)`, userID, key, requestHash, http.StatusAccepted, string(response)); err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_jobs', $1)`, jobID); err != nil {
		return RunSnapshot{}, false, err
	}
	return run, false, nil
}

func (s *Store) RetryConfiguredQueued(ctx context.Context, userID, sourceRunID, key, requestHash, jobID string, command ConfiguredChatAdmission) (RunSnapshot, bool, error) {
	if command.UserID != userID {
		return RunSnapshot{}, false, ErrRunNotFound
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,0))`, "admit_agent_run:"+userID); err != nil {
		return RunSnapshot{}, false, err
	}
	var existingHash, existingJSON string
	err := s.db.QueryRow(ctx, `
		select request_hash,response_json::text from platform_idempotency_keys
		where principal_id=$1 and action='retry_agent_run' and key=$2
	`, userID, key).Scan(&existingHash, &existingJSON)
	if err == nil {
		if existingHash != requestHash {
			return RunSnapshot{}, false, ErrIdempotencyMismatch
		}
		var body struct {
			Run RunSnapshot `json:"run"`
		}
		if err := json.Unmarshal([]byte(existingJSON), &body); err != nil {
			return RunSnapshot{}, false, err
		}
		return body.Run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, err
	}
	if _, err := s.ExpireIfOverdue(ctx, userID, ""); err != nil {
		return RunSnapshot{}, false, err
	}
	var inputMessageID, chatID, status string
	err = s.db.QueryRow(ctx, `
		select product.input_message_id,product.chat_id,run.status
		from agent_runs run join chat_runs product on product.root_agent_run_id=run.id
		where run.id=$1 and product.user_id=$2
		for update of run,product
	`, sourceRunID, userID).Scan(&inputMessageID, &chatID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, ErrRunNotFound
	}
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if status != "failed" && status != "cancelled" {
		return RunSnapshot{}, false, ErrRunNotRetryable
	}
	command.ChatID = chatID
	command.InputMessageID = inputMessageID
	var latestRunID, latestMessageID string
	var completed bool
	err = s.db.QueryRow(ctx, `
		select
			(select run.id from agent_runs run left join chat_runs product on product.root_agent_run_id=run.id
			 where coalesce(run.input_message_id,product.input_message_id)=$1
			   and ((run.runtime_kind='legacy_role' and run.agent_role='leader') or run.runtime_kind='configured')
			 order by run.created_at desc,run.id desc limit 1),
			(select id from chat_messages where chat_id=$2 order by created_at desc,id desc limit 1),
			exists(select 1 from agent_runs run left join chat_runs product on product.root_agent_run_id=run.id
			 where coalesce(run.input_message_id,product.input_message_id)=$1 and run.status='completed'
			   and ((run.runtime_kind='legacy_role' and run.agent_role='leader') or run.runtime_kind='configured'))
	`, inputMessageID, chatID).Scan(&latestRunID, &latestMessageID, &completed)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if latestRunID != sourceRunID || latestMessageID != inputMessageID {
		return RunSnapshot{}, false, ErrRetryNotLatest
	}
	if completed {
		return RunSnapshot{}, false, ErrRunNotRetryable
	}
	if _, active, err := s.ActiveByUser(ctx, userID); err != nil {
		return RunSnapshot{}, false, err
	} else if active {
		return RunSnapshot{}, false, ErrActiveRun
	}
	sourceIDs, err := s.sourceIDsForRun(ctx, sourceRunID)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if err := s.CreateConfiguredChatQueued(ctx, command); err != nil {
		return RunSnapshot{}, false, err
	}
	if err := s.PinEvidenceSet(ctx, command.RunID, userID, sourceIDs); err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `insert into agent_jobs(id,kind,run_id,status) values($1,'agent_run',$2,'queued')`, jobID, command.RunID); err != nil {
		return RunSnapshot{}, false, err
	}
	tx, ok := s.db.(pgx.Tx)
	if !ok {
		return RunSnapshot{}, false, errors.New("configured Run Retry requires a transaction")
	}
	sourceTrace, err := NewOwnedRunTraceRecorder(ctx, tx, sourceRunID)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	retryFrom := sourceTrace.RootSpanContext()
	if err := StartRunTraceInTx(ctx, tx, command.RunID, command.ModelPolicy.ProviderModel, command.Definition.Reference().String(), &retryFrom); err != nil {
		return RunSnapshot{}, false, err
	}
	if err := s.FinalizeConfiguredChatOwnership(ctx, command.RunID); err != nil {
		return RunSnapshot{}, false, err
	}
	run := RunSnapshot{ID: command.RunID, InputMessageID: inputMessageID, Status: "queued"}
	response, err := json.Marshal(map[string]any{"run": run})
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id,action,key,request_hash,status_code,response_json)
		values($1,'retry_agent_run',$2,$3,$4,$5::jsonb)
	`, userID, key, requestHash, http.StatusAccepted, string(response)); err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_jobs',$1)`, jobID); err != nil {
		return RunSnapshot{}, false, err
	}
	return run, false, nil
}

func (s *Store) sourceIDsForRun(ctx context.Context, runID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `select source_id from agent_run_evidence_set where run_id=$1 order by ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			return nil, err
		}
		result = append(result, sourceID)
	}
	return result, rows.Err()
}

type Store struct {
	db DBTX
}

type ConfiguredChatAdmission struct {
	RunID           string
	UserID          string
	ChatID          string
	InputMessageID  string
	Definition      agentcatalog.Definition
	ModelPolicy     agentcatalog.ModelPolicy
	ModelContext    agentcatalog.ResolvedModelContextPolicy
	DeadlineAt      time.Time
	ContextManifest json.RawMessage
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) CreateConfiguredChatQueued(ctx context.Context, command ConfiguredChatAdmission) error {
	if s == nil || s.db == nil || strings.TrimSpace(command.RunID) == "" || strings.TrimSpace(command.UserID) == "" ||
		strings.TrimSpace(command.ChatID) == "" || strings.TrimSpace(command.InputMessageID) == "" ||
		command.Definition.Reference().Identity == "" || len(command.Definition.SHA256) != 64 ||
		command.ModelPolicy.Reference() != command.Definition.ModelPolicy || len(command.ModelPolicy.SHA256) != 64 ||
		command.ModelContext.Policy.InvocationModelPolicy != command.ModelPolicy.Reference() ||
		command.ModelContext.Policy.PinnedMaxOutputTokens != command.ModelPolicy.MaxOutputTokens ||
		command.ModelContext.Capability.ProviderModel != command.ModelPolicy.ProviderModel ||
		len(command.ModelContext.Policy.SHA256) != 64 || len(command.ModelContext.Capability.SHA256) != 64 ||
		!command.DeadlineAt.After(time.Now()) {
		return errors.New("invalid configured Chat admission")
	}
	manifest, err := CanonicalJSONObject(command.ContextManifest)
	if err != nil || len(manifest) > command.Definition.Limits.ContextBytes {
		return errors.New("invalid configured Chat context manifest")
	}
	treeID := "tree_" + command.RunID
	if _, err := s.db.Exec(ctx, `
		insert into agent_trees(
			id,absolute_deadline,model_call_limit,action_limit,context_byte_limit,result_byte_limit,context_bytes_consumed
		) values($1,$2,$3,$4,$5,$6,$7)
	`, treeID, command.DeadlineAt, command.Definition.Limits.ModelCalls, command.Definition.Limits.Actions,
		command.Definition.Limits.ContextBytes, command.Definition.Limits.ResultBytes, len(manifest)); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		insert into agent_runs(
			id,user_id,status,runtime_kind,tree_id,definition_identity,definition_version,definition_sha256,
			executor_identity,model_policy_identity,model_policy_version,model_policy_sha256,provider_model,
			provider_capability_identity,provider_capability_version,provider_capability_sha256,
			model_context_policy_identity,model_context_policy_version,model_context_policy_sha256,parent_context_manifest
		) values($1,$2,'queued','configured',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb)
	`, command.RunID, command.UserID, treeID,
		command.Definition.Identity, command.Definition.Version, command.Definition.SHA256, command.Definition.Executor,
		command.ModelPolicy.Identity, command.ModelPolicy.Version, command.ModelPolicy.SHA256, command.ModelPolicy.ProviderModel,
		command.ModelContext.Capability.Identity, command.ModelContext.Capability.Version, command.ModelContext.Capability.SHA256,
		command.ModelContext.Policy.Identity, command.ModelContext.Policy.Version, command.ModelContext.Policy.SHA256,
		string(manifest)); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		insert into chat_runs(id,user_id,chat_id,input_message_id,root_agent_run_id,status)
		values($1,$2,$3,$4,$1,'queued')
	`, command.RunID, command.UserID, command.ChatID, command.InputMessageID); err != nil {
		return err
	}
	if result, err := s.db.Exec(ctx, `update agent_trees set root_agent_run_id=$2,updated_at=now() where id=$1 and root_agent_run_id is null`, treeID, command.RunID); err != nil {
		return err
	} else if result.RowsAffected() != 1 {
		return errors.New("configured Agent Tree root was not pinned")
	}
	return nil
}

func (s *Store) CreateConfiguredResearchPlanningQueued(ctx context.Context, sessionID string, command ConfiguredChatAdmission) error {
	if strings.TrimSpace(sessionID) == "" || command.Definition.Executor != "research_planner" {
		return errors.New("invalid configured Research planning admission")
	}
	if err := s.CreateConfiguredChatQueued(ctx, command); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		insert into research_sessions(id,user_id,chat_id,input_message_id,status,planning_run_id)
		values($1,$2,$3,$4,'planning',$5)
	`, sessionID, command.UserID, command.ChatID, command.InputMessageID, command.RunID); err != nil {
		return err
	}
	return nil
}

// ErrResearchPlanNotRevisable reports a revision request for a session that
// is not waiting for plan confirmation.
var ErrResearchPlanNotRevisable = errors.New("Research Plan is not awaiting confirmation")

// CreateConfiguredResearchPlanningTurnQueued admits a plan revision turn: a
// new planning Run for the same session that continues the planning
// conversation from the latest plan version.
func (s *Store) CreateConfiguredResearchPlanningTurnQueued(ctx context.Context, sessionID string, command ConfiguredChatAdmission) error {
	if strings.TrimSpace(sessionID) == "" || command.Definition.Executor != "research_planner" {
		return errors.New("invalid configured Research planning turn")
	}
	var previousRunID, inputMessageID string
	if err := s.db.QueryRow(ctx, `
		select planning_run_id,input_message_id from research_sessions
		where id=$1 and status='awaiting_confirmation' and planning_run_id is not null
		for update
	`, sessionID).Scan(&previousRunID, &inputMessageID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrResearchPlanNotRevisable
		}
		return err
	}
	if err := s.CreateConfiguredChatQueued(ctx, command); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		insert into research_planning_turns(session_id,turn_no,run_id,input_message_id)
		values($1,0,$2,$3) on conflict(session_id,turn_no) do nothing
	`, sessionID, previousRunID, inputMessageID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		insert into research_planning_turns(session_id,turn_no,run_id,input_message_id,base_plan_version)
		values($1,(select max(turn_no)+1 from research_planning_turns where session_id=$1),$2,$3,
			(select max(version) from research_plan_versions where session_id=$1))
	`, sessionID, command.RunID, command.InputMessageID); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `
		update research_sessions set planning_run_id=$2,status='planning',updated_at=now()
		where id=$1 and status='awaiting_confirmation'
	`, sessionID, command.RunID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrResearchPlanNotRevisable
	}
	return nil
}

func (s *Store) FinalizeConfiguredChatOwnership(ctx context.Context, runID string) error {
	if s == nil || s.db == nil || strings.TrimSpace(runID) == "" {
		return errors.New("invalid configured Chat ownership finalization")
	}
	result, err := s.db.Exec(ctx, `update agent_runs set user_id=null where id=$1 and runtime_kind='configured' and user_id is not null`, runID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("configured Agent Run ownership was not finalized")
	}
	return nil
}

func (s *Store) CreateQueued(ctx context.Context, runID, userID, chatID, inputMessageID, model, promptVersion, timeZone string, config RunConfig) error {
	if config.ID == "" {
		config.ID = "nano-interactive-v1"
	}
	if config.ExecutorVersion == "" {
		config.ExecutorVersion = "leader-executor-v1"
	}
	var deadlineAt time.Time
	err := s.db.QueryRow(ctx, `
		insert into agent_runs(
			id, user_id, chat_id, input_message_id, status, model, prompt_version, agent_config_id, executor_version,
			time_zone, deadline_at, action_decision_limit, final_decision_limit,
			action_limit, action_batch_limit, action_result_byte_limit, action_results_byte_limit
		)
		values(
			$1, $2, $3, $4, 'queued', $5, $6, $7, $8,
			$9, now() + ($10 * interval '1 millisecond'), $11, $12, $13, $14, $15, $16
		)
		returning deadline_at`,
		runID, userID, chatID, inputMessageID, model, promptVersion, config.ID, config.ExecutorVersion,
		timeZone, config.Deadline.Milliseconds(), config.ActionDecisionLimit, config.FinalDecisionLimit,
		config.ActionLimit, config.ActionBatchLimit, config.ActionResultByteLimit, config.ActionResultsByteLimit,
	).Scan(&deadlineAt)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		insert into chat_runs(id,user_id,chat_id,input_message_id,root_agent_run_id,status)
		values($1,$2,$3,$4,$1,'queued')
	`, runID, userID, chatID, inputMessageID); err != nil {
		return err
	}
	treeID := "tree_" + runID
	if _, err := s.db.Exec(ctx, `
		insert into agent_trees(
			id,root_agent_run_id,absolute_deadline,model_call_limit,action_limit,context_byte_limit,result_byte_limit
		) values($1,$2,$3,$4,$5,$6,$7)
	`, treeID, runID, deadlineAt, config.ActionDecisionLimit+config.FinalDecisionLimit,
		config.ActionLimit, 65536, config.ActionResultsByteLimit); err != nil {
		return err
	}
	result, err := s.db.Exec(ctx, `update agent_runs set tree_id=$2 where id=$1 and runtime_kind='legacy_role'`, runID, treeID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("Agent Tree ownership was not pinned")
	}
	return nil
}

// PinEvidenceSet resolves member-supplied Source identities to the exact
// active Evidence Revision and verified active Retrieval Index Version. It
// must run in the same transaction as CreateQueued.
func (s *Store) PinEvidenceSet(ctx context.Context, runID, userID string, sourceIDs []string) error {
	return s.pinEvidenceSet(ctx, runID, userID, sourceIDs, "")
}

// PinEvidenceSetVersion is the offline-Eval admission path. It may pin one
// explicitly identified candidate version, but is intentionally not exposed
// by the user-facing HTTP API.
func (s *Store) PinEvidenceSetVersion(ctx context.Context, runID, userID, versionID string, sourceIDs []string) error {
	if versionID == "" {
		return ErrEvidenceSetInvalid
	}
	return s.pinEvidenceSet(ctx, runID, userID, sourceIDs, versionID)
}

func (s *Store) pinEvidenceSet(ctx context.Context, runID, userID string, sourceIDs []string, versionID string) error {
	if s == nil || s.db == nil || runID == "" || userID == "" || len(sourceIDs) > 50 {
		return ErrEvidenceSetInvalid
	}
	seen := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if sourceID == "" {
			return ErrEvidenceSetInvalid
		}
		if _, duplicate := seen[sourceID]; duplicate {
			return ErrEvidenceSetInvalid
		}
		seen[sourceID] = struct{}{}
	}
	var notebookID string
	if err := s.db.QueryRow(ctx, `
		select coalesce(c.notebook_id,studio.notebook_id)
		from agent_runs r
		left join chat_runs chat_product on chat_product.root_agent_run_id=r.id
		left join chat_chats c on c.id=coalesce(r.chat_id,chat_product.chat_id)
		left join studio_outputs studio on studio.root_agent_run_id=r.id
		where r.id=$1 and coalesce(r.user_id,chat_product.user_id,studio.created_by_user_id)=$2 and r.status='queued'
	`, runID, userID).Scan(&notebookID); err != nil {
		return ErrEvidenceSetInvalid
	}
	for ordinal, sourceID := range sourceIDs {
		var revisionID, indexVersionID string
		query := `
			select r.id, b.index_version_id
			from source_sources s
			join source_evidence_revisions r on r.source_id=s.id and r.status='active'
			join retrieval_source_index_builds b on b.revision_id=r.id and b.source_id=s.id
				and b.notebook_id=s.notebook_id and b.status='verified'
			join retrieval_index_versions v on v.id=b.index_version_id and v.status='active'
			where s.id=$1 and s.notebook_id=$2 and s.state='ready'`
		args := []any{sourceID, notebookID}
		if versionID != "" {
			query = `
				select r.id, b.index_version_id
				from source_sources s
				join source_evidence_revisions r on r.source_id=s.id and r.status='active'
				join retrieval_source_index_builds b on b.revision_id=r.id and b.source_id=s.id
					and b.notebook_id=s.notebook_id and b.status='verified'
				join retrieval_index_versions v on v.id=b.index_version_id and v.id=$3 and v.status='candidate'
				where s.id=$1 and s.notebook_id=$2 and s.state='ready'`
			args = append(args, versionID)
		}
		err := s.db.QueryRow(ctx, query, args...).Scan(&revisionID, &indexVersionID)
		if err != nil {
			return ErrEvidenceSetInvalid
		}
		if _, err := s.db.Exec(ctx, `
			insert into agent_run_evidence_set(
				run_id, ordinal, notebook_id, source_id, evidence_revision_id, index_version_id
			) values($1,$2,$3,$4,$5,$6)
		`, runID, ordinal, notebookID, sourceID, revisionID, indexVersionID); err != nil {
			return err
		}
	}
	tag, err := s.db.Exec(ctx, `
		update agent_runs set selected_source_count=$3, updated_at=now()
		where id=$1 and status='queued' and (
			user_id=$2
			or exists(select 1 from chat_runs product where product.root_agent_run_id=agent_runs.id and product.user_id=$2)
			or exists(select 1 from studio_outputs product where product.root_agent_run_id=agent_runs.id and product.created_by_user_id=$2)
		)
	`, runID, userID, len(sourceIDs))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrEvidenceSetInvalid
	}
	return nil
}

func (s *Store) EvidenceSetMatches(ctx context.Context, runID string, sourceIDs []string) (bool, error) {
	rows, err := s.db.Query(ctx, `
		select source_id from agent_run_evidence_set where run_id=$1 order by ordinal
	`, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	pinned := make([]string, 0, len(sourceIDs))
	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			return false, err
		}
		pinned = append(pinned, sourceID)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(pinned) != len(sourceIDs) {
		return false, nil
	}
	for index := range pinned {
		if pinned[index] != sourceIDs[index] {
			return false, nil
		}
	}
	return true, nil
}
