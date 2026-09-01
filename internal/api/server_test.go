package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
)

type fakeScheduler struct{ sandboxes []model.SandboxEntity }

func (s fakeScheduler) Config() mlfq.SessionConfig       { return mlfq.DefaultSessionConfig() }
func (s fakeScheduler) Sandboxes() []model.SandboxEntity { return s.sandboxes }
func (s fakeScheduler) Snapshot() scheduler.Snapshot {
	return scheduler.Snapshot{Sandboxes: len(s.sandboxes), Q0: len(s.sandboxes)}
}

func TestReadOnlySandboxAPI(t *testing.T) {
	handler := NewServer(fakeScheduler{sandboxes: []model.SandboxEntity{{SandboxName: "session-a"}}}, nil).Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var payload struct {
		Sandboxes []model.SandboxEntity `json:"sandboxes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sandboxes) != 1 || payload.Sandboxes[0].SandboxName != "session-a" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestToolLifecycleEndpointWasRemoved(t *testing.T) {
	handler := NewServer(fakeScheduler{}, nil).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
