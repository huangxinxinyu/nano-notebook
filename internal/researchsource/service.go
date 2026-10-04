// Package researchsource imports public PDFs discovered by Research as
// permanent Notebook Sources without exposing document bytes to the model.
package researchsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/realtime"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool     *pgxpool.Pool
	acquirer webreader.Acquirer
	objects  objectstore.Store
}

type importAuthority struct {
	SessionID  string
	UserID     string
	ChatID     string
	NotebookID string
}

func NewService(pool *pgxpool.Pool, acquirer webreader.Acquirer, objects objectstore.Store) *Service {
	return &Service{pool: pool, acquirer: acquirer, objects: objects}
}

func (s *Service) ImportResearchPDF(ctx context.Context, request agent.ResearchSourceImportRequest) (agent.ResearchSourceImportResult, error) {
	if s == nil || s.pool == nil || s.acquirer == nil || s.objects == nil ||
		request.Attempt.RunID == "" || request.Attempt.JobID == "" || request.Attempt.LeaseToken == "" ||
		request.Attempt.AttemptNo < 1 || strings.TrimSpace(request.ActionID) == "" ||
		len(request.ActionID) > 128 || !utf8.ValidString(request.ActionID) {
		return agent.ResearchSourceImportResult{}, agent.ErrResearchSourceImportUnavailable
	}
	readerRequest := webreader.Request{URL: request.URL, Format: webreader.FormatMarkdown, MaxChars: webreader.MaxContentChars}
	if err := readerRequest.Validate(); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}

	authority, replayed, found, err := s.loadAuthorityAndReplay(ctx, request)
	if err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if found {
		replayed.Reused = true
		return replayed, nil
	}

	content, err := s.acquirer.Acquire(ctx, readerRequest)
	if err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if content.MediaType != webreader.MediaTypePDF {
		return agent.ResearchSourceImportResult{}, webreader.ErrUnsupportedType
	}
	if len(content.PDF) < 5 || len(content.PDF) > webreader.MaxPDFBytes || !bytes.HasPrefix(content.PDF, []byte("%PDF-")) {
		return agent.ResearchSourceImportResult{}, webreader.ErrDocumentTypeMismatch
	}
	finalURL, err := source.CanonicalURLIdentity(content.FinalURL)
	if err != nil {
		return agent.ResearchSourceImportResult{}, webreader.ErrResponseInvalid
	}
	requestedURL, err := source.CanonicalURLIdentity(request.URL)
	if err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	digest := sha256.Sum256(content.PDF)
	contentSHA := hex.EncodeToString(digest[:])
	objectKey := fmt.Sprintf("sources/notebooks/%s/content/%s/original.pdf", authority.NotebookID, contentSHA)
	if err := s.objects.Put(ctx, objectKey, content.PDF); err != nil {
		return agent.ResearchSourceImportResult{}, agent.ErrResearchSourceImportUnavailable
	}
	persisted, err := s.objects.Get(ctx, objectKey, int64(len(content.PDF)))
	if err != nil || !bytes.Equal(persisted, content.PDF) {
		return agent.ResearchSourceImportResult{}, agent.ErrResearchSourceImportUnavailable
	}
	return s.commitImport(ctx, request, authority, requestedURL, finalURL, contentSHA, objectKey, int64(len(content.PDF)))
}

func (s *Service) loadAuthorityAndReplay(
	ctx context.Context,
	request agent.ResearchSourceImportRequest,
) (importAuthority, agent.ResearchSourceImportResult, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return importAuthority{}, agent.ResearchSourceImportResult{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return importAuthority{}, agent.ResearchSourceImportResult{}, false, err
	}
	authority, err := resolveAuthority(ctx, tx, request.Attempt)
	if err != nil {
		return importAuthority{}, agent.ResearchSourceImportResult{}, false, err
	}
	result, found, err := loadActionImport(ctx, tx, request.Attempt.RunID, request.ActionID)
	if err != nil {
		return importAuthority{}, agent.ResearchSourceImportResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return importAuthority{}, agent.ResearchSourceImportResult{}, false, err
	}
	return authority, result, found, nil
}

