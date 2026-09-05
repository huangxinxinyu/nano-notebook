package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/researchsource"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
)

type importPDFAcquirer struct {
	mu       sync.Mutex
	contents map[string]webreader.Content
	calls    int
}

type corruptReadObjectStore struct{ objectstore.Store }

func (s corruptReadObjectStore) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	payload, err := s.Store.Get(ctx, key, limit)
	if err == nil && len(payload) > 0 {
		payload[0] ^= 0xff
	}
	return payload, err
}

func (a *importPDFAcquirer) Acquire(_ context.Context, request webreader.Request) (webreader.Content, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.contents[request.URL], nil
}

func (a *importPDFAcquirer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func TestResearchPDFImportPersistsOnePermanentSourceAndReplaysWithoutBodyLeakage(t *testing.T) {
	api := newTestAPI(t)
	claimed, sessionID, chatID, notebookID := admitResearchExecutionForSourceImport(t, api, "research-pdf-import@example.com")
	pdf := researchNativePDF("Permanent PDF evidence with page authority.")
	acquirer := &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/paper.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://cdn.example.com/papers/paper.pdf", PDF: pdf,
		},
	}}
	objects := objectstore.NewMemoryStore()
	service := researchsource.NewService(api.db.Pool(), acquirer, objects)
	listener, err := api.db.Pool().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(context.Background(), `listen nano_source_processing_jobs`); err != nil {
		t.Fatal(err)
	}
	request := agent.ResearchSourceImportRequest{
		URL: "https://example.com/paper.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(claimed),
	}

	first, err := service.ImportResearchPDF(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	notification, notifyErr := listener.Conn().WaitForNotification(waitCtx)
	cancel()
	if notifyErr != nil || notification == nil || notification.Channel != "nano_source_processing_jobs" || notification.Payload != first.ProcessingJobID {
		t.Fatalf("Source worker notification=%+v err=%v", notification, notifyErr)
	}
	if first.SourceID == "" || first.ProcessingJobID == "" || first.State != "processing" || first.Searchable || first.Reused ||
		first.FinalURL != "https://cdn.example.com/papers/paper.pdf" {
		t.Fatalf("first=%+v", first)
	}
	var format, mediaType, sourceState, objectKey, jobStatus, relatedSession, relatedRun, relatedAction string
	var selected, explicit bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select source.format,source.media_type,source.state,source.original_object_key,job.status,
			selection.selected,selection.explicit,relation.session_id,relation.run_id,relation.action_id
		from source_sources source
		join source_processing_jobs job on job.source_id=source.id
		join chat_source_selections selection on selection.source_id=source.id and selection.chat_id=$2
		join research_source_imports relation on relation.source_id=source.id
		where source.id=$1
	`, first.SourceID, chatID).Scan(
		&format, &mediaType, &sourceState, &objectKey, &jobStatus,
		&selected, &explicit, &relatedSession, &relatedRun, &relatedAction,
	); err != nil {
		t.Fatal(err)
	}
	if format != "pdf" || mediaType != webreader.MediaTypePDF || sourceState != "uploaded" || jobStatus != "queued" ||
		!selected || explicit || relatedSession != sessionID || relatedRun != claimed.RunID || relatedAction != request.ActionID {
		t.Fatalf("authority=%s/%s/%s/%s selected=%t explicit=%t relation=%s/%s/%s",
			format, mediaType, sourceState, jobStatus, selected, explicit, relatedSession, relatedRun, relatedAction)
	}
	stored, err := objects.Get(context.Background(), objectKey, webreader.MaxPDFBytes)
	if err != nil || !bytes.Equal(stored, pdf) {
		t.Fatalf("stored bytes=%d err=%v", len(stored), err)
	}

	replayed, err := service.ImportResearchPDF(context.Background(), request)
	if err != nil || !replayed.Reused || replayed.SourceID != first.SourceID || replayed.ProcessingJobID != first.ProcessingJobID ||
		acquirer.callCount() != 1 || objects.Len() != 1 {
		t.Fatalf("replayed=%+v calls=%d objects=%d err=%v", replayed, acquirer.callCount(), objects.Len(), err)
	}
	var sourceCount, jobCount, relationCount int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select
			(select count(*) from source_sources where notebook_id=$1),
			(select count(*) from source_processing_jobs where notebook_id=$1),
			(select count(*) from research_source_imports where run_id=$2)
	`, notebookID, claimed.RunID).Scan(&sourceCount, &jobCount, &relationCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || jobCount != 1 || relationCount != 1 {
		t.Fatalf("counts=%d/%d/%d", sourceCount, jobCount, relationCount)
	}

	if _, err := api.db.Pool().Exec(context.Background(), `
		update research_sessions
		set status='failed',error_code='test_terminal',completed_at=now(),updated_at=now()
		where id=$1
	`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `delete from agent_runs where id=$1`, claimed.RunID); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_sources where id=$1`, first.SourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("Run deletion removed permanent Source count=%d", sourceCount)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from research_source_imports where run_id=$1`, claimed.RunID).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 0 {
		t.Fatalf("Run deletion retained import relation count=%d", relationCount)
	}
}

