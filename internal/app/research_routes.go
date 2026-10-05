package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/jackc/pgx/v5"
)

var errResearchStateConflict = errors.New("Research Session state conflict")

type researchSessionSummary struct {
	ID                  string  `json:"id"`
	InputMessageID      string  `json:"input_message_id"`
	Status              string  `json:"status"`
	PlanningRunID       *string `json:"planning_run_id,omitempty"`
	AcceptedPlanVersion *int    `json:"accepted_plan_version,omitempty"`
	ExecutionRunID      *string `json:"execution_run_id,omitempty"`
	CurrentReport       *int    `json:"current_report_version,omitempty"`
	ErrorCode           *string `json:"error_code,omitempty"`
}

func listResearchSessionSummaries(r *http.Request, tx pgx.Tx, userID, chatID string) ([]researchSessionSummary, error) {
	rows, err := tx.Query(r.Context(), `
		select id,input_message_id,status,planning_run_id,accepted_plan_version,execution_run_id,current_report_version,error_code
		from research_sessions where user_id=$1 and chat_id=$2 order by created_at,id
	`, userID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]researchSessionSummary, 0)
	for rows.Next() {
		var session researchSessionSummary
		if err := rows.Scan(&session.ID, &session.InputMessageID, &session.Status, &session.PlanningRunID,
			&session.AcceptedPlanVersion, &session.ExecutionRunID, &session.CurrentReport, &session.ErrorCode); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Server) researchSessionByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/research-sessions/"), "/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
		s.researchSessionSnapshot(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "plan" && r.Method == http.MethodPatch {
		s.editResearchPlan(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "start" && r.Method == http.MethodPost {
		s.startResearch(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "answers" && r.Method == http.MethodPost {
		s.answerResearchQuestions(w, r, user.ID, parts[0])
		return
	}
	writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
}

func (s *Server) researchSessionSnapshot(w http.ResponseWriter, r *http.Request, userID, sessionID string) {
	var session struct {
		ID                  string  `json:"id"`
		ChatID              string  `json:"chat_id"`
		InputMessageID      string  `json:"input_message_id"`
		Status              string  `json:"status"`
		PlanningRunID       *string `json:"planning_run_id,omitempty"`
		AcceptedPlanVersion *int    `json:"accepted_plan_version,omitempty"`
		ExecutionRunID      *string `json:"execution_run_id,omitempty"`
		CurrentReport       *int    `json:"current_report_version,omitempty"`
		ErrorCode           *string `json:"error_code,omitempty"`
	}
	var planVersion *int
	var plan json.RawMessage
	var reportVersion *int
	var report *string
	var discovered, read, failed int
	var pending *agent.PendingPlanningQuestions
	err := s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			select id,chat_id,input_message_id,status,planning_run_id,accepted_plan_version,
				execution_run_id,current_report_version,error_code
			from research_sessions where id=$1 and user_id=$2
		`, sessionID, userID).Scan(&session.ID, &session.ChatID, &session.InputMessageID, &session.Status,
			&session.PlanningRunID, &session.AcceptedPlanVersion, &session.ExecutionRunID, &session.CurrentReport, &session.ErrorCode); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `select version,plan_json from research_plan_versions where session_id=$1 order by version desc limit 1`, sessionID).Scan(&planVersion, &plan); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(r.Context(), `select version,content_markdown from research_report_versions where session_id=$1 order by version desc limit 1`, sessionID).Scan(&reportVersion, &report); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if session.Status == "awaiting_input" {
			var err error
			if pending, err = agent.LoadPendingPlanningQuestions(r.Context(), tx, sessionID); err != nil {
				return err
			}
		}
		return tx.QueryRow(r.Context(), `
			select count(*) filter(where status='discovered'),count(*) filter(where status='read'),count(*) filter(where status='failed')
			from research_evidence_ledger where session_id=$1
		`, sessionID).Scan(&discovered, &read, &failed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.research_session_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	response := map[string]any{"session": session, "evidence": map[string]int{"discovered": discovered, "read": read, "failed": failed}}
	if planVersion != nil {
		response["plan"] = map[string]any{"version": *planVersion, "content": plan}
	}
	if reportVersion != nil && report != nil {
		response["report"] = map[string]any{"version": *reportVersion, "content_markdown": *report}
	}
	if pending != nil {
		response["pending_questions"] = pending
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) answerResearchQuestions(w http.ResponseWriter, r *http.Request, userID, sessionID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	var request struct {
		ActionID       string                 `json:"action_id"`
		Answers        []agent.PlanningAnswer `json:"answers"`
		UseRecommended bool                   `json:"use_recommended"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.ActionID) == "" || len(request.Answers) > 3 {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.research_answer_invalid")
		return
	}
	err := s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var owned bool
		if err := tx.QueryRow(r.Context(), `select exists(select 1 from research_sessions where id=$1 and user_id=$2)`, sessionID, userID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return pgx.ErrNoRows
		}
		return agent.AnswerPlanningQuestionsInTx(r.Context(), tx, sessionID, request.ActionID, request.Answers, request.UseRecommended)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "not_found", "error.research_session_not_found")
	case errors.Is(err, agent.ErrPlanningAnswerInvalid):
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.research_answer_invalid")
	case errors.Is(err, agent.ErrPlanningQuestionClosed):
		writeError(w, r, http.StatusConflict, "research_state_conflict", "error.research_state_conflict")
	case err != nil:
		slog.ErrorContext(r.Context(), "Research answer failed", "session_id", sessionID, "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"session_id": sessionID, "status": "planning"})
	}
}

func (s *Server) editResearchPlan(w http.ResponseWriter, r *http.Request, userID, sessionID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	var request struct {
		Plan json.RawMessage `json:"plan"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	plan, err := agent.ValidateResearchPlanJSON(string(request.Plan))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "research_plan_invalid", "error.research_plan_invalid")
		return
	}
	version := 0
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(r.Context(), `select status from research_sessions where id=$1 and user_id=$2 for update`, sessionID, userID).Scan(&status); err != nil {
			return err
		}
		if status != "awaiting_confirmation" {
			return errResearchStateConflict
		}
		if err := tx.QueryRow(r.Context(), `select coalesce(max(version),0)+1 from research_plan_versions where session_id=$1`, sessionID).Scan(&version); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `insert into research_plan_versions(session_id,version,plan_json,created_by) values($1,$2,$3::jsonb,'member')`, sessionID, version, string(plan))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.research_session_not_found")
		return
	}
	if errors.Is(err, errResearchStateConflict) {
		writeError(w, r, http.StatusConflict, "research_state_conflict", "error.research_state_conflict")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "version": version, "plan": plan})
}

func (s *Server) startResearch(w http.ResponseWriter, r *http.Request, userID, sessionID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	if s.researchAgent == nil {
		writeError(w, r, http.StatusConflict, "research_mode_unavailable", "error.research_mode_unavailable")
		return
	}
	var request struct {
		PlanVersion int    `json:"plan_version"`
		TimeZone    string `json:"time_zone"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.PlanVersion < 1 {
		writeError(w, r, http.StatusBadRequest, "research_plan_invalid", "error.research_plan_invalid")
		return
	}
	runID, err := newOpaqueID("run")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	jobID, err := newOpaqueID("job")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `select pg_advisory_xact_lock(hashtextextended($1,0))`, "admit_agent_run:"+userID); err != nil {
			return err
		}
		var chatID, inputMessageID, currentStatus string
		var accepted *int
		var existingRun *string
		if err := tx.QueryRow(r.Context(), `select chat_id,input_message_id,status,accepted_plan_version,execution_run_id from research_sessions where id=$1 and user_id=$2 for update`, sessionID, userID).Scan(&chatID, &inputMessageID, &currentStatus, &accepted, &existingRun); err != nil {
			return err
		}
		if currentStatus != "awaiting_confirmation" {
			if existingRun != nil && accepted != nil && *accepted == request.PlanVersion && currentStatus == "queued" {
				runID = *existingRun
				return nil
			}
			return errResearchStateConflict
		}
		var planExists bool
		if err := tx.QueryRow(r.Context(), `select exists(select 1 from research_plan_versions where session_id=$1 and version=$2)`, sessionID, request.PlanVersion).Scan(&planExists); err != nil {
			return err
		}
		if !planExists {
			return errResearchStateConflict
		}
		store := agent.NewStore(tx)
		if _, active, err := store.ActiveByUser(r.Context(), userID); err != nil {
			return err
		} else if active {
			return agent.ErrActiveRun
		}
		deadline := s.cfg.ResearchDeadline
		if deadline <= 0 {
			deadline = 45 * time.Minute
		}
		zone := normalizeBrowserTimeZone(request.TimeZone)
		manifest, err := json.Marshal(map[string]any{"agent_release": s.researchAgent.Release.String(), "time_zone": zone, "mode": "research", "research_session_id": sessionID, "accepted_plan_version": request.PlanVersion})
		if err != nil {
			return err
		}
		if err := store.CreateConfiguredChatQueued(r.Context(), agent.ConfiguredChatAdmission{
			RunID: runID, UserID: userID, ChatID: chatID, InputMessageID: inputMessageID,
			Definition: s.researchAgent.Definition, ModelPolicy: s.researchAgent.Policy, ModelContext: s.researchAgent.Context,
			DeadlineAt: time.Now().Add(deadline), ContextManifest: manifest,
		}); err != nil {
			return err
		}
		if tag, err := tx.Exec(r.Context(), `update research_sessions set status='queued',accepted_plan_version=$2,execution_run_id=$3,updated_at=now() where id=$1 and status='awaiting_confirmation'`, sessionID, request.PlanVersion, runID); err != nil || tag.RowsAffected() != 1 {
			if err != nil {
				return err
			}
			return errResearchStateConflict
		}
		rows, err := tx.Query(r.Context(), `select source_id from chat_source_selections where chat_id=$1 order by source_id`, chatID)
		if err != nil {
			return err
		}
		var sourceIDs []string
		for rows.Next() {
			var sourceID string
			if err := rows.Scan(&sourceID); err != nil {
				rows.Close()
				return err
			}
			sourceIDs = append(sourceIDs, sourceID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if err := store.PinEvidenceSet(r.Context(), runID, userID, sourceIDs); err != nil {
			return err
		}
		if err := jobs.NewStore(tx).CreateAgentRun(r.Context(), jobID, runID); err != nil {
			return err
		}
		if err := agent.StartRunTraceInTx(r.Context(), tx, runID, s.researchAgent.Policy.ProviderModel, s.researchAgent.Definition.Reference().String(), nil); err != nil {
			return err
		}
		if err := store.FinalizeConfiguredChatOwnership(r.Context(), runID); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `select pg_notify('nano_agent_jobs',$1)`, jobID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.research_session_not_found")
		return
	}
	if errors.Is(err, errResearchStateConflict) || errors.Is(err, agent.ErrActiveRun) {
		writeError(w, r, http.StatusConflict, "research_state_conflict", "error.research_state_conflict")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "Research start failed", "session_id", sessionID, "run_id", runID, "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"session_id": sessionID, "run_id": runID, "status": "queued"})
}