func (s *Service) commitImport(
	ctx context.Context,
	request agent.ResearchSourceImportRequest,
	authority importAuthority,
	requestedURL, finalURL, contentSHA, objectKey string,
	byteSize int64,
) (agent.ResearchSourceImportResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,0))`, "research-source-action:"+request.Attempt.RunID+":"+request.ActionID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	currentAuthority, err := resolveAuthority(ctx, tx, request.Attempt)
	if err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if currentAuthority != authority {
		return agent.ResearchSourceImportResult{}, agent.ErrResearchSourceImportUnavailable
	}
	if replayed, found, err := loadActionImport(ctx, tx, request.Attempt.RunID, request.ActionID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
		replayed.Reused = true
		return replayed, nil
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,0))`, "source-notebook:"+authority.NotebookID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}

	var sourceID, processingJobID string
	var state source.State
	var storedFinalURL, jobStatus string
	reused := false
	err = tx.QueryRow(ctx, `
		select source.id,job.id,source.state,coalesce(source.final_url,$4),job.status
		from source_sources source
		join source_processing_jobs job on job.source_id=source.id
		where source.notebook_id=$1
		  and (
			(source.input_kind='url' and (source.origin_url_identity=any($2::text[]) or source.final_url_identity=any($2::text[])))
			or (source.format='pdf' and source.content_sha256=$3)
		  )
		order by
			case when source.final_url_identity=$4 then 0 when source.content_sha256=$3 then 1 else 2 end,
			source.created_at,source.id
		limit 1
	`, authority.NotebookID, []string{requestedURL, finalURL}, contentSHA, finalURL).Scan(
		&sourceID, &processingJobID, &state, &storedFinalURL, &jobStatus,
	)
	if err == nil {
		reused = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return agent.ResearchSourceImportResult{}, err
	} else {
		var sourceCount int
		if err := tx.QueryRow(ctx, `select count(*) from source_sources where notebook_id=$1`, authority.NotebookID).Scan(&sourceCount); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
		if sourceCount >= 50 {
			return agent.ResearchSourceImportResult{}, source.ErrQuotaReached
		}
		sourceID = "src_" + uuid.NewString()
		processingJobID = "srcjob_" + uuid.NewString()
		state = source.StateUploaded
		jobStatus = "queued"
		storedFinalURL = finalURL
		if _, err := tx.Exec(ctx, `
			insert into source_sources(
				id,notebook_id,input_kind,format,title,media_type,byte_size,content_sha256,
				original_object_key,origin_url,final_url,origin_url_identity,final_url_identity,state
			) values($1,$2,'url','pdf',$3,'application/pdf',$4,$5,$6,$7,$8,$9,$10,'uploaded')
		`, sourceID, authority.NotebookID, pdfTitle(finalURL), byteSize, contentSHA, objectKey,
			request.URL, finalURL, requestedURL, finalURL); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			insert into source_processing_jobs(id,source_id,notebook_id,status)
			values($1,$2,$3,'queued')
		`, processingJobID, sourceID, authority.NotebookID); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
	}

	if _, err := tx.Exec(ctx, `
		insert into chat_source_selections(chat_id,source_id,selected,explicit,updated_at)
		values($1,$2,true,false,now())
		on conflict(chat_id,source_id) do update
		set selected=case when chat_source_selections.explicit then chat_source_selections.selected else true end,
			updated_at=now()
	`, authority.ChatID, sourceID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into research_source_imports(
			session_id,run_id,action_id,requested_url,final_url_identity,source_id,processing_job_id
		) values($1,$2,$3,$4,$5,$6,$7)
	`, authority.SessionID, request.Attempt.RunID, request.ActionID, request.URL, finalURL, sourceID, processingJobID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	searchable := false
	if state == source.StateReady {
		var revisionID string
		if err := tx.QueryRow(ctx, `
			select revision.id
			from source_evidence_revisions revision
			join retrieval_source_index_builds build on build.revision_id=revision.id
			join retrieval_index_versions version on version.id=build.index_version_id and version.status='active'
			where revision.source_id=$1 and revision.status='active' and build.source_id=$1 and build.status='verified'
			order by revision.revision_no desc limit 1
		`, sourceID).Scan(&revisionID); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
		if err := agent.AttachResearchSourceEvidenceInTx(ctx, tx, sourceID, revisionID); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
		if err := tx.QueryRow(ctx, `select exists(select 1 from agent_run_evidence_set where run_id=$1 and source_id=$2)`, request.Attempt.RunID, sourceID).Scan(&searchable); err != nil {
			return agent.ResearchSourceImportResult{}, err
		}
	}
	if err := realtime.NotifyNotebookSources(ctx, tx, authority.NotebookID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_source_processing_jobs',$1)`, processingJobID); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.ResearchSourceImportResult{}, err
	}
	return importResult(sourceID, processingJobID, state, jobStatus, storedFinalURL, searchable, reused), nil
}