func TestResearchPDFImportConvergesFinalURLAndExactContentAcrossActions(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, notebookID := admitResearchExecutionForSourceImport(t, api, "research-pdf-dedupe@example.com")
	pdf := researchNativePDF("The same immutable paper bytes.")
	acquirer := &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/download?id=1": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://cdn.example.com/paper.pdf", PDF: pdf,
		},
		"https://cdn.example.com/./paper.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://cdn.example.com/paper.pdf", PDF: pdf,
		},
		"https://mirror.example.org/copy.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://mirror.example.org/copy.pdf", PDF: pdf,
		},
	}}
	service := researchsource.NewService(api.db.Pool(), acquirer, objectstore.NewMemoryStore())
	results := make([]agent.ResearchSourceImportResult, 3)
	urls := []string{
		"https://example.com/download?id=1",
		"https://cdn.example.com/./paper.pdf",
		"https://mirror.example.org/copy.pdf",
	}
	for index, url := range urls {
		var err error
		results[index], err = service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
			URL: url, ActionID: "decision:2/action:" + string(rune('0'+index)), Attempt: attemptFromClaim(claimed),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Reused || !results[1].Reused || !results[2].Reused ||
		results[0].SourceID != results[1].SourceID || results[0].SourceID != results[2].SourceID {
		t.Fatalf("results=%+v", results)
	}
	var sources, jobsCount, relations int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select
			(select count(*) from source_sources where notebook_id=$1),
			(select count(*) from source_processing_jobs where notebook_id=$1),
			(select count(*) from research_source_imports where run_id=$2)
	`, notebookID, claimed.RunID).Scan(&sources, &jobsCount, &relations); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || jobsCount != 1 || relations != 3 {
		t.Fatalf("counts=%d/%d/%d", sources, jobsCount, relations)
	}
}

func TestResearchPDFImportConcurrentEquivalentActionsConvergeOnOneSource(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, notebookID := admitResearchExecutionForSourceImport(t, api, "research-pdf-concurrent@example.com")
	pdf := researchNativePDF("Concurrent imports must converge.")
	acquirer := &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://mirror-a.example.com/paper.pdf": {MediaType: webreader.MediaTypePDF, FinalURL: "https://mirror-a.example.com/paper.pdf", PDF: pdf},
		"https://mirror-b.example.com/paper.pdf": {MediaType: webreader.MediaTypePDF, FinalURL: "https://mirror-b.example.com/paper.pdf", PDF: pdf},
	}}
	service := researchsource.NewService(api.db.Pool(), acquirer, objectstore.NewMemoryStore())
	results := make([]agent.ResearchSourceImportResult, 2)
	errorsByIndex := make([]error, 2)
	var group sync.WaitGroup
	for index, url := range []string{"https://mirror-a.example.com/paper.pdf", "https://mirror-b.example.com/paper.pdf"} {
		group.Add(1)
		go func(index int, url string) {
			defer group.Done()
			results[index], errorsByIndex[index] = service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
				URL: url, ActionID: "decision:3/action:" + string(rune('0'+index)), Attempt: attemptFromClaim(claimed),
			})
		}(index, url)
	}
	group.Wait()
	if errorsByIndex[0] != nil || errorsByIndex[1] != nil || results[0].SourceID == "" || results[0].SourceID != results[1].SourceID {
		t.Fatalf("results=%+v errors=%v", results, errorsByIndex)
	}
	var sources, jobsCount, relations int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select
			(select count(*) from source_sources where notebook_id=$1),
			(select count(*) from source_processing_jobs where notebook_id=$1),
			(select count(*) from research_source_imports where run_id=$2)
	`, notebookID, claimed.RunID).Scan(&sources, &jobsCount, &relations); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || jobsCount != 1 || relations != 2 {
		t.Fatalf("counts=%d/%d/%d", sources, jobsCount, relations)
	}
}

func TestResearchPDFImportRejectsCorruptPermanentObjectBeforeCreatingSourceRows(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, notebookID := admitResearchExecutionForSourceImport(t, api, "research-pdf-integrity@example.com")
	service := researchsource.NewService(api.db.Pool(), &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/corrupt.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/corrupt.pdf", PDF: researchNativePDF("Integrity must be verified."),
		},
	}}, corruptReadObjectStore{Store: objectstore.NewMemoryStore()})
	_, err := service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
		URL: "https://example.com/corrupt.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(claimed),
	})
	if !errors.Is(err, agent.ErrResearchSourceImportUnavailable) {
		t.Fatalf("import error=%v", err)
	}
	var sources, jobsCount, relations int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select
			(select count(*) from source_sources where notebook_id=$1),
			(select count(*) from source_processing_jobs where notebook_id=$1),
			(select count(*) from research_source_imports where run_id=$2)
	`, notebookID, claimed.RunID).Scan(&sources, &jobsCount, &relations); err != nil {
		t.Fatal(err)
	}
	if sources != 0 || jobsCount != 0 || relations != 0 {
		t.Fatalf("corrupt object created rows=%d/%d/%d", sources, jobsCount, relations)
	}
}

func TestResearchPDFImportReplayNeverRecreatesDeletedSourceAuthority(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, notebookID := admitResearchExecutionForSourceImport(t, api, "research-pdf-deleted-replay@example.com")
	acquirer := &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/deleted.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/deleted.pdf",
			PDF: researchNativePDF("Deleted Source authority must stay deleted."),
		},
	}}
	service := researchsource.NewService(api.db.Pool(), acquirer, objectstore.NewMemoryStore())
	request := agent.ResearchSourceImportRequest{
		URL: "https://example.com/deleted.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(claimed),
	}
	imported, err := service.ImportResearchPDF(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `delete from source_processing_jobs where id=$1`, imported.ProcessingJobID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `delete from source_sources where id=$1`, imported.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportResearchPDF(context.Background(), request); !errors.Is(err, agent.ErrResearchSourceImportUnavailable) {
		t.Fatalf("deleted replay error=%v", err)
	}
	var sources, relations int
	var sourceID, jobID *string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select
			(select count(*) from source_sources where notebook_id=$1),
			count(*),max(source_id),max(processing_job_id)
		from research_source_imports where run_id=$2
	`, notebookID, claimed.RunID).Scan(&sources, &relations, &sourceID, &jobID); err != nil {
		t.Fatal(err)
	}
	if sources != 0 || relations != 1 || sourceID != nil || jobID != nil || acquirer.callCount() != 1 {
		t.Fatalf("deleted replay sources=%d relations=%d source=%v job=%v calls=%d", sources, relations, sourceID, jobID, acquirer.callCount())
	}
}

