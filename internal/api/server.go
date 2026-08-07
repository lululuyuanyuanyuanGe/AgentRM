package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/controller"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

type Server struct {
	controller *controller.Controller
	logger     *slog.Logger
}

func NewServer(resourceController *controller.Controller, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{controller: resourceController, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/cluster", s.cluster)
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("GET /v1/sessions/{session_id}", s.getSession)
	mux.HandleFunc("PATCH /v1/sessions/{session_id}/state", s.updateState)
	mux.HandleFunc("PUT /v1/sessions/{session_id}/metrics", s.updateMetrics)
	mux.HandleFunc("POST /v1/sessions/{session_id}/resources", s.requestResources)
	mux.HandleFunc("POST /v1/sessions/{session_id}/suspend", s.suspendSession)
	mux.HandleFunc("POST /v1/sessions/{session_id}/resume", s.resumeSession)
	mux.HandleFunc("POST /v1/sessions/{session_id}/finish", s.finishSession)
	mux.HandleFunc("POST /v1/scheduler/run-once", s.runSchedulerOnce)
	return s.logging(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) cluster(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.controller.ClusterSnapshot())
}

type createSessionRequest struct {
	SessionID string             `json:"session_id"`
	Min       model.Resources    `json:"min_resource"`
	Max       model.Resources    `json:"max_resource"`
	Priority  model.TaskPriority `json:"task_priority"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var request createSessionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	session, err := s.controller.CreateSession(r.Context(), model.Session{
		ID: request.SessionID, Min: request.Min, Max: request.Max, Priority: request.Priority,
	})
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.controller.ListSessions()})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.controller.GetSession(r.PathValue("session_id"))
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

type updateStateRequest struct {
	State    model.SessionState `json:"session_state"`
	Priority model.TaskPriority `json:"task_priority"`
}

func (s *Server) updateState(w http.ResponseWriter, r *http.Request) {
	var request updateStateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	session, err := s.controller.UpdateState(r.PathValue("session_id"), request.State, request.Priority)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

type updateMetricsRequest struct {
	ActualCPUMilli        int64     `json:"actual_cpu_milli"`
	MemoryWorkingSetBytes int64     `json:"memory_working_set_bytes"`
	MemoryStableSince     time.Time `json:"memory_stable_since"`
}

func (s *Server) updateMetrics(w http.ResponseWriter, r *http.Request) {
	var request updateMetricsRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	session, err := s.controller.UpdateMetrics(r.PathValue("session_id"), request.ActualCPUMilli, request.MemoryWorkingSetBytes, request.MemoryStableSince)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

type resourceRequest struct {
	Desired    model.Resources    `json:"desired_resource"`
	Generation int64              `json:"generation"`
	Priority   model.TaskPriority `json:"priority"`
}

func (s *Server) requestResources(w http.ResponseWriter, r *http.Request) {
	var request resourceRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	session, err := s.controller.RequestResources(model.ResourceRequest{
		SessionID: r.PathValue("session_id"), Desired: request.Desired,
		Generation: request.Generation, Priority: request.Priority,
	})
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, session)
}

func (s *Server) suspendSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.controller.SuspendSession(r.Context(), r.PathValue("session_id"))
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) resumeSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.controller.ResumeSession(r.Context(), r.PathValue("session_id"))
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) finishSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.controller.FinishSession(r.Context(), r.PathValue("session_id"))
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) runSchedulerOnce(w http.ResponseWriter, r *http.Request) {
	plan, processed, err := s.controller.ProcessNext(r.Context())
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"processed": processed, "plan": plan})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func writeControllerError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrSessionNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, store.ErrSessionExists) || errors.Is(err, controller.ErrGenerationConflict) {
		status = http.StatusConflict
	} else if errors.Is(err, controller.ErrInsufficientMinimum) {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}
