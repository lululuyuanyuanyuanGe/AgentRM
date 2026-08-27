package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/daemon"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

type Server struct {
	daemon *daemon.Daemon
	logger *slog.Logger
}

func NewServer(nodeDaemon *daemon.Daemon, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{daemon: nodeDaemon, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/config", s.config)
	mux.HandleFunc("GET /v1/scheduler", s.schedulerSnapshot)
	mux.HandleFunc("POST /v1/jobs", s.registerJob)
	mux.HandleFunc("GET /v1/jobs", s.listJobs)
	mux.HandleFunc("GET /v1/jobs/{job_id}", s.getJob)
	mux.HandleFunc("POST /v1/jobs/{job_id}/finish", s.finishJob)
	mux.HandleFunc("POST /v1/tick", s.tick)
	return s.logging(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type levelResponse struct {
	Weight      int   `json:"weight"`
	QuantumUsec int64 `json:"quantum_usec,omitempty"`
}

func (s *Server) config(w http.ResponseWriter, _ *http.Request) {
	config := s.daemon.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"queues": map[string]levelResponse{
			"Q0": {Weight: config.Q0.Weight, QuantumUsec: config.Q0.Quantum.Microseconds()},
			"Q1": {Weight: config.Q1.Weight, QuantumUsec: config.Q1.Quantum.Microseconds()},
			"Q2": {Weight: config.Q2.Weight},
		},
		"q1_aging_millis": config.Q1Aging.Milliseconds(),
		"q2_aging_millis": config.Q2Aging.Milliseconds(),
		"boost_millis":    config.BoostInterval.Milliseconds(),
		"idle_weight":     config.IdleWeight,
	})
}

func (s *Server) schedulerSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.daemon.Snapshot())
}

type registerJobRequest struct {
	JobID      string `json:"job_id"`
	SandboxID  string `json:"sandbox_id"`
	CgroupPath string `json:"cgroup_path"`
}

func (s *Server) registerJob(w http.ResponseWriter, r *http.Request) {
	var request registerJobRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	job, err := s.daemon.RegisterJob(r.Context(), request.JobID, request.SandboxID, request.CgroupPath)
	if err != nil {
		writeDaemonError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.daemon.ListJobs()})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.daemon.GetJob(r.PathValue("job_id"))
	if err != nil {
		writeDaemonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type finishJobRequest struct {
	State model.JobState `json:"state"`
}

func (s *Server) finishJob(w http.ResponseWriter, r *http.Request) {
	request := finishJobRequest{State: model.JobFinished}
	if !decodeJSON(w, r, &request) {
		return
	}
	job, err := s.daemon.FinishJob(r.Context(), r.PathValue("job_id"), request.State)
	if err != nil {
		writeDaemonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) tick(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.daemon.Tick(r.Context()))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must contain one JSON object"})
		return false
	}
	return true
}

func writeDaemonError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrJobNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, store.ErrJobExists) || errors.Is(err, daemon.ErrCgroupBusy) {
		status = http.StatusConflict
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