func TestResearchPDFImportBarrierReleasesLeaseAndSourceTerminalRequeuesSameRun(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, _ := admitResearchExecutionForSourceImport(t, api, "research-pdf-barrier@example.com")
	pdf := researchNativePDF("Pending PDF evidence.")
	service := researchsource.NewService(api.db.Pool(), &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/pending.pdf": {MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/pending.pdf", PDF: pdf},
	}}, objectstore.NewMemoryStore())
	if _, err := service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
		URL: "https://example.com/pending.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(claimed),
	}); err != nil {
		t.Fatal(err)
	}
	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	var assemble agent.Action
	for _, action := range workspaceActions {
		if action.Definition().Name == "assemble_research_report" {
			assemble = action
		}
	}
	if assemble == nil {
		t.Fatal("assemble_research_report is missing")
	}
	request := agent.ActionRequest{
		ActionID: "decision:2/action:0", Attempt: attemptFromClaim(claimed),
		Definition: agentcatalog.MustParseReference("research.executor@9"),
		Input:      json.RawMessage(`{"title":"Report","section_paths":["sections/report.md"]}`),
	}
	if _, err := assemble.Execute(context.Background(), request); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("assembly error=%v, want lease release", err)
	}
	var runStatus, jobStatus string
	var leaseCleared bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select run.status,job.status,job.lease_token is null
		from agent_runs run join agent_jobs job on job.run_id=run.id where run.id=$1
	`, claimed.RunID).Scan(&runStatus, &jobStatus, &leaseCleared); err != nil {
		t.Fatal(err)
	}
	if runStatus != "running" || jobStatus != "waiting" || !leaseCleared {
		t.Fatalf("after barrier run=%s job=%s lease_cleared=%t", runStatus, jobStatus, leaseCleared)
	}

	sourceQueue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	sourceLease, ok, err := sourceQueue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Source claim=%+v ok=%t err=%v", sourceLease, ok, err)
	}
	if err := sourceQueue.Fail(context.Background(), sourceLease.ID, sourceLease.LeaseToken, "extraction_invalid"); err != nil {
		t.Fatal(err)
	}
	var sourceState, sourceJobStatus string
	var allImportsTerminal, deadlineLive bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select run.status,agent_job.status,source.state,source_job.status,
			coalesce(run.deadline_at,tree.absolute_deadline)>now(),
			not exists(
				select 1 from research_source_imports candidate
				left join source_sources candidate_source on candidate_source.id=candidate.source_id
				left join source_processing_jobs candidate_job on candidate_job.id=candidate.processing_job_id
				where candidate.run_id=run.id and not (
					candidate.source_id is null or candidate_source.state in ('ready','failed') or
					(candidate_source.state='qualifying' and candidate_job.status='succeeded')
				)
			)
		from agent_runs run
		left join agent_trees tree on tree.id=run.tree_id
		join agent_jobs agent_job on agent_job.run_id=run.id
		join research_source_imports imported on imported.run_id=run.id
		join source_sources source on source.id=imported.source_id
		join source_processing_jobs source_job on source_job.id=imported.processing_job_id
		where run.id=$1
	`, claimed.RunID).Scan(&runStatus, &jobStatus, &sourceState, &sourceJobStatus, &deadlineLive, &allImportsTerminal); err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || jobStatus != "queued" {
		t.Fatalf("after Source terminal run=%s job=%s source=%s source_job=%s deadline_live=%t all_terminal=%t", runStatus, jobStatus, sourceState, sourceJobStatus, deadlineLive, allImportsTerminal)
	}
	replayed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok || replayed.RunID != claimed.RunID || replayed.AttemptNo != claimed.AttemptNo+1 {
		t.Fatalf("replayed=%+v ok=%t err=%v", replayed, ok, err)
	}
	request.Attempt = attemptFromClaim(replayed)
	result, err := assemble.Execute(context.Background(), request)
	if err != nil || result.Status != agent.ActionDomainError || result.ErrorCode != "research_file_not_found" {
		t.Fatalf("post-terminal assembly result=%+v err=%v", result, err)
	}
}

func TestResearchPDFImportTerminalBeforeBoundaryForcesOneContextRefresh(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, _ := admitResearchExecutionForSourceImport(t, api, "research-pdf-early-terminal@example.com")
	service := researchsource.NewService(api.db.Pool(), &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/early-failure.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/early-failure.pdf", PDF: researchNativePDF("Early terminal Source."),
		},
	}}, objectstore.NewMemoryStore())
	if _, err := service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
		URL: "https://example.com/early-failure.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(claimed),
	}); err != nil {
		t.Fatal(err)
	}
	sourceQueue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	lease, ok, err := sourceQueue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Source claim=%+v ok=%t err=%v", lease, ok, err)
	}
	if err := sourceQueue.Fail(context.Background(), lease.ID, lease.LeaseToken, "extraction_invalid"); err != nil {
		t.Fatal(err)
	}
	var runStatus, jobStatus string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select run.status,job.status from agent_runs run join agent_jobs job on job.run_id=run.id where run.id=$1
	`, claimed.RunID).Scan(&runStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "running" || jobStatus != "running" {
		t.Fatalf("early Source terminal interrupted useful work run=%s job=%s", runStatus, jobStatus)
	}
	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range workspaceActions {
		if action.Definition().Name != "assemble_research_report" {
			continue
		}
		_, err = action.Execute(context.Background(), agent.ActionRequest{
			ActionID: "decision:2/action:0", Attempt: attemptFromClaim(claimed),
			Definition: agentcatalog.MustParseReference("research.executor@9"),
			Input:      json.RawMessage(`{"title":"Report","section_paths":["sections/report.md"]}`),
		})
	}
	if !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("assembly error=%v, want immediate context refresh", err)
	}
	var observedAttempt *int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select run.status,job.status,imported.barrier_observed_attempt_no
		from agent_runs run join agent_jobs job on job.run_id=run.id
		join research_source_imports imported on imported.run_id=run.id where run.id=$1
	`, claimed.RunID).Scan(&runStatus, &jobStatus, &observedAttempt); err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || jobStatus != "queued" || observedAttempt == nil || *observedAttempt != claimed.AttemptNo {
		t.Fatalf("refresh run=%s job=%s observed=%v", runStatus, jobStatus, observedAttempt)
	}
}

func TestResearchMultiPDFBarrierWaitsForEveryImportedSource(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, _ := admitResearchExecutionForSourceImport(t, api, "research-multi-pdf-barrier@example.com")
	acquirer := &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/one.pdf": {MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/one.pdf", PDF: researchNativePDF("First paper.")},
		"https://example.com/two.pdf": {MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/two.pdf", PDF: researchNativePDF("Second paper.")},
	}}
	service := researchsource.NewService(api.db.Pool(), acquirer, objectstore.NewMemoryStore())
	for index, url := range []string{"https://example.com/one.pdf", "https://example.com/two.pdf"} {
		if _, err := service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
			URL: url, ActionID: "decision:1/action:" + string(rune('0'+index)), Attempt: attemptFromClaim(claimed),
		}); err != nil {
			t.Fatal(err)
		}
	}
	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range workspaceActions {
		if action.Definition().Name == "assemble_research_report" {
			_, err = action.Execute(context.Background(), agent.ActionRequest{
				ActionID: "decision:2/action:0", Attempt: attemptFromClaim(claimed),
				Definition: agentcatalog.MustParseReference("research.executor@9"),
				Input:      json.RawMessage(`{"title":"Report","section_paths":["sections/report.md"]}`),
			})
		}
	}
	if !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("assembly error=%v", err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	for index := 0; index < 2; index++ {
		lease, ok, err := queue.Claim(context.Background())
		if err != nil || !ok {
			t.Fatalf("claim %d=%+v ok=%t err=%v", index, lease, ok, err)
		}
		if err := queue.Fail(context.Background(), lease.ID, lease.LeaseToken, "extraction_invalid"); err != nil {
			t.Fatal(err)
		}
		var runStatus, jobStatus string
		if err := api.db.Pool().QueryRow(context.Background(), `
			select run.status,job.status from agent_runs run join agent_jobs job on job.run_id=run.id where run.id=$1
		`, claimed.RunID).Scan(&runStatus, &jobStatus); err != nil {
			t.Fatal(err)
		}
		want := "waiting"
		if index == 1 {
			want = "queued"
		}
		if jobStatus != want || (runStatus != "running" && runStatus != "queued") {
			t.Fatalf("after terminal %d run=%s job=%s want_job=%s", index+1, runStatus, jobStatus, want)
		}
	}
}

