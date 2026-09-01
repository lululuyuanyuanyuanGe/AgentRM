package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
)

type Scheduler interface {
	Config() mlfq.SessionConfig
	Sandboxes() []model.SandboxEntity
	Snapshot() scheduler.Snapshot
}

type Server struct {
	scheduler Scheduler
	logger    *slog.Logger
}

func NewServer(nodeScheduler Scheduler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{scheduler: nodeScheduler, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/config", s.config)
	mux.HandleFunc("GET /v1/scheduler", s.schedulerSnapshot)
	mux.HandleFunc("GET /v1/sandboxes", s.listSandboxes)
	return s.logging(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type levelResponse struct {
	Weight   int   `json:"weight"`
	BudgetNS int64 `json:"budget_ns,omitempty"`
}

func (s *Server) config(w http.ResponseWriter, _ *http.Request) {
	config := s.scheduler.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"queues": map[string]levelResponse{
			"Q0": {Weight: config.Q0.Weight, BudgetNS: int64(config.Q0.Budget)},
			"Q1": {Weight: config.Q1.Weight, BudgetNS: int64(config.Q1.Budget)},
			"Q2": {Weight: config.Q2.Weight},
		},
		"boost_interval_seconds": config.BoostInterval.Seconds(),
		"scheduling_unit":        "agent_sandbox_pod",
		"accounting":             "ebpf_sched_switch",
	})
}

func (s *Server) schedulerSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.scheduler.Snapshot())
}

func (s *Server) listSandboxes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sandboxes": s.scheduler.Sandboxes()})
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