func resolveAuthority(ctx context.Context, tx pgx.Tx, attempt agent.Attempt) (importAuthority, error) {
	var authority importAuthority
	err := tx.QueryRow(ctx, `
		select session.id,session.user_id,session.chat_id,chat.notebook_id
		from agent_runs run
		join research_sessions session on session.execution_run_id=nano_research_root_run(run.id)
		join chat_chats chat on chat.id=session.chat_id
		join notebook_memberships member on member.notebook_id=chat.notebook_id and member.user_id=session.user_id
		join agent_jobs job on job.run_id=run.id
		where run.id=$1 and job.id=$2 and job.status='running' and job.lease_token=$3::uuid
		  and job.attempt_no=$4 and job.lease_expires_at>now()
		  and run.status='running' and run.output_message_id is null
		  and run.executor_identity='research_root'
		  and session.status in ('queued','running')
	`, attempt.RunID, attempt.JobID, attempt.LeaseToken, attempt.AttemptNo).Scan(
		&authority.SessionID, &authority.UserID, &authority.ChatID, &authority.NotebookID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return importAuthority{}, agent.ErrResearchSourceImportUnavailable
	}
	return authority, err
}

func loadActionImport(ctx context.Context, tx pgx.Tx, runID, actionID string) (agent.ResearchSourceImportResult, bool, error) {
	var sourceID, processingJobID, finalURL, jobStatus string
	var state source.State
	var searchable bool
	err := tx.QueryRow(ctx, `
		select source.id,job.id,source.state,source.final_url,job.status,
			exists(select 1 from agent_run_evidence_set evidence where evidence.run_id=relation.run_id and evidence.source_id=source.id)
		from research_source_imports relation
		join source_sources source on source.id=relation.source_id
		join source_processing_jobs job on job.id=relation.processing_job_id and job.source_id=source.id
		where relation.run_id=$1 and relation.action_id=$2
	`, runID, actionID).Scan(&sourceID, &processingJobID, &state, &finalURL, &jobStatus, &searchable)
	if errors.Is(err, pgx.ErrNoRows) {
		var relationExists bool
		if lookupErr := tx.QueryRow(ctx, `select exists(select 1 from research_source_imports where run_id=$1 and action_id=$2)`, runID, actionID).Scan(&relationExists); lookupErr != nil {
			return agent.ResearchSourceImportResult{}, false, lookupErr
		}
		if relationExists {
			// The Source or Job was deleted after admission. Preserve the
			// historical Action identity and never recreate deleted authority.
			return agent.ResearchSourceImportResult{}, false, agent.ErrResearchSourceImportUnavailable
		}
		return agent.ResearchSourceImportResult{}, false, nil
	}
	if err != nil {
		return agent.ResearchSourceImportResult{}, false, err
	}
	return importResult(sourceID, processingJobID, state, jobStatus, finalURL, searchable, true), true, nil
}

func importResult(sourceID, processingJobID string, state source.State, jobStatus, finalURL string, searchable, reused bool) agent.ResearchSourceImportResult {
	lifecycle := "processing"
	switch state {
	case source.StateReady:
		lifecycle = "ready"
	case source.StateFailed:
		lifecycle = "failed"
	case source.StateQualifying:
		if jobStatus == "succeeded" {
			lifecycle = "review_required"
		}
	}
	return agent.ResearchSourceImportResult{
		SourceID: sourceID, ProcessingJobID: processingJobID, State: lifecycle,
		Searchable: searchable, Reused: reused, FinalURL: finalURL,
	}
}

func pdfTitle(finalURL string) string {
	parsed, err := url.Parse(finalURL)
	if err == nil {
		candidate, _ := url.PathUnescape(path.Base(parsed.Path))
		candidate = strings.Join(strings.Fields(candidate), " ")
		if candidate != "" && candidate != "." && candidate != "/" {
			runes := []rune(candidate)
			if len(runes) > 255 {
				runes = runes[:255]
			}
			return string(runes)
		}
		if parsed.Hostname() != "" {
			return parsed.Hostname() + " PDF"
		}
	}
	return "Research PDF"
}