func TestResearchMultiPDFLiveFlowWaitsRetrievesAndPublishesWithoutPreRetrievalBody(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, notebookID := admitResearchExecutionForSourceImport(t, api, "research-multi-pdf-live@example.com")
	installReadyEvidenceSetFixture(t, api, notebookID, "", "", "", "")
	ctx := context.Background()
	attempt := attemptFromClaim(claimed)
	objects := objectstore.NewMemoryStore()
	service := researchsource.NewService(api.db.Pool(), &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/alpha.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/alpha.pdf",
			PDF: researchNativePDF("Alpha private body must not appear before retrieval."),
		},
		"https://example.com/beta.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/beta.pdf",
			PDF: researchNativePDF("Beta private body must not appear before retrieval."),
		},
	}}, objects)
	prompts, err := promptcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), prompts, objects)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	save := agent.NewSaveURLAsSourceAction(service)
	saveInputs := []json.RawMessage{
		json.RawMessage(`{"url":"https://example.com/alpha.pdf"}`),
		json.RawMessage(`{"url":"https://example.com/beta.pdf"}`),
	}
	appendResearchProposal(t, runtime, attempt, 1, []models.ActionProposal{
		{Name: "save_url_as_source", Input: saveInputs[0]},
		{Name: "save_url_as_source", Input: saveInputs[1]},
	})
	importedSources := make(map[string]string)
	for index, input := range saveInputs {
		actionID := "decision:1/action:" + string(rune('0'+index))
		result, err := save.Execute(ctx, agent.ActionRequest{
			ActionID: actionID, Attempt: attempt, Definition: agentcatalog.MustParseReference("research.executor@9"), Input: input,
		})
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("save %d result=%+v err=%v", index, result, err)
		}
		var output struct {
			SourceID string `json:"source_id"`
			FinalURL string `json:"final_url"`
		}
		if json.Unmarshal(result.Output, &output) != nil || output.SourceID == "" || output.FinalURL == "" {
			t.Fatalf("save output=%s", result.Output)
		}
		importedSources[output.SourceID] = output.FinalURL
		appendResearchResult(t, runtime, attempt, 1, index, result)
	}

	prefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	preRetrieval, err := runtime.BuildDecisionRequest(ctx, execution, prefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	encodedRequest, _ := json.Marshal(preRetrieval)
	if bytes.Contains(encodedRequest, []byte("Alpha private body")) || bytes.Contains(encodedRequest, []byte("Beta private body")) {
		t.Fatalf("pre-retrieval context leaked PDF body: %s", encodedRequest)
	}

	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objects)
	if err != nil {
		t.Fatal(err)
	}
	workspace := actionsByName(workspaceActions)
	initialSection := json.RawMessage(`{"path":"sections/findings.md","content":"## Findings\n\nWaiting for imported PDF evidence while continuing useful synthesis."}`)
	appendResearchProposal(t, runtime, attempt, 2, []models.ActionProposal{{Name: "write_research_file", Input: initialSection}})
	writeResult, err := workspace["write_research_file"].Execute(ctx, agent.ActionRequest{
		ActionID: "decision:2/action:0", Attempt: attempt, Definition: agentcatalog.MustParseReference("research.executor@9"), Input: initialSection,
	})
	if err != nil || writeResult.Status != agent.ActionSucceeded {
		t.Fatalf("initial write=%+v err=%v", writeResult, err)
	}
	appendResearchResult(t, runtime, attempt, 2, 0, writeResult)

	assemblyInput := json.RawMessage(`{"title":"Imported PDF findings","section_paths":["sections/findings.md"]}`)
	appendResearchProposal(t, runtime, attempt, 3, []models.ActionProposal{{Name: "assemble_research_report", Input: assemblyInput}})
	_, err = workspace["assemble_research_report"].Execute(ctx, agent.ActionRequest{
		ActionID: "decision:3/action:0", Attempt: attempt, Definition: agentcatalog.MustParseReference("research.executor@9"), Input: assemblyInput,
	})
	if !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("pending assembly error=%v", err)
	}

	queue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	chunkIDs := make(map[string]string)
	for index := 0; index < 2; index++ {
		lease, ok, err := queue.Claim(ctx)
		if err != nil || !ok {
			t.Fatalf("Source claim %d=%+v ok=%t err=%v", index, lease, ok, err)
		}
		finalURL, ok := importedSources[lease.SourceID]
		if !ok {
			t.Fatalf("unexpected Source lease=%+v", lease)
		}
		label := "alpha"
		page := 3
		text := "Alpha evidence supports the first finding."
		if strings.Contains(finalURL, "beta.pdf") {
			label, page, text = "beta", 9, "Beta evidence supports the second finding."
		}
		chunkIDs[lease.SourceID] = completeResearchPDFEvidence(t, api, queue, lease, "evr_live_"+label, "unit_live_"+label, text, page)
	}
	replayed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || replayed.RunID != claimed.RunID {
		t.Fatalf("replayed=%+v ok=%t err=%v", replayed, ok, err)
	}
	replayAttempt := attemptFromClaim(replayed)
	assemblyResult, err := workspace["assemble_research_report"].Execute(ctx, agent.ActionRequest{
		ActionID: "decision:3/action:0", Attempt: replayAttempt, Definition: agentcatalog.MustParseReference("research.executor@9"), Input: assemblyInput,
	})
	if err != nil || assemblyResult.Status != agent.ActionSucceeded {
		t.Fatalf("replayed assembly=%+v err=%v", assemblyResult, err)
	}
	appendResearchResult(t, runtime, replayAttempt, 3, 0, assemblyResult)

	searchInputs := []json.RawMessage{
		json.RawMessage(`{"query":"alpha finding","purpose":"support the alpha claim"}`),
		json.RawMessage(`{"query":"beta finding","purpose":"support the beta claim"}`),
	}
	appendResearchProposal(t, runtime, replayAttempt, 4, []models.ActionProposal{
		{Name: "search_evidence", Input: searchInputs[0]},
		{Name: "search_evidence", Input: searchInputs[1]},
	})
	index := 0
	for sourceID, finalURL := range importedSources {
		search := agent.NewSearchEvidenceAction(agent.NewEvidenceSearchService(
			api.db.Pool(), &evidenceVectorSearchStub{candidateID: chunkIDs[sourceID]}, &evidenceModelsStub{},
		))
		if strings.Contains(finalURL, "beta.pdf") {
			index = 1
		} else {
			index = 0
		}
		result, err := search.Execute(ctx, agent.ActionRequest{
			ActionID: "decision:4/action:" + string(rune('0'+index)), Attempt: replayAttempt,
			Definition: agentcatalog.MustParseReference("research.executor@9"), Input: searchInputs[index],
		})
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("search %s=%+v err=%v", sourceID, result, err)
		}
		appendResearchResult(t, runtime, replayAttempt, 4, index, result)
	}

	finalSection := json.RawMessage(`{"path":"sections/findings.md","content":"## Findings\n\n[Alpha](https://example.com/alpha.pdf) supports the first finding. [Beta](https://example.com/beta.pdf) supports the second finding."}`)
	appendResearchProposal(t, runtime, replayAttempt, 5, []models.ActionProposal{{Name: "write_research_file", Input: finalSection}})
	writeResult, err = workspace["write_research_file"].Execute(ctx, agent.ActionRequest{
		ActionID: "decision:5/action:0", Attempt: replayAttempt, Definition: agentcatalog.MustParseReference("research.executor@9"), Input: finalSection,
	})
	if err != nil || writeResult.Status != agent.ActionSucceeded {
		t.Fatalf("final write=%+v err=%v", writeResult, err)
	}
	appendResearchResult(t, runtime, replayAttempt, 5, 0, writeResult)
	appendResearchProposal(t, runtime, replayAttempt, 6, []models.ActionProposal{{Name: "assemble_research_report", Input: assemblyInput}})
	assemblyResult, err = workspace["assemble_research_report"].Execute(ctx, agent.ActionRequest{
		ActionID: "decision:6/action:0", Attempt: replayAttempt, Definition: agentcatalog.MustParseReference("research.executor@9"), Input: assemblyInput,
	})
	if err != nil || assemblyResult.Status != agent.ActionSucceeded {
		t.Fatalf("final assembly=%+v err=%v", assemblyResult, err)
	}
	appendResearchResult(t, runtime, replayAttempt, 6, 0, assemblyResult)

	execution, err = runtime.Load(ctx, replayAttempt)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err = runtime.LoadCheckpointPrefix(ctx, replayAttempt)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runtime.PrepareFinal(ctx, replayAttempt, execution, prefix, models.FinalDraft{Text: "Research complete."})
	if err != nil || !strings.Contains(prepared.Text, "https://example.com/alpha.pdf") || !strings.Contains(prepared.Text, "https://example.com/beta.pdf") {
		t.Fatalf("prepared report=%q err=%v", prepared.Text, err)
	}
	finalCheckpoint, err := agent.NewFinalDraftCheckpoint(7, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, replayAttempt, finalCheckpoint); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishFinal(ctx, replayAttempt, prepared); err != nil {
		t.Fatal(err)
	}
	var runStatus, jobStatus, sessionStatus, report string
	if err := api.db.Pool().QueryRow(ctx, `
		select run.status,job.status,session.status,report.content_markdown
		from agent_runs run
		join agent_jobs job on job.run_id=run.id
		join research_sessions session on session.execution_run_id=run.id
		join research_report_versions report on report.session_id=session.id and report.version=session.current_report_version
		where run.id=$1
	`, claimed.RunID).Scan(&runStatus, &jobStatus, &sessionStatus, &report); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || jobStatus != "succeeded" || sessionStatus != "completed" ||
		!strings.Contains(report, "https://example.com/alpha.pdf") || !strings.Contains(report, "https://example.com/beta.pdf") {
		t.Fatalf("publication=%s/%s/%s report=%q", runStatus, jobStatus, sessionStatus, report)
	}
}

func appendResearchProposal(t *testing.T, runtime *agent.ResearchRuntime, attempt agent.Attempt, decision int, actions []models.ActionProposal) {
	t.Helper()
	checkpoint, err := agent.NewProposalCheckpoint(decision, models.ActionProposalBatch{Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(context.Background(), attempt, checkpoint); err != nil {
		t.Fatal(err)
	}
}

func appendResearchResult(t *testing.T, runtime *agent.ResearchRuntime, attempt agent.Attempt, decision, index int, result agent.ActionResult) {
	t.Helper()
	actionID := "decision:" + strconv.Itoa(decision) + "/action:" + strconv.Itoa(index)
	checkpoint, err := agent.NewActionResultCheckpoint(decision, index, actionID, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(context.Background(), attempt, checkpoint); err != nil {
		t.Fatal(err)
	}
}

func actionsByName(actions []agent.Action) map[string]agent.Action {
	result := make(map[string]agent.Action, len(actions))
	for _, action := range actions {
		result[action.Definition().Name] = action
	}
	return result
}

func completeResearchPDFEvidence(
	t *testing.T,
	api *testAPI,
	queue *sourcejobs.Queue,
	lease sourcejobs.Lease,
	revisionID, unitID, text string,
	page int,
) string {
	t.Helper()
	ctx := context.Background()
	for _, transition := range [][2]source.State{
		{source.StateUploaded, source.StateValidating},
		{source.StateValidating, source.StateNormalizing},
		{source.StateNormalizing, source.StateSegmenting},
		{source.StateSegmenting, source.StateQualifying},
		{source.StateQualifying, source.StateIndexing},
		{source.StateIndexing, source.StateVerifying},
	} {
		if err := queue.Advance(ctx, lease.ID, lease.LeaseToken, transition[0], transition[1]); err != nil {
			t.Fatalf("advance %s -> %s: %v", transition[0], transition[1], err)
		}
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into source_evidence_revisions(
			id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,
			artifact_object_key,artifact_sha256,status
		) values($1,$2,$3,1,'extract-pdf-v1','nano.normalized-source.v1',$4,$5,'building')
	`, revisionID, lease.SourceID, lease.NotebookID, "sources/"+lease.SourceID+"/evidence/"+revisionID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	coordinate, _ := json.Marshal(map[string]any{"kind": "pdf_region", "page": page, "x": 72, "y": 700, "width": 180, "height": 14})
	if _, err := api.db.Pool().Exec(ctx, `
		insert into source_evidence_units(
			id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune,coordinate_json
		) values($1,$2,$3,$4,0,'paragraph',$5,0,char_length($5),$6::jsonb)
	`, unitID, revisionID, lease.SourceID, lease.NotebookID, text, string(coordinate)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into retrieval_source_index_builds(
			revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at
		) values($1,'riv_pin_active',$2,$3,1,$4,'verified',now())
	`, revisionID, lease.SourceID, lease.NotebookID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if err := queue.CompleteEvidence(ctx, lease.ID, lease.LeaseToken, revisionID); err != nil {
		t.Fatal(err)
	}
	chunks, err := retrieval.BuildChunks("riv_pin_active", revisionID, []retrieval.Unit{{
		ID: unitID, Ordinal: 0, Kind: "paragraph", Text: text,
	}}, retrieval.ChunkConfig{MaxRunes: 512, OverlapRunes: 64, PreserveHeadingContext: true})
	if err != nil || len(chunks) != 1 {
		t.Fatalf("chunks=%+v err=%v", chunks, err)
	}
	return chunks[0].ID
}

func TestResearchPDFImportReadyExtendsRunScopeAndReturnsPageAwareEvidence(t *testing.T) {
	api := newTestAPI(t)
	claimed, _, _, notebookID := admitResearchExecutionForRelease(t, api, "research-pdf-ready@example.com", "nano.default@17")
	installReadyEvidenceSetFixture(t, api, notebookID, "", "", "", "")
	const unitText = "The authoritative launch date is 20 July."
	pageTexts := []string{"Orientation page one.", "Prior work.", "Methods.", "Results.", "Discussion.", "Limitations.", unitText}
	pdfPayload := evidenceTestPDFWithOutlineAtPage(pageTexts, "Abstract", 7)
	objects := objectstore.NewMemoryStore()
	service := researchsource.NewService(api.db.Pool(), &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/ready.pdf": {
			MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/ready.pdf",
			PDF: pdfPayload,
		},
	}}, objects)
	imported, err := service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
		URL: "https://example.com/ready.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(claimed),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewSourceInspectionService(api.db.Pool(), objects).
		InspectSource(context.Background(), attemptFromClaim(claimed), imported.SourceID); !errors.Is(err, agent.ErrSourceNotInspectable) {
		t.Fatalf("processing Source inspection err=%v", err)
	}
	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range workspaceActions {
		if action.Definition().Name != "assemble_research_report" {
			continue
		}
		_, err = action.Execute(context.Background(), agent.ActionRequest{
			ActionID: "decision:2/action:0", Attempt: attemptFromClaim(claimed),
			Definition: agentcatalog.MustParseReference("research.executor@10"),
			Input:      json.RawMessage(`{"title":"Report","section_paths":["sections/report.md"]}`),
		})
	}
	if !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("assembly error=%v, want Source wait", err)
	}

	sourceQueue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	lease, ok, err := sourceQueue.Claim(context.Background())
	if err != nil || !ok || lease.SourceID != imported.SourceID {
		t.Fatalf("Source claim=%+v ok=%t err=%v", lease, ok, err)
	}
	transitions := [][2]source.State{
		{source.StateUploaded, source.StateValidating},
		{source.StateValidating, source.StateNormalizing},
		{source.StateNormalizing, source.StateSegmenting},
		{source.StateSegmenting, source.StateQualifying},
		{source.StateQualifying, source.StateIndexing},
		{source.StateIndexing, source.StateVerifying},
	}
	for _, transition := range transitions {
		if err := sourceQueue.Advance(context.Background(), lease.ID, lease.LeaseToken, transition[0], transition[1]); err != nil {
			t.Fatalf("advance %s -> %s: %v", transition[0], transition[1], err)
		}
	}
	const revisionID = "evr_research_pdf_ready"
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_revisions(
			id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,
			artifact_object_key,artifact_sha256,status
		) values($1,$2,$3,1,'extract-pdf-v1','nano.normalized-source.v1',$4,$5,'building')
	`, revisionID, imported.SourceID, notebookID, "sources/"+imported.SourceID+"/evidence/"+revisionID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	originalDigest := sha256.Sum256(pdfPayload)
	parserPages := make([]sourcemap.Page, 7)
	for index := range parserPages {
		parserPages[index] = sourcemap.Page{Ordinal: index + 1, Width: 612, Height: 792, Blocks: []sourcemap.Block{{
			ReadingOrder: 0, Kind: "paragraph", Text: "Parser orientation page.",
			BBox: sourcemap.BBox{X0: 72, Y0: 72, X1: 300, Y1: 100},
		}}}
	}
	var mapParser sourcemap.Adapter = sourceMapParserStub{result: sourcemap.ParseResult{
		Document: sourcemap.Document{
			SchemaVersion: 1, SourceID: imported.SourceID, InputSHA256: hex.EncodeToString(originalDigest[:]),
			ParserIdentity: sourcemap.ParserIdentity, ParserVersion: "1.28.2", ParserPolicyID: sourcemap.ParserPolicyNoOCR,
			PageCount: 7, Outline: []sourcemap.OutlineEntry{{Level: 1, Title: "Abstract", Page: 7}}, Pages: parserPages,
		}, CanonicalJSON: []byte(`{"parser":"fixture"}`), SHA256: strings.Repeat("e", 64),
	}}
	if parserURL, parserToken := strings.TrimSpace(os.Getenv("NANO_TEST_SOURCE_MAP_PARSER_URL")), strings.TrimSpace(os.Getenv("NANO_TEST_SOURCE_MAP_PARSER_TOKEN")); parserURL != "" && parserToken != "" {
		mapParser, err = sourcemap.NewHTTPAdapter(sourcemap.HTTPConfig{
			Endpoint: parserURL, ServiceToken: parserToken, HTTPClient: &http.Client{Timeout: 30 * time.Second},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	mapMaterializer, err := sourcemap.NewMaterializer(mapParser, sourcemap.NewPostgresRepository(api.db.Pool()), objects, sourcemap.Config{MaxPages: 500, MaxOutputBytes: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapMaterializer.Materialize(context.Background(), sourcemap.MaterializeRequest{
		SourceID: imported.SourceID, NotebookID: notebookID, RevisionID: revisionID,
		OriginalSHA256: hex.EncodeToString(originalDigest[:]), Payload: pdfPayload,
		Normalized: normalize.Artifact{SourceID: imported.SourceID, Format: "pdf"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_units(
			id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune,coordinate_json
		) values('unit_research_pdf_ready',$1,$2,$3,0,'paragraph',$4,0,char_length($4),
			'{"kind":"pdf_region","page":7,"x":72,"y":700,"width":180,"height":14}'::jsonb)
	`, revisionID, imported.SourceID, notebookID, unitText); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into retrieval_source_index_builds(
			revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at
		) values($1,'riv_pin_active',$2,$3,1,$4,'verified',now())
	`, revisionID, imported.SourceID, notebookID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err := sourceQueue.CompleteEvidence(context.Background(), lease.ID, lease.LeaseToken, revisionID); err != nil {
		t.Fatal(err)
	}
	var pinnedSource, pinnedRevision, pinnedVersion, runStatus, jobStatus string
	var selectedCount int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select evidence.source_id,evidence.evidence_revision_id,evidence.index_version_id,
			run.selected_source_count,run.status,job.status
		from agent_run_evidence_set evidence
		join agent_runs run on run.id=evidence.run_id
		join agent_jobs job on job.run_id=run.id
		where evidence.run_id=$1
	`, claimed.RunID).Scan(&pinnedSource, &pinnedRevision, &pinnedVersion, &selectedCount, &runStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if pinnedSource != imported.SourceID || pinnedRevision != revisionID || pinnedVersion != "riv_pin_active" ||
		selectedCount != 1 || runStatus != "queued" || jobStatus != "queued" {
		t.Fatalf("scope=%s/%s/%s selected=%d run=%s job=%s", pinnedSource, pinnedRevision, pinnedVersion, selectedCount, runStatus, jobStatus)
	}
	replayed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok || replayed.RunID != claimed.RunID {
		t.Fatalf("replayed=%+v ok=%t err=%v", replayed, ok, err)
	}
	inspection, err := agent.NewSourceInspectionService(api.db.Pool(), objects).
		InspectSource(context.Background(), attemptFromClaim(replayed), imported.SourceID)
	if err != nil || len(inspection) > agent.InspectSourceMaxResultBytes ||
		!strings.Contains(string(inspection), unitText) || !strings.Contains(string(inspection), `"evidence_eligibility":"navigation_only_use_search_evidence_for_claims"`) ||
		strings.Contains(string(inspection), "Parser orientation page") {
		t.Fatalf("inspection=%s err=%v", inspection, err)
	}
	var inspected struct {
		Entries []struct {
			EntryID string `json:"entry_id"`
			Heading string `json:"heading"`
		} `json:"entries"`
	}
	if json.Unmarshal(inspection, &inspected) != nil || len(inspected.Entries) != 1 || inspected.Entries[0].EntryID == "" {
		t.Fatalf("inspection navigation=%s", inspection)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_sources(
			id,notebook_id,input_kind,format,title,media_type,byte_size,content_sha256,original_object_key,state
		) values(
			'src_ready_not_pinned',$1,'file','pdf','Unselected Ready PDF','application/pdf',4,$2,
			'sources/src_ready_not_pinned/original','ready'
		)
	`, notebookID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_revisions(
			id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,
			artifact_object_key,artifact_sha256,status,activated_at
		) values(
			'evr_ready_not_pinned','src_ready_not_pinned',$1,1,'extract-pdf-v1','nano.normalized-source.v1',
			'sources/src_ready_not_pinned/evidence/evr_ready_not_pinned',$2,'active',now()
		)
	`, notebookID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into retrieval_source_index_builds(
			revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at
		) values(
			'evr_ready_not_pinned','riv_pin_active','src_ready_not_pinned',$1,1,$2,'verified',now()
		)
	`, notebookID, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewSourceInspectionService(api.db.Pool(), objects).
		InspectSource(context.Background(), attemptFromClaim(replayed), "src_ready_not_pinned"); !errors.Is(err, agent.ErrSourceNotInspectable) {
		t.Fatalf("unselected Source inspection err=%v", err)
	}
	searchQuery := "launch date"
	if os.Getenv("NANO_QWEN_INSPECT_SOURCE_SMOKE") == "1" {
		temperature := 0.0
		bifrostURL := strings.TrimSpace(os.Getenv("NANO_BIFROST_URL"))
		if bifrostURL == "" {
			bifrostURL = "http://127.0.0.1:56666"
		}
		model := models.NewBifrostClient(bifrostURL, &http.Client{Timeout: 2 * time.Minute}, 1024)
		outcome, modelErr := model.Decide(context.Background(), models.ModelRequest{
			Model: "aliyun/qwen-plus",
			Messages: []models.ModelMessage{
				{Role: models.RoleSystem, Content: "You are executing a Research plan. The user message is a navigation-only inspect_source result. Form one focused evidence question about a concrete claim visible in its representative preview, then call search_evidence exactly once. Do not treat inspection as citable evidence."},
				{Role: models.RoleUser, Content: string(inspection)},
			},
			ActionDefinitions: []models.ActionDefinition{{
				Name: "search_evidence", Description: "Retrieve citable original Evidence Units for one focused Research question.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":2048},"purpose":{"type":"string","minLength":1,"maxLength":1024}},"required":["query","purpose"],"additionalProperties":false}`),
			}},
			RequiredActionName: "search_evidence",
			InvocationPolicy:   models.ModelInvocationPolicy{Temperature: &temperature, MaxOutputTokens: 1024, Timeout: 2 * time.Minute, EnableThinking: true},
		})
		if modelErr != nil || outcome.ModelDecision.Proposal == nil || len(outcome.ModelDecision.Proposal.Actions) != 1 || outcome.ModelDecision.Proposal.Actions[0].Name != "search_evidence" {
			t.Fatalf("live inspection question outcome=%+v err=%v", outcome, modelErr)
		}
		var focused struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}
		if err := json.Unmarshal(outcome.ModelDecision.Proposal.Actions[0].Input, &focused); err != nil {
			t.Fatal(err)
		}
		focused.Query, focused.Purpose = strings.TrimSpace(focused.Query), strings.TrimSpace(focused.Purpose)
		semanticQuery := strings.ToLower(focused.Query)
		if focused.Query == "" || focused.Purpose == "" ||
			!(strings.Contains(semanticQuery, "launch") || strings.Contains(semanticQuery, "date") || strings.Contains(semanticQuery, "july") || strings.Contains(semanticQuery, "20") || strings.Contains(semanticQuery, "日期") || strings.Contains(semanticQuery, "7月")) {
			t.Fatalf("live model did not form a focused question from inspection: %+v", focused)
		}
		searchQuery = focused.Query
	}
	chunks, err := retrieval.BuildChunks("riv_pin_active", revisionID, []retrieval.Unit{{
		ID: "unit_research_pdf_ready", Ordinal: 0, Kind: "paragraph", Text: unitText,
	}}, retrieval.ChunkConfig{MaxRunes: 512, OverlapRunes: 64, PreserveHeadingContext: true})
	if err != nil {
		t.Fatal(err)
	}
	vectors := &evidenceVectorSearchStub{candidateID: chunks[0].ID}
	searchService := agent.NewEvidenceSearchService(api.db.Pool(), vectors, &evidenceModelsStub{}).WithSourceMapObjects(objects)
	for _, unavailable := range []agent.EvidenceSearchRequest{
		{Query: searchQuery, Purpose: "hide unpinned Source", SourceID: "src_ready_not_pinned"},
		{Query: searchQuery, Purpose: "hide missing entry", SourceID: imported.SourceID, EntryID: "entry_missing"},
	} {
		if _, err := searchService.SearchEvidenceScoped(context.Background(), attemptFromClaim(replayed), unavailable); !errors.Is(err, agent.ErrEvidenceScopeUnavailable) {
			t.Fatalf("unavailable scope %+v error=%v", unavailable, err)
		}
	}
	result, err := searchService.SearchEvidenceScoped(context.Background(), attemptFromClaim(replayed), agent.EvidenceSearchRequest{
		Query: searchQuery, Purpose: "cite the imported PDF", SourceID: imported.SourceID, EntryID: inspected.Entries[0].EntryID,
	})
	if err != nil || len(result.Candidates) != 1 || result.Scope == nil || result.Scope.SourceID != imported.SourceID ||
		result.Scope.EntryID != inspected.Entries[0].EntryID || result.Scope.PageStart != 7 || result.Scope.PageEnd != 7 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	candidate := result.Candidates[0]
	if candidate.SourceID != imported.SourceID || candidate.RevisionID != revisionID || candidate.ID != chunks[0].ID ||
		candidate.Preview != unitText || len(candidate.Coordinates) != 1 || candidate.Coordinates[0].Page != 7 ||
		candidate.Coordinates[0].Kind != "pdf_region" {
		t.Fatalf("candidate=%+v", candidate)
	}
	for _, scope := range vectors.scopes {
		if !slices.Contains(scope.AllowedChunkIDs, chunks[0].ID) {
			t.Fatalf("entry chunk filter missing from Qdrant scope: %+v", scope)
		}
	}
	prompts, err := promptcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), prompts, objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(context.Background(), attemptFromClaim(replayed))
	if err != nil || execution.SelectedSourceCount != 1 {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	request, err := runtime.BuildDecisionRequest(context.Background(), execution, agent.CheckpointPrefix{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, imported.SourceID+": ready, searchable") ||
		strings.Contains(request.Messages[0].Content, unitText) {
		t.Fatalf("Research state projection=%s", request.Messages[0].Content)
	}
	reusedReady, err := service.ImportResearchPDF(context.Background(), agent.ResearchSourceImportRequest{
		URL: "https://example.com/ready.pdf", ActionID: "decision:3/action:0", Attempt: attemptFromClaim(replayed),
	})
	if err != nil || !reusedReady.Reused || reusedReady.SourceID != imported.SourceID || reusedReady.State != "ready" || !reusedReady.Searchable {
		t.Fatalf("reused Ready Source=%+v err=%v", reusedReady, err)
	}
}

func admitResearchExecutionForSourceImport(t *testing.T, api *testAPI, email string) (jobs.ClaimedJob, string, string, string) {
	return admitResearchExecutionForRelease(t, api, email, "nano.default@16")
}

func admitResearchExecutionForRelease(t *testing.T, api *testAPI, email, release string) (jobs.ClaimedJob, string, string, string) {
	t.Helper()
	catalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	api.server = app.NewServer(app.Config{
		CookieSecure: false, AgentCatalog: catalog, AgentRelease: agentcatalog.MustParseReference(release),
	}, api.db)
	api.handler = api.server.Handler()
	sessionCookie, csrfCookie := api.registerWithCSRF(t, email)
	notebookID, chatID := createNotebookAndChatForEvidenceSet(t, api, sessionCookie, csrfCookie)
	admission := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788d701", "content": "Research permanent PDF Sources.", "mode": "research",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admission.Code != http.StatusAccepted {
		t.Fatalf("admission status=%d body=%s", admission.Code, admission.Body.String())
	}
	var admitted struct {
		ResearchSessionID string `json:"research_session_id"`
		RunID             string `json:"run_id"`
	}
	decodeBody(t, admission, &admitted)
	planJSON, _ := json.Marshal(researchPlanFixture("PDF Source plan"))
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `insert into research_plan_versions(session_id,version,plan_json,producer_run_id,created_by) values($1,1,$2::jsonb,$3,'model')`, admitted.ResearchSessionID, string(planJSON), admitted.RunID); err != nil {
		t.Fatal(err)
	}
	for _, update := range []struct {
		query string
		id    string
	}{
		{query: `update research_sessions set status='awaiting_confirmation' where id=$1`, id: admitted.ResearchSessionID},
		{query: `update agent_runs set status='completed',finished_at=now() where id=$1`, id: admitted.RunID},
		{query: `update agent_jobs set status='succeeded',finished_at=now() where run_id=$1`, id: admitted.RunID},
	} {
		if _, err := api.db.Pool().Exec(ctx, update.query, update.id); err != nil {
			t.Fatal(err)
		}
	}
	started := api.postJSONWithCookieAndCSRF(t, "/api/v1/research-sessions/"+admitted.ResearchSessionID+"/start", map[string]any{
		"plan_version": 1,
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if started.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}
	var startBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, started, &startBody)
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || claimed.RunID != startBody.RunID {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	return claimed, admitted.ResearchSessionID, chatID, notebookID
}
